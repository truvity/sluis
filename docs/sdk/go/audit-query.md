# Go audit query client

```sh
go get github.com/truvity/sluis/audit/sdk@vX.Y.Z
```

`auditv1connect.QueryServiceClient` reads `audit.v1.QueryService`. It ships in the same module as the [emitter](audit-emitter.md). This page follows [`examples/read`](../../../audit/examples/read/main.go). The API is on [pkg.go.dev](https://pkg.go.dev/github.com/truvity/sluis/audit/sdk/gen/audit/v1). Access rules are in [read the trail](../../guides/audit/connect/read-the-trail.md) and the wire contract in the [API reference](../../reference/audit/api.md).

## Make a client

```go
import (
    auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
    "github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
)

client := auditv1connect.NewQueryServiceClient(httpClient, "https://audit-query.example.com")
```

Authentication is yours. Wrap the transport and set `Authorization: Bearer <token>` on every request. A program gets the token by [exchange](sluis.md#token-exchange). The service ANDs the caller's grants into every query.

```go
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
    r = r.Clone(r.Context())
    r.Header.Set("Authorization", "Bearer "+b.token)
    return http.DefaultTransport.RoundTrip(r)
}
```

## A filtered query

A `SearchRequest` names a profile and a filter. The filter is an OR-joined list of conjunctions of typed predicates. There is no free text. This one finds failed and denied actions of the last day under one prefix:

```go
query := &auditv1.SearchRequest{
    Profile: "security",
    Filter: []*auditv1.Filter{{
        OccurredAt: &auditv1.TimePredicate{Operator: &auditv1.TimePredicate_Between{Between: &auditv1.TimeRange{
            From: timestamppb.New(time.Now().Add(-24 * time.Hour)),
            To:   timestamppb.Now(),
        }}},
        Action:  &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_Prefix{Prefix: "shop.order."}},
        Outcome: &auditv1.StringPredicate{Operator: &auditv1.StringPredicate_In{In: &auditv1.StringList{Values: []string{"failure", "denied"}}}},
    }},
    Limit: 100,
}
page, err := client.Search(ctx, connect.NewRequest(query))
```

Records in `page.Msg.GetItems()` have typed getters such as `GetOccurredAt()` and `GetActor().GetId()`.

## Paging

Set the page cursor on the next request until a page comes back short or without one:

```go
for {
    page, err := client.Search(ctx, connect.NewRequest(query))
    if err != nil {
        return err
    }
    // ... use page.Msg.GetItems() ...
    next := page.Msg.GetCursors().GetNext()
    if len(page.Msg.GetItems()) < int(query.GetLimit()) || next == "" {
        break
    }
    query.Cursor = next
}
```

Keep the last `next` cursor. Asked later, it returns what was recorded since.

## One record and where it came from

```go
got, err := client.Get(ctx, connect.NewRequest(&auditv1.GetRequest{Profile: "security", Id: id}))
p := got.Msg.GetProvenance()   // p.GetObjectKey(), p.GetLine(), p.GetDigestId(), p.GetVerifiedAt()
```

Provenance names the archived copy and whether a seal vouches for it. The digest and time stay empty until seals exist. The service records every `Search` and `Get`.
