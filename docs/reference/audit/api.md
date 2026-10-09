# API reference

ConnectRPC serves [sink.proto](../../../audit/proto/audit/v1/sink.proto), [registry.proto](../../../audit/proto/audit/v1/registry.proto) and [query.proto](../../../audit/proto/audit/v1/query.proto) as snake_case JSON with RFC 3339 timestamps. An empty search has no `items` key.

| Process | Serves |
|---|---|
| Receiver (`audit-writer`) | `SinkService`, `RegistryService` ([0053](../../decisions/0053-one-installation-per-service-or-product.md)) |
| Query service (`audit-query`) | `QueryService` |

## Write

`audit.v1.SinkService/Write` takes a batch with a delivery, from workloads and adapters, never browsers. The response gives the accepted count, each `Rejection` with a reason, and the `Durability`. A structural refusal, such as an unknown catalogue version, refuses the whole batch. Decided in [0054](../../decisions/0054-two-deliveries-and-a-durable-ack.md) and [0059](../../decisions/0059-sink-durability-and-transports.md).

| Catalogue `delivery` | On the wire | The call returns |
|---|---|---|
| `block` | `DELIVERY_BLOCK` | When the records are durable at the next hop |
| `async` (default) | `DELIVERY_ASYNC` | At once; the record waits in the emitter's bounded queue |

| `durability` | Means |
|---|---|
| `DURABILITY_ARCHIVED` | The records are in the archive's bucket; the writer's own put reports this |
| `DURABILITY_QUEUED` | A replicated queue holds them; the JetStream and SQS publishers report this after every acknowledgement |
| `DURABILITY_LOGGED` | The process wrote them to its log |
| `DURABILITY_UNSPECIFIED` | The hop did not say; weaker than all others |

A higher value survives more. A hop reports its next hop's answer; a wrapper never reports more.

| Go call | Effect |
|---|---|
| `Guarantees()` | A sink declares the best it will ever report |
| `sink.Require(s, min)` | Refuses at start-up a chain that cannot give `min` |
| `sink.Guard(s, min)` | Does that, and fails any write whose acknowledgement is weaker at run time |
| `Client.Expecting` | States what a Connect client's far side gives, which it cannot learn |

`DELIVERY_OUTBOX` and `DELIVERY_BEST_EFFORT` are deprecated and never produced.

## Catalogue registration

The receiver serves `audit.v1.RegistryService` on `Write`'s port.

| Call | Behaviour |
|---|---|
| `RegisterCatalogue` | Called at start-up with the catalogue document and its extension schemas. An empty `problems` list means registered. A malformed catalogue returns its findings and the application does not start. The first use of a version copies it into the archive beside its records |
| `GetCatalogue`, `ListCatalogues` | Answer what this installation has been told. The list is short: an installation admits one application |

## Query

`audit.v1.QueryService/Search`:

```json
{
  "profile": "security",
  "filter": [
    {
      "occurred_at": {"between": {"from": "2026-09-01T00:00:00Z", "to": "2026-09-17T00:00:00Z"}},
      "action":      {"prefix": "wallet.credential."},
      "outcome":     {"in": {"values": ["failure", "denied"]}},
      "targets":     {"in": {"values": [{"type": "credential"}]}}
    },
    { "actor_id": {"equal": "…"} }
  ],
  "sort": [{"field": "FIELD_OCCURRED_AT", "order": "ORDER_DESC"}, {"field": "FIELD_ID", "order": "ORDER_ASC"}],
  "limit": 100
}
```

Response:

```json
{
  "items": [ … ],
  "query": { "profile": "security", "filter": [ … ], "sort": [ … ], "limit": 100 },
  "cursors": { "self": "…", "first": "…", "prev": "…", "next": "…" }
}
```

`first` restarts the same question. There is no `last`. The service refuses operators not built.

| Type | Operators | Built |
|---|---|---|
| string | equal, not_equal, in, not_in, prefix | yes |
| string | is_null, is_not_null | no |
| time | between, greater_than, greater_than_or_equal, less_than, less_than_or_equal | yes, each as a half-open range |
| integer | equal | yes |
| integer | not_equal, in, not_in, between, comparisons | no |
| targets | in, not_in (an empty id matches the type) | yes |
| attributes | equal, not_equal, key_is_null, key_is_not_null | no |
| data path | string, integer or time predicate on a filterable property | yes |

A predicate on `id` takes whole UUIDs; anything else is `invalid_argument`. `Get` of a non-UUID is `not_found`.

`Facets` counts values of fields under the same filter:

```json
{"profile": "security", "filter": [ … ], "fields": ["action", "outcome"], "limit_per_field": 10}
→ {"facets": [{"field": "action", "values": [{"value": "shop.order.placed", "count": "42"}]}]}
```

`Get` returns one record and its copy's location. `digest_id` and `verified_at` stay empty until seals ([0061](../../decisions/0061-seals.md)) set them:

```json
{"profile": "security", "id": "0199b100-…"}
→ {"record": { … },
   "provenance": {"object_key": "records/security/acme/2026/09/17/10/01K5…", "line": "3",
                  "digest_id": "", "verified_at": ""}}
```

`Export` starts a job. `GetExport` polls it and returns an expiring signed link:

```json
{"profile": "security", "filter": [ … ], "format": "FORMAT_NDJSON"}
→ {"job_id": "x-…", "state": "EXPORT_STATE_PENDING"}

{"job_id": "x-…"}
→ {"state": "EXPORT_STATE_READY", "url": "https://…", "expires_at": "…", "records": "1204"}
```

| Limit | Value |
|---|---|
| `filter` | 4 terms |
| `sort` | 4 terms |
| `in` | 100 values |
| `limit` | 1000; a larger value is cut to it |
| Export | 100000 records; a larger match fails the job, asking to narrow the filter |
| Export recording | `audit.export.requested` is recorded before any read; if it cannot be, the export does not start |

A grant's window narrows a wider request. 64-bit integers (`count`, `line`, `records`) are JSON strings.

## Access

`audit.v1.QueryService/Access` lists each profile a grant names with the caller's operations, tenants and period. It reads no record and is not recorded. Profiles with no effective operation are not listed. `AuditView` calls it when the host passes no profiles.

```json
{}
→ {"profiles": [
    {"profile": "security", "operations": ["search", "facets", "get", "tail", "export"],
     "all_tenants": true},
    {"profile": "history", "operations": ["search", "get"], "tenants": ["acme"],
     "from": "2026-07-01T00:00:00Z"}
  ]}
```

## Resolve

`audit.v1.QueryService/Resolve` maps a pseudonym to its identity: `{"profile": "security", "tenant_id": "acme", "pseudonym": "ps_…"}` returns `{"identifier": "…"}`. Name the profile that carried it.

| Rule | Detail |
|---|---|
| No key provider | `unimplemented`. This is the default ([0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)) |
| Offered when | The service has the keys: `keys` and `archive` in `audit-query`'s configuration (local provider with `keys.local.rootFile` and `.dir`, or `transit` with a login that may decrypt) |
| Permission | The `resolve` operation on the profile, from an explicit rule only. No read grant or group name implies it. The tenant must be one the grant covers |
| Audit | `audit.pseudonym.resolved` is recorded and confirmed before the identity returns. If the trail cannot take it, nothing resolves. It names the pseudonym and the rule, never the identity |
| Scope | Only actor and subject pseudonyms. The writer seals each identifier under the tenant's key (`identity/` in the archive) |
| Erasure | Destroying the key makes resolve fail with `failed_precondition` |
| `x-audit-sensitive: hmac` | Findable, never readable |

## Errors

| Connect code | When |
|---|---|
| `invalid_argument` | A malformed filter, or a cursor from a different query |
| `permission_denied` | The grant excludes the profile, the operation or the tenant |
| `resource_exhausted` | A published limit or an export cap exceeded |
| `failed_precondition` | Resolve of a pseudonym whose tenant key was destroyed; retrying never helps |
| `not_found` | No such record, and also a record outside the grant, so a caller cannot learn it exists |
| `unimplemented` | Resolve with no key provider; export with no bucket configured |
| `unavailable` | The searcher is down. Also the default for anything unrecognised |
