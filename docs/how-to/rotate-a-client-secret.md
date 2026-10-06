# Rotate a client secret

## Purpose

Replace the secret of a client the issuer generates, without cutting off the relying party between the new value being
made and being deployed.

## Preconditions

- The client is declared `secret: {generate: true}` and has a stored record
  ([let the issuer generate it](let-the-issuer-generate-a-clients-secret.md)).
- You are signed in with `sluisctl login` and your token holds the operators group. The call is accepted only with a
  token issued to `accessctl` (sluisctl's default client) or `console`; a token for any other client is refused even for
  an operator.

## Before you start

- **The old secret stays valid for the overlap.** Default 24h, at most 7d; `0` is a hard cut. Choose it longer than the
  relying party's export refresh plus its own refresh. A rotation made during an open overlap drops the older previous
  secret, and says so.
- **Other replicas learn within 30 seconds.** The replica that served the request forgets the client at once; the others
  cache a record for 30 seconds. After a hard cut, another replica may accept the old current secret for up to 30
  seconds.
- **A replica whose store reads fail** serves the last good record it read for about 5 minutes, then refuses the client.
- **Rotated-away values stay in the store's version history** (SSM parameter history, OpenBao KV versions), readable only
  by whoever can read the store.
- Nothing here prints a secret.

## Steps

### 1. Rotate

**Run** `sluisctl clients rotate <id> [--overlap 24h]`.
**Expect** the new secret is stored, the old one is accepted until the printed time, and an exported copy is written at
once (or at the export's `interval` where it is not).
**Verify** `sluisctl clients show <id>`: `rotated` is now, `previous still valid` is `true` until the overlap ends.
**Rollback**: none; rotate again. A refusal says why: the client is not generated, has no record yet, or is being
changed by another call (try again).

### 2. Confirm the relying party picked it up

**Run** check the relying party's secret operator has the new value and a sign-in works.
**Expect** a sign-in succeeds with the new secret. The issuer counts which one matched in `sluis.client_secret.auth`
(`current`, `previous`, `none`).
**Verify** `previous` falls to zero before the overlap ends.
**Rollback**: lengthen the next rotation's overlap.

## Retire a generated client

When a client stops being generated (removed, or back to a named input) its record is reported once as orphaned
(`roster.client.secret.orphaned`) and kept. Delete it only when you mean it:

**Run** `sluisctl clients purge <id>`, which is refused while the client is still a generated client of the policy.
Then delete the input secret `clients/<id>/secret` and the exported copy at the export's target, by hand. The issuer
touches neither.
**Expect** `roster.client.secret.deleted`.

## What the audit trail records

`roster.client.secret.created`, `.adopted`, `.rotated`, `.orphaned`, `.deleted` and `.denied` on a `client` target, with
the client, the times and the overlap, never a value
([audit actions](../reference/audit-actions.md)). A verified caller who is refused (not an operator, wrong audience,
busy, not generated, still declared, no record, a bad overlap) is audited as `.denied` with the reason. A request with
no accepted token is only logged and counted (`sluis.client_secret.admin_refused`).

## Afterwards

- Record the rotation date with the installation's other rotations
  ([rotate keys and credentials](rotate-keys-and-credentials.md)).
