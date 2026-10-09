# Record reference

The record is defined by [record.proto](../../../audit/proto/audit/v1/record.proto). A buf plugin generates its JSON Schema during `just generate`:
[`gen/jsonschema/record.v1.schema.json`](../../../audit/gen/jsonschema/record.v1.schema.json), identifier `https://truvity.github.io/sluis/schemas/audit/v1/record.schema.json`.
The schema uses proto field names, enum names, 64-bit integers as strings and absent unpopulated fields. Every proto comment is a description.

The writer archives the proto of a record's major beside the schema on first use. The [profiles](profiles.md) decide which copy carries a field; `audit profile explain <name>` prints the result.

| field | set by | notes |
|---|---|---|
| `id` | emitter | UUIDv7; idempotency key at every hop |
| `occurred_at` | emitter | RFC 3339 UTC |
| `recorded_at` | writer | drives the tail cursor |
| `schema_version` | emitter | `major.minor` |
| `catalogue_version` | emitter | resolves extension schemas |
| `source` | emitter | namespace of `action` |
| `observer` | writer | verified publisher identity, version, instance |
| `sequence` | emitter | monotonic per instance |
| `action` | emitter | `resource.verb` under `source` |
| `operation` | emitter | seven values |
| `outcome` | emitter | result, reason, code |
| `tenant_id` | emitter | legal entity; `@platform` for the installation's own records |
| `subject` | emitter | kind and id; treated by kind category |
| `actor` | emitter | kind, id, session, auth method, credential hash, attributes |
| `targets[]` | emitter | type, id, name (non-person only), attributes |
| `context` | emitter | address chain, user agent, request, trace, span, areas |
| `capture` | emitter | level-gated request and response, truncated flag |
| `previous_attributes` | emitter | update actions only |
| `data` | emitter | extension slot keyed by action; each property carries its own class |
| `meter` | emitter | name, quantity, unit, kind, dimensions |
| `attributes` | emitter | bounded map |
| `unmapped` | emitter or adapter | what could not be mapped |
| `origin_hash` | writer | SHA-256 of the wide record |
| `profile` | writer | which copy this is |

## Bounds

| what | bound |
|---|---|
| `attributes` | 50 keys, key 64 chars, value 512 chars |
| `outcome.reason` | 512 chars |
| `context.user_agent` | 256 chars |
| `context.client_addresses` | 8 entries |
| `capture.request`, `capture.response` | 64 KiB each; the emitter drops a larger body |
| `targets` | 32 entries |
| truncation order | response, request, unmapped, attributes, reason |

## Negative list

The emitter library refuses these values.

| Class | Refused |
|---|---|
| Credentials | secrets, tokens, passwords, private keys, connection strings |
| Financial | card numbers, bank account numbers |
| Identity | session identifiers in clear, presented attribute values, user content, names, e-mail addresses |
| Property names | `password`, `secret`, `token`, `authorization` in extension schemas, unless annotated `x-audit-sensitive: redact` |
