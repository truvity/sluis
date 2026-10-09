# Read the trail

Read the audit trail through the query service as a person or program, or check the archive as an auditor without trusting anyone.

## Before you start

- Enable the query service (`query.enabled`) with grants naming your issuer, and get a bearer token from that issuer ([configuration](../../../reference/audit/configuration-observe-query.md#query-service)).

- A sort that leads with `recorded_at` is a tail and needs the `tail` operation. The reserved `q` field is refused as `unimplemented`.

## Steps

### Access

The query service trusts only the issuers its grants name, and a group's name can be the grant ([grants file](../../../reference/audit/configuration-observe-query.md#query-service)). Ask what a token may open:

```sh
curl -s …/audit.v1.QueryService/Access \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}'
```

`Access` lists the profiles, operations, tenants and period the caller holds. It reads no record and is not recorded.

### Search

Every method is a `POST` with a snake_case JSON body. The other methods and the operators are in the [API reference](../../../reference/audit/api.md).

```sh
curl -s https://audit-query.example.com/audit.v1.QueryService/Search \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{
    "profile": "security",
    "filter": [{
      "occurred_at": {"between": {"from": "2026-09-01T00:00:00Z", "to": "2026-09-18T00:00:00Z"}},
      "action":      {"prefix": "shop.order."},
      "outcome":     {"in": {"values": ["failure", "denied"]}}
    }],
    "limit": 100
  }'
```

Send `cursors.next` back as `"cursor"` with the same query for the next page. The last page's `next` stays valid: ask again later to tail. A search that matches nothing has no `items` key.

In `Get`, an empty `digest_id` means the hour is not sealed yet. Read an empty `verified_at` as not verified. `Resolve` returns `unimplemented` without a key provider, the default. Retry only `unavailable`.

### From Go

```go
client := auditv1connect.NewQueryServiceClient(httpClientWithYourBearer, "https://audit-query.example.com")
page, err := client.Search(ctx, connect.NewRequest(&auditv1.SearchRequest{Profile: "security", Limit: 100}))
```

See [`examples/read`](../../../../audit/examples/read/main.go).

### From TypeScript

Point the `@truvity` scope at `https://npm.pkg.github.com`, then `npm install @truvity/audit @truvity/audit-react`.

```ts
import { createConnectTransport } from "@connectrpc/connect-web";
import { compileQualifiers, createQueryClient, Sentencer } from "@truvity/audit";

const audit = createQueryClient(createConnectTransport({
  baseUrl: "https://audit-query.example.com",
  jsonOptions: { useProtoFieldName: true },
  interceptors: [(next) => async (req) => {
    req.header.set("Authorization", `Bearer ${await token()}`);
    return next(req);
  }],
}));

const { filter, errors } = compileQualifiers("outcome:failure,denied since:24h");
const page = await audit.search({ profile: "security", filter: [filter], limit: 100 });
const words = new Sentencer([myCatalogue]); // from `audit messages catalogue.yaml`
for (const r of page.items) console.log(words.sentence(r));
```

The view is in [connect an application](connect-an-application.md#the-audit-page). A console that draws its own uses the `useSearch`, `useTail`, `useFacets`, `useRecord` and `useAccess` hooks.

### For an auditor

With read-only access to the archive and nothing else, check every record object ingested in the range:

```sh
audit verify --profile security --from 2026-09-01 --to 2026-09-18 \
  --bucket example-audit --prefix audit/app
```

Exit status zero means nothing was found. An archive written before the v1 layout needs the v0.6.x CLI. See [verify the trail](../operate/verify-the-trail.md).

To hold the query service to its search contract, run this with a token that may read:

```sh
audit conformance --query https://audit-query.example.com --profile security \
  --token-file token --verified-before 48h
```

It only reads. A non-zero exit names the failed checks. With `--verified-before`, older records must be covered by a verified seal.

## Verify

Run `audit conformance` after changing grants.

## Decided in

[0055](../../../decisions/0055-no-pseudonymisation-keys-by-default.md), [0061](../../../decisions/0061-seals.md).
