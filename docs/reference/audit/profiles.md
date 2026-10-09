# Profiles reference

What a framework profile says, what composing several produces, and what a deployment may relax. To choose, see [which profiles to compose](../../concepts/audit/which-profiles-to-compose.md).

| Term | Meaning |
|---|---|
| Framework profile | One standard's requirements as a file, such as `profiles/pci-dss.yaml`: required, optional and forbidden record fields, identity treatment, retention, integrity and review cadence |
| Profile | A deployment's named copy (`security`, `billing`), composed from framework profiles under `frameworks:`. The split writer produces one copy per profile |
| Install preset | How much of the system an installation provisions ([below](#install-presets)) |
| Grants preset | A named bundle of grant rules ([observe and query](configuration-observe-query.md#grants-file)) |

| Item | Value |
|---|---|
| Files | [`profiles/`](../../../audit/profiles/) |
| Meta-schema | [`profile.schema.json`](../../../audit/sdk/schemas/profile.schema.json) |
| Go package | `profile` |
| Refused key | `presets:` names framework profiles no longer; the message names `frameworks:` |
| Version-change event | `audit.preset.changed` (target type `preset`); renaming an archived action is a catalogue major version |

## Composition into a profile

The installation's configuration declares its profiles; only the application's catalogue names them.

```yaml
profiles:
  security:
    frameworks: [security, pci-dss]
  billing:
    frameworks: [billing-nl]
  history:
    frameworks: [history]
  evidence:
    frameworks: [evidence-etsi]
```

| Property | Rule |
|---|---|
| `field_classes` | Union |
| `required_fields`, `optional_fields`, `required_categories` | Union |
| `forbidden_fields`, `forbidden_pii` | Union; forbidden beats optional |
| `identity.<category>` | Strictest: omit > pseudonym > scoped > clear |
| `retention` | Longest; `minimum_days` may only be raised; `after_expiry` beats `fixed` |
| `integrity` | required > recommended. `object_lock_mode` (the least lock the store must run, [0056](../../decisions/0056-lock-modes-and-store-tiers.md)): compliance > governance > none. Daily > none. The `note` kept is the one behind the winning lock mode |
| `review.cadence` | Most frequent |
| `pipeline` | Longest windows; shortest `close_after_hours` |

## Install presets

| Preset | Provisions | Lowest for |
|---|---|---|
| `operational` | Writer, archive, deduplication, queue or HTTP intake. No notary, seal key, Object Lock or pseudonym keys; alarms off | `history` |
| `standard` | Adds the notary, its seal key and the alarms | `security`, `billing-nl` |
| `attested` | Adds compliance Object Lock and pseudonym keys | `dora`, `pci-dss`, `nen-7513`, `evidence-etsi` |

| Rule | Detail |
|---|---|
| `min_preset` | Each framework profile states the lowest preset it can be kept under. A profile's preset is the highest minimum among its framework profiles |
| `preset:` | A profile may ask for a stronger preset. A weaker one is refused, naming the profile and the framework profiles needing more (`profile.Deployment.ProfileNeeds`) |
| Configured presets | An installation configures the presets it uses, each with its own store. A profile whose preset is not configured is refused, naming both |
| Features | The notary, seal key, alarms and pseudonym keys are provisioned when any configured preset needs them (`profile.Deployment.Features`) |
| Object Lock | A property of the preset's bucket ([0068](../../decisions/0068-storage-is-configured-per-preset.md)) |

## Presets and their storage

```yaml
apiVersion: audit.truvity.github.io/audit-deployment/v2
presets:
  operational:
    bucket: example-audit
    prefix: operational/
    region: auto
    endpoint: https://<account>.r2.cloudflarestorage.com   # S3-compatible: set it; AWS S3: leave it out
    credentials: internal/audit/r2                         # address below the state root; endpoint only
  standard:
    bucket: example-audit-main
    prefix: standard/
    region: eu-central-1
    key_alias: alias/audit-archive                         # AWS S3 only; a name, never a key id or ARN
  attested:
    bucket: example-audit-locked
    prefix: attested/
    region: eu-central-1
profiles:
  security: {frameworks: [security], categories: [security]}
  history:  {frameworks: [history],  categories: [activity]}
```

| Key | Meaning |
|---|---|
| `bucket` | The bucket (required) |
| `prefix` | Every key of the preset lives under it; ends in `/`; required where the bucket is shared |
| `region` | The bucket's region; `auto` for a store at an endpoint |
| `endpoint` | URL of an S3-compatible store that is not AWS; empty is AWS S3 |
| `path_style` | Address the bucket as `endpoint/bucket/key`; with `endpoint` only |
| `credentials` | Address, below the process's `archive.stateRoot`, of the store's static credentials; with `endpoint` only |
| `credentials_preset` | `{account, minter, prototype, lifetime}`: mint R2 credentials from a Cloudflare prototype instead of reading static ones. Exclusive with `credentials`; with `endpoint` only ([prepare the bucket](../../guides/audit/operate/prepare-the-bucket.md)) |
| `key_alias` | KMS key alias encrypting the preset's objects; AWS S3 only; empty is the process's `archive.kmsKey` or the bucket's default |

| Rule | Detail |
|---|---|
| Object Lock | Only the `attested` preset's bucket: compliance mode on S3. An `attested` preset with an `endpoint` is refused, as is a profile demanding a stricter lock than its bucket gives. Other presets have no lock |
| At least one preset | `audit validate` and `audit profile explain` read documents with none. The writer, notary, indexer and query service do not |
| Process configuration | Keeps only `archive: {stateRoot, ca, kmsKey}` |
| A profile's store | Its records, seals and recorded compositions are in its preset's store |
| Every store | Catalogues and extension schemas, so each bucket reads alone |
| Strongest preset's store | Legal holds, sealed identities, dead letters, notary key delegations |
| Readers | The indexer, query service and `audit verify` read every configured preset's store |

## Destinations

Each profile is a destination: the prefix `records/<profile>/` of its preset's bucket, with its own framework profiles, retention and projection.

```yaml
profiles:
  security:
    frameworks: [security]
    categories: [security]
  activity:
    frameworks: [history]
    categories: [activity]
  billing:
    frameworks: [billing-nl]
    categories: [billing]
  evidence:
    frameworks: [evidence-etsi]
    categories: [security, billing]
```

| Rule | Detail |
|---|---|
| Action category | A catalogue declares one `category` per action. This differs from the framework event `categories` an action satisfies |
| Projection | The emitter sends each record once with every field. The writer stores only the fields a destination's profile keeps, under every destination whose `categories` include the action's category |
| `categories` | Lower-case names. A destination listing none keeps only actions naming it in `profiles` (a deprecated per-action list) |
| Unclaimed category | Reported once per action |
| Encryption key | The preset's `key_alias`; a profile has no key of its own. The Pulumi library creates no key: it looks the alias up and grants each role reading or writing the bucket the key behind it, conditioned on the storage KMS backend's encryption context |
| Retention | The destination's framework profiles' `retention`. The Pulumi library writes an S3 lifecycle rule per prefix expiring objects when a fixed retention ends, with Glacier steps before it |

## What a copy carries

A framework profile names fields as JSON pointers. It drops every field it does not name.

| List | Effect |
|---|---|
| `required_fields` | Must be present; kept |
| `optional_fields` | Kept when present |
| `forbidden_fields` | Never kept. Forbidding a field forbids everything beneath it: `/actor` and `/actor/id` are one rule |

| Rule | Detail |
|---|---|
| Parents | A listed child keeps its parent: `/outcome/result` needs `/outcome` |
| Extension properties | Kept when `x-audit-class` is in `field_classes` and `x-audit-pii` is not in `forbidden_pii` |
| `attributes`, `unmapped` | Class `audit` |

`audit profile explain <name>` prints the effective treatment per identity category, including the override below.

## What a deployment can relax

Composition only tightens, except for `external_identifiers_are_opaque`: declare it when the identifiers received for external people mean nothing outside your database.

| Case | Behaviour |
|---|---|
| Declared | A composed `external: pseudonym` is treated as `clear`. The writer refuses a record whose external actor, subject or person target carries a direct identifier (an address or whitespace), naming the field |
| Not declared, `keys.provider: none` | The writer refuses to start if the catalogue declares an external actor kind or a person target type ([0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)) |
| `internal`, `scoped`, `omit` | Never relaxed |

## Validation

The validator refuses:

| Case |
|---|
| A profile whose composed allow-list drops a required field |
| A profile whose required and forbidden sets intersect |
| A deployment whose registered catalogues lack a required category for a profile they emit into |
| A framework profile without citations or the disclaimer |
| A profile whose framework profiles require and forbid one field, directly or through an ancestor. Security (needs the actor) and billing (must not have it) cannot share a copy |

## Changing a profile

The writer compares each composed profile's fingerprint at start-up with the last composition under `schema/profile/<name>/`.

| Change | Result |
|---|---|
| First composition | Kept; not a change |
| Different composition | Kept beside the old one, both readable. Emits `audit.profile.changed` with both fingerprints |
| Different framework profile version | Emits `audit.preset.changed`, even when the composed rules are the same. Retention already set on written objects is unaffected |

Both actions use `block` delivery, recorded before the composition is written. A writer that cannot record the change does not start.

## The framework profiles

Each file cites its clauses and carries a disclaimer. Where a framework gives no number, the file carries a default marked `configurable`.

<!-- generated: framework-profiles -->
| framework profile | framework | retention default | identities: internal / external |
|---|---|---|---|
| `security` | NIS2 + ISO/IEC 27001:2022 + PCI DSS as the prescriptive floor | 365 days, 90 hot | clear / pseudonym |
| `billing-nl` | Dutch tax administration duty (AWR art. 52) | 7 years | omit / omit |
| `evidence-etsi` | ETSI EN 319 401 / 411 for trust service providers | 7 years after expiry | clear / pseudonym |
| `history` | product policy for tenant-facing activity | 365 days | **omit** / scoped |
| `pci-dss` | PCI DSS v4.0.1 Requirement 10 | 12 months, 3 hot | clear / pseudonym |
| `dora` | DORA RTS on ICT risk management, Art. 12 | entity-defined, 365 default | clear / pseudonym |
| `nen-7513` | NEN 7513 healthcare access logging | 5 years | clear / scoped |
<!-- /generated -->

`history` omits internal actors ([0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)). Composed with `security`, the stricter reading wins and the profile omits them.
