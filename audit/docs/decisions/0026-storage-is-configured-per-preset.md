# 0026. Storage is configured per install preset

- Status: accepted; refines [0014](0014-lock-modes-and-store-tiers.md) and [0023](0023-archive-retention-and-lifecycle.md)
- Date: 2026-10-08

## Context

An installation had one archive: one bucket, one lock mode, one key, in every
component's configuration. Destinations (profiles) then came to differ in what
they needed: an operational trail that is happy on an S3-compatible store, a
standard one on S3 with a key of its own, an attested one under compliance Object
Lock. One bucket could not be all three, and the lock mode, the key and the
endpoint were settings repeated in every component's file.

## Decision

**An installation configures the install presets it uses, and each preset has a
store of its own**, in the deployment document every component already reads:

```yaml
presets:
  operational: {bucket: ..., prefix: operational/, region: auto, endpoint: https://..., credentials: internal/audit/r2}
  standard:    {bucket: ..., prefix: standard/, region: eu-central-1, key_alias: alias/audit-archive}
  attested:    {bucket: ..., prefix: attested/, region: eu-central-1}
```

- A profile\'s preset is derived as before (the highest `min_preset` of its
  framework profiles, or a stronger `preset` it asks for) and **must be
  configured**; a profile whose preset is not is refused, naming both.
- **Object Lock is a property of the preset\'s bucket**: compliance for
  `attested`, on S3 only (an attested preset with an endpoint is refused), none
  for every other preset. The per-destination lock and the process\'s `lockMode`
  are gone.
- A profile\'s records, seals and compositions are in its preset\'s store; the
  catalogues and schemas are in every store (each bucket stays readable on its
  own); holds, sealed identities, dead letters and key delegations are in the
  strongest preset\'s. A `routed` store addresses them as one archive, so the
  notary, the indexer and the query service read every preset\'s store through
  the interface they had.
- The notary, seal key, alarms and pseudonym keys turn on when any configured
  preset needs them.
- The key alias moves from the destination to the preset. The process
  configuration keeps `archive: {stateRoot, ca, kmsKey}` (the state root the
  `credentials` address is read below, a CA bundle, a default key).

## Consequences

- Breaking: process configurations lose `archive.bucket`, `prefix`, `lockMode`
  and `credentials`; the observe and notary configurations name the `deployment`;
  a version-1 file that names the bucket is refused with the pointer to `presets`.
  The chart and both Pulumi libraries take `presets` in place of the single
  archive.
- A preset nobody uses can be configured (its bucket is opened), and a profile
  cannot silently land in a preset below the one it needs.
- Roles that read are granted per preset bucket; per-profile reader roles are a
  follow-up.
