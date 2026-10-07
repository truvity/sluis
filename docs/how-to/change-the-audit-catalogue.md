# Change what the audit trail records

## Purpose

Add or change an audit action without stopping the service at the next start.

## Preconditions

- A checkout of the sluis repository and its devbox (`just`).

## Before you start

- **An audit installation keeps every catalogue version it was sent and refuses a different document under a version it
  already holds.** That stops the service (and the controllers) at start. v1.41.0 and v1.42.0 did exactly this; v1.42.1
  fixed it. Any change to `internal/audit/catalogue/roster.yaml`, even one new action, is a new `version`.
- **The catalogue is the document plus its `.json` schemas.** The audit writer refuses to start without every schema the
  document references (and with one nothing references). A Lambda writer that was given only `roster.yaml` fails each cold
  start. The release ships both as `sluis-audit-catalogue_<version>.tar.gz`, held to its source by a test; vendor the
  whole directory.
- **A name built at run time is invisible to the check.** Every action is spelled once, in `internal/audit/events.go`.

## Steps

### 1. Edit and bump

**Run** change `internal/audit/catalogue/roster.yaml` and set a new `version` (the current one is the `version` already in that file; bump it from there). Save the
released document as `internal/audit/catalogue/testdata/released/roster-<version>.yaml`, a copy of the file as shipped.
**Expect** the diff to hold the document, its schemas, and the fixture.
**Verify** next step.
**Rollback**: revert the files.

### 2. Run the gate

**Run** `just audit-catalogue`.
**Expect** it validates the document, fails on an action emitted and not declared (or the reverse), and regenerates
`frontend/src/auditSentences.ts`; the recipe fails on a diff, so commit the result.
**Verify** `go test ./internal/audit/...`: `TestAReleasedCatalogueVersionIsNeverChanged` fails if a catalogue carrying a
released version differs from its fixture.
**Rollback**: revert.

### 3. Roll out

**Run** release as usual. An installation connected to this release accepts the new version at start; roll the audit
installation's grants and `workloadIdentity` only if a new source was added.
**Expect** `audit catalogue registered` in the log.
**Verify** `the audit installation refused the catalogue` does not appear; if it does, the process ends and the rollout
stalls with the old pod serving ([check health](check-health.md#2-a-rollout-that-does-not-complete)).
**Rollback**: roll back the release; the installation keeps the new version and the old one is still accepted.

## Afterwards

- Update [audit actions](../reference/audit-actions.md) (generated from the catalogue) and mention the new actions in the
  release's upgrade page.
