# Go audit query client

```sh
go get github.com/truvity/sluis/audit/sdk@vX.Y.Z
```

The query service is a Connect service, `audit.v1.QueryService`. Go reads it with the generated client
`auditv1connect.QueryServiceClient`, in the same module as the [emitter](audit-emitter.md). This page walks through
[`examples/read`](../../../audit/examples/read/main.go), which is compiled on every run of the gate; the whole API is on
[pkg.go.dev](https://pkg.go.dev/github.com/truvity/sluis/audit/sdk/gen/audit/v1). What the service does with a
request, and who may see what, is in [read the trail](../../guides/audit/connect/read-the-trail.md); the wire contract is the
[API reference](../../reference/audit/api.md). The other SDKs are listed in [the overview](../README.md).

## Make a client

```go
import (
    auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
    "github.com/truvity/sluis/audit/sdk/gen/audit/v1/auditv1connect"
)

client := auditv1connect.NewQueryServiceClient(httpClient, "https://audit-query.example.com")
```

The client sends what `httpClient` sends. **Authentication is the caller's:** the service verifies a bearer token from
an issuer its grants name, so wrap the transport and set `Authorization: Bearer <token>` on every request, with a token
the caller obtained its own way (for a program, an exchange at the issuer; see [the Go module](sluis.md#exchange-and-the-two-credential-shapes)).
What the token's holder may read is decided by the service's grants and is AND-ed into every query, so a caller never
sees outside its grant.

```go
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
    r = r.Clone(r.Context())
    r.Header.Set("Authorization", "Bearer "+b.token)
    return http.DefaultTransport.RoundTrip(r)
}
```

## A filtered query

A `SearchRequest` names a profile and a filter. The filter is a list of conjunctions of typed predicates, OR-joined;
there is no free text. This one asks for failed and denied actions of the last day under one prefix:

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

Every record in `page.Msg.GetItems()` has typed getters: `GetOccurredAt()`, `GetAction()`, `GetActor().GetId()`,
`GetOutcome().GetResult()`.

## Paging

A page carries a cursor. Set it on the next request until a page comes back short or without one:

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

Keep the last `next` cursor: asked again later it returns what was recorded since, which is what a tail is.

## One record and where it came from

```go
got, err := client.Get(ctx, connect.NewRequest(&auditv1.GetRequest{Profile: "security", Id: id}))
p := got.Msg.GetProvenance()   // p.GetObjectKey(), p.GetLine(), p.GetDigestId(), p.GetVerifiedAt()
```

The provenance says where the archived copy lives and whether a seal has vouched for it; the digest and the time stay
empty until seals exist. Every read, `Search` and `Get` alike, is itself recorded by the service.
