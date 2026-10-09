# Which framework profiles does a deployment compose?

This repository ships seven [framework profiles](../../../audit/profiles/README.md). A deployment composes only the ones it needs. An uncomposed profile costs nothing. A composed profile costs storage for its retention and the controls it requires, such as a daily clock job, a legal-hold procedure or a review cadence.

## The seven side by side

| profile | keeps | retention | identities | demands | compose it |
|---|---|---|---|---|---|
| `security` | authentication, authorisation, privileged access, configuration change, key and secret use, every read of the trail | 365 days, 90 hot (minimum 180) | staff clear, external pseudonym | daily clock-synchronisation event, seals, reads logged; `object_lock_mode: none` | always |
| `billing-nl` | quantities per tenant and meter, no actor, no subject | 7 years | both omitted | seals; `object_lock_mode: none` | when the installation meters |
| `history` | what a tenant's administrator changed, as sentences | 365 days | staff by role, tenant's people scoped | seals | when a product shows activity to its tenants |
| `evidence-etsi` | credential and trust-service lifecycle facts | 7 years after expiry (10-year fallback) | staff clear, external pseudonym | daily clock synchronisation, timestamp anchor recommended, quarterly verification report | a trust-service deployment under audit |
| `pci-dss` | the security set at PCI's floor | 12 months, 3 hot (minimum 365 days) | staff clear, external pseudonym | daily review with dispositions | cardholder-data scope |
| `dora` | the security set plus logging-failure detection | 365 days, entity-defined | staff clear, external pseudonym | documented reference time source, monthly review | a supplier whose contract flows DORA down |
| `nen-7513` | every access to a person's care record, with role, subject, on-behalf-of and reason | 5 years | staff clear, external scoped | monthly review, overview on request | a healthcare deployment |

## The policy

Compose `security` in every installation. It covers everything the component does to itself: every read, verification and job. It demands `clock_sync_event: daily`, so the chart refuses to render without a reference clock for the clock-synchronisation job.

Compose `billing-nl` where the installation meters. It keeps quantities for seven years and nothing about a person. It does not replace `security`: a metering installation composes both, and one record lands in two copies with two retentions.

Compose `history` only where a tenant reads their own history. The operator's staff appear there by kind and role, never by identity, so the profile needs no key provider.

Keep `evidence-etsi`, `pci-dss`, `dora` and `nen-7513` as files until a contract or regulator demands one. Each raises retention. `pci-dss` obliges someone to review daily and keep the dispositions. Record the decision where the deployment's other decisions live.

## Composing more than one

Composition is a union that only tightens: the longest retention, the stricter identity treatment, the forbidden field and the most frequent review win. The rules are in [the profiles reference](../../reference/audit/profiles.md).

`audit profile explain <name>` prints what a profile keeps after composition. That includes `external_identifiers_are_opaque`, which relaxes a `pseudonym` to `clear` when the deployment declares that its external identifiers carry nothing direct.

## Traps before composing

Retention cannot be shortened later. Under Object Lock in compliance mode, an object written under a seven-year profile stays seven years.

`pci-dss`, `nen-7513`, `dora` and `evidence-etsi` demand Object Lock in compliance mode, so the bucket needs it. `security`, `history` and `billing-nl` need only seals under a managed key. They run on any S3-compatible store, with a preset of `operational` or `standard` that writes without a lock.

A component refuses to start when the store is weaker than a composed profile demands.

A profile that requires a category nobody emits also refuses to start. The writer checks the registered catalogue against the profile's required categories, so `nen-7513` fails at start-up when the application records no patient-record access.

## Decided in

- [0044 Profiles composed from framework profiles](../../decisions/0044-profiles-composed-from-framework-profiles.md)
- [0055 No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md)
- [0056 Lock modes and store tiers](../../decisions/0056-lock-modes-and-store-tiers.md)
- [0068 Storage is configured per preset](../../decisions/0068-storage-is-configured-per-preset.md)
