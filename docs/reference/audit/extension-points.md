# Extension points

Proto defines the compiled core record; JSON Schema defines the loaded extensions. An application's schemas live in its repository and reach the receiver with its [catalogue](catalogue.md). The slots and annotations belong to the contract beside [the record](record.md).

| slot | keyed by | registered by |
|---|---|---|
| `data` | `action` | the application |
| `targets[].attributes` | `targets[].type` | whoever owns the type |
| `actor.attributes` | `actor.kind` | the application |
| `context.areas.<area>` | area name | the application, in its catalogue |
| `meter.dimensions` | `meter.name` | the application |

## Extension schema rules

[extension.schema.json](../../../audit/sdk/schemas/extension.schema.json) enforces these.

| Rule | Detail |
|---|---|
| Shape | Closed object (`additionalProperties: false`), URI `$id` under the source's namespace |
| `x-audit-class` | Required on every property: `shared`, `audit`, `metering`, `history` or `evidence` |
| `x-audit-pii` | Required on every property: `none` or `identifier`; `direct` and `content` are refused |
| Types | Facetable properties and meter dimensions are primitives; depth and string length are bounded; no binary |
| `x-audit-filter`, `x-audit-sensitive` (`hmac` or `redact`), `x-ocsf-path`, `x-ecs-path` | Optional |
| `x-audit-expiry: true` | Optional, on a `string` with `format: date-time`: when the credential or certificate expires |
| `extends: /pointer` | On an action: names the data property (an id, or a list) of the earlier records it adds to; the schema must mark an expiry |

### Retention by expiry

| Step | Behaviour |
|---|---|
| Lock | A profile retained `after_expiry` locks the object until the expiry plus its years, or its fallback if later. An object with several such records locks for the latest |
| Source | The writer reads the expiry from the record as written, so it applies to copies whose profile drops the data slot. A record without one gets the fallback |
| Extension | After an addendum is durable, the writer finds each earlier record by index, or by a scan within its budget and horizon, in every `after_expiry` profile that keeps the addendum. It lengthens the object's lock to the addendum's expiry plus the years |
| Limits | A lock never shortens, so a shorter expiry changes nothing. An earlier record of another tenant is refused |
| Audit | Each extension and each failure with its reason is an `audit.retention.extended` record under the addendum's tenant. A failure never fails the batch and is counted for an alert |

## Example

```json
{
  "$id": "https://schemas.example/wallet/credential-issued/v2.json",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "credential_format": { "type": "string", "enum": ["sd-jwt-vc", "mdoc"],
                           "x-audit-class": "shared", "x-audit-pii": "none",
                           "x-audit-facet": true },
    "credential_id":     { "type": "string", "x-audit-class": "evidence",
                           "x-audit-pii": "none", "x-audit-filter": true },
    "attestation_bytes": { "type": "integer", "x-audit-class": "metering",
                           "x-audit-pii": "none" }
  }
}
```

## Component use

| Component | Uses |
|---|---|
| Emitter | Validates against the composed schema before publishing |
| [Split writer](../../concepts/audit/split-writer.md) | Routes each property to the profiles whose classes include it; applies each `identifier` property the treatment of its kind's category in the profile (clear or dropped without a key provider, [0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)); applies `x-audit-sensitive` |
| Indexer | Creates facet columns and typed filters from `x-audit-facet` and `x-audit-filter` |
| [Audit page](../../concepts/audit/audit-page.md) | Renders the detail panel, facets and filters from `title`, `description`, `enum` and `format` |
| Exporters | Map by `x-ocsf-path` and `x-ecs-path` |

A property in two sources' slots is promoted to the core in the next minor. Adding to the annotation vocabulary needs a decision record.
