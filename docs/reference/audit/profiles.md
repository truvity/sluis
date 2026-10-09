# Profiles reference

What a framework profile says, what composing several into a profile produces, and what a
deployment may relax.

**Terminology.** A **framework profile** is one standard's requirements as a file, such as
`profiles/pci-dss.yaml`: which record fields must, may and may never be kept, how identities are
treated, how long copies live, what integrity applies and how often the trail is reviewed. A
**profile** is a deployment's own named copy (`security`, `billing`), composed from one or more
framework profiles listed under the key `frameworks:`; the split writer produces one copy per profile. These
files used to be called *presets* and the key `presets:`, which is now refused with a message naming
`frameworks:`. A *preset* is reserved for a named bundle of adapter or deployment choices
([policy decision 0012](https://github.com/truvity/policy/blob/master/docs/decisions/0054-stabilization-amendments.md)).
The directory is [`profiles/`](../../../audit/profiles/), the meta-schema is
[`profile.schema.json`](../../../audit/sdk/schemas/profile.schema.json), the Go package is `profile`. The
event that records a framework profile's version changing keeps its name, `audit.preset.changed`
(and its target type `preset`), because a catalogue's action names are archived and renaming one is a
catalogue major version.

Which framework profiles an installation is expected to compose, and what each costs to run, is
[which profiles to compose](../../concepts/audit/which-profiles-to-compose.md).

## Composition into a profile

A profile is declared in the installation's configuration, and the profiles
are that installation's own: the application names them in its catalogue,
and nothing else composes them.

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

Composition rules:

| property | rule |
|---|---|
| `field_classes` | union |
| `required_fields`, `optional_fields`, `required_categories` | union |
| `forbidden_fields`, `forbidden_pii` | union; forbidden beats optional |
| `identity.<category>` | strictest: omit > pseudonym > scoped > clear |
| `retention` | longest; `minimum_days` may only be raised; `after_expiry` beats `fixed` |
| `integrity` | required > recommended; compliance > governance > none for `object_lock_mode`, which is the least lock the store must run ([0056](../../decisions/0056-lock-modes-and-store-tiers.md)); daily > none. The `note` kept is the one behind the lock mode that won |
| `review.cadence` | most frequent |
| `pipeline` | longest windows; shortest `close_after_hours` |

## Install presets

An **install preset** says how much of the system an installation provisions. It is not a
framework profile and not a grant preset.

| preset | provisions |
|---|---|
| `operational` | the writer, the archive, deduplication, queue or HTTP intake. No notary, seal key, Object Lock or pseudonym keys; alarms off |
| `standard` | adds the notary and its seal key, and the alarms |
| `attested` | adds compliance Object Lock and the pseudonym keys |

A profile's preset is derived. Each framework profile states the lowest preset it can be kept
under as `min_preset` (`history` is `operational`; `security` and `billing-nl` are `standard`;
`dora`, `pci-dss`, `nen-7513` and `evidence-etsi` are `attested`), and a profile's preset is the
highest minimum over the framework profiles it is composed from. A profile may ask for a stronger
one with `preset:`; a weaker one is refused, naming the profile and the framework profiles that
need more (`profile.Deployment.ProfileNeeds`).

An installation configures **the presets it uses**, and each preset has a store of its own. A
profile's preset must be one of them: a profile whose preset is not configured is refused, naming
both. The notary, seal key, alarms and pseudonym keys are provisioned when **any** configured preset
needs them (`profile.Deployment.Features`); Object Lock is a property of the preset's bucket
([0068](../../decisions/0068-storage-is-configured-per-preset.md)).

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

| key | meaning |
|---|---|
| `bucket` | the bucket (required) |
| `prefix` | every key of this preset lives under it; ends in `/`; required wherever the bucket is shared |
| `region` | the bucket's region; `auto` for a store at an endpoint |
| `endpoint` | the URL of an S3-compatible store that is not AWS; empty is AWS S3 |
| `path_style` | address the bucket as `endpoint/bucket/key`; with `endpoint` only |
| `credentials` | the address, below the process's `archive.stateRoot`, of the store's static credentials; with `endpoint` only |
| `credentials_ref` | `external/cloudflare/<preset>`: the R2 credentials a sluis installation rotates, read below the process's `archive.sluisRoot` or `archive.sluisDir`; exclusive with `credentials` and `credentials_preset`, with `endpoint` only (see [put the archive on R2](../../guides/audit/operate/archive-on-r2.md)) |
| `credentials_preset` | `{account, minter, prototype, lifetime}`: mint the store's R2 credentials for the process from a Cloudflare prototype, which puts the minter in every process; exclusive with `credentials` and `credentials_ref`, with `endpoint` only (see [prepare the bucket](../../guides/audit/operate/prepare-the-bucket.md)) |
| `key_alias` | the KMS key alias the preset's objects are encrypted under; AWS S3 only; empty is the process's `archive.kmsKey` or the bucket's default |

- **Object Lock is the `attested` preset's bucket alone**: compliance mode on S3. An `attested`
  preset with an `endpoint` is refused, and so is a profile whose framework profiles demand a stricter
  lock than its preset's bucket gives. Every other preset is written without a lock, so it writes
  nothing it cannot clear.
- The deployment document needs at least one preset; `audit validate` and `audit profile explain`
  read documents that have none, the writer, the notary, the indexer and the query service do not.
- The process configuration keeps only `archive: {stateRoot, sluisRoot, sluisDir, ca, kmsKey}`; where the archive is,
  is this document's.
- A profile's records, seals and recorded compositions are in its preset's store. The catalogues and
  extension schemas that describe the records are in every store, so that each bucket can be read
  without the others. Legal holds, sealed identities, dead letters and the notary's key
  delegations are in the store of the strongest configured preset.
- Readers (the indexer, the query service, `audit verify`) read every configured preset's store.

## Destinations

Each profile of the deployment document is a **destination**: a prefix of its preset's bucket
(`records/<profile>/`) with its own framework profiles, its own retention, and its own projection of
every record.

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

- A catalogue declares one `category` per action (not `categories`, which are the framework event
  categories an action satisfies). The emitter sends a record once, with every field; the writer
  stores a projection of it, only the fields that destination's profile keeps, under every
  destination whose `categories` include the action's category. The deprecated `profiles` list on an
  action still names destinations directly. A category nobody takes is reported once per action.
- `categories` are lower-case names. A destination that lists none keeps only the actions that
  name it in `profiles`.
- The encryption key is the **preset's** `key_alias` (the estate creates the key; the Pulumi library
  creates none, looks the alias up and grants every role that reads or writes the preset's bucket the
  key behind it, conditioned on the encryption context the storage KMS backend uses). A profile has no
  key of its own.
- Retention is the destination's framework profiles' (`retention`): the Pulumi library writes an S3
  lifecycle rule per prefix that expires its objects when a fixed retention ends, with the
  Glacier steps before it.

## What a copy carries

A framework profile names fields as JSON pointers in three lists, and **anything it does
not name is dropped**. A copy carries what its purpose justifies and nothing
more, so adding a field to the record does not quietly widen every copy of it.

- `required_fields` must be present, and are kept.
- `optional_fields` are kept when the record has them.
- `forbidden_fields` are never kept. Forbidding a field forbids everything
  beneath it, so `/actor` and `/actor/id` are one rule.

A listed child keeps the parent that has to carry it: a copy cannot hold
`/outcome/result` without `/outcome`.

Extension properties are not named field by field. They are kept when their
`x-audit-class` is in the profile's `field_classes` and their `x-audit-pii` is
not in `forbidden_pii`. The free-form bags (`attributes`, `unmapped`) count as
class `audit`.

`audit profile explain <name>` prints the result for a deployment, which is what
a reviewer checks the deployment against. It reports the **effective**
treatment per identity category, including the override below, so what is
actually written is inspectable rather than inferred from the framework profile files.

## What a deployment can relax

Composition only ever tightens, with one exception. A deployment declares
`external_identifiers_are_opaque` when the identifiers it receives for
external people are ones it minted itself and that mean nothing outside its
own database. When it does, a composed profile's `external: pseudonym` is
treated as `clear`, and the writer refuses a record whose external actor,
subject or person target carries something that looks like a direct
identifier — an address, or whitespace — naming the field and why.

This is what lets `keys.provider: none` be the default: a profile that asks
for a pseudonym does not force a key provider on a deployment that has
nothing to pseudonymise
([0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)). When the
declaration is false and the provider is `none`, the writer refuses to start
if the registered catalogue declares an external actor kind or a person
target type: a deployment chooses, and may not arrive at clear-text
addresses in a locked archive by omission.

Nothing relaxes `internal`, `scoped` or `omit`.

The `history` framework profile was reworked for the same reason. It treated internal
actors as `pseudonym`, which made a tenant-facing view need a key provider
before it would render. It now omits them, so a staff actor is shown by kind
and role and never by identity, and `external: scoped` stays.

## Validation

The validator refuses:

- a profile whose composed allow-list drops a required field;
- a profile whose required and forbidden sets intersect;
- a deployment whose registered catalogues lack a required category for a
  profile they emit into;
- a framework profile without citations or the disclaimer;
- a profile whose framework profiles both require and forbid a field, directly or through
  an ancestor. Composing the security and billing framework profiles into one copy is
  refused for exactly this reason: security must know who acted and billing must
  not, which is why they are separate copies rather than one with a compromise.

## Changing a framework profile in use

Emits `audit.preset.changed` with block delivery. Retention already set on
written objects is unaffected; only new objects take the new value.

## Changing a profile

A profile's rules decide what every record written under it means, so a change
is itself recorded. At start-up the writer fingerprints each composed profile
and compares it with the last composition in the archive, under
`schema/profile/<name>/`:

- the first composition a deployment records is kept there and is not a change;
- a different composition is kept beside the old one (both stay readable, so
  "what did this profile keep in April" has an answer) and emits
  `audit.profile.changed` with both fingerprints;
- a different framework profile version emits `audit.preset.changed`, even when the rules
  it composes to are the same — a library upgrade changes meaning nobody typed.

Both actions are declared `block`: a writer that cannot record the change does
not start. The events are recorded before the new composition is written, so a
writer stopped in between records the change again next time rather than never.

## The framework profiles

The framework profiles this repository ships, from `profiles/*.yaml`. Each cites the clauses it
reads and carries a disclaimer. Where a framework gives no number (NIS2, ISO 27001 and DORA all say
"define it yourself"), the file carries a defended default and marks it `configurable`.

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

`history` omits internal actors rather than pseudonymising them, which is what lets a tenant-facing
view of activity render with no key provider: a staff actor is shown by kind and role, never by
identity ([0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)). Composed with `security`,
which keeps staff in clear, the stricter reading wins and the composed profile omits them. A
deployment that has declared its external identifiers opaque gets `clear` where the table says
`pseudonym`; `audit profile explain <name>` prints the effective treatment.
