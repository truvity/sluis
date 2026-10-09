# Change what the audit trail records

Add or change an audit action without stopping the service at the next start. You need a checkout of the sluis repository and its devbox.

## Before you start

- An audit installation keeps every catalogue version it was sent and refuses a different document under a version it holds. That stops the service at start. Any change to `internal/audit/catalogue/roster.yaml`, even one new action, needs a new `version`.

- The catalogue is the document plus its `.json` schemas. The writer refuses to start without every schema the document references. A Lambda writer given only the YAML fails each cold start.

- Spell every action once, in `internal/audit/events.go`. A name built at run time is invisible to the check.

## 1. Edit and bump

Change `roster.yaml` and set a new `version`. Save the document as shipped:

```sh
cp internal/audit/catalogue/roster.yaml internal/audit/catalogue/testdata/released/roster-<version>.yaml
( cd internal/audit/catalogue/testdata/released && sha256sum roster-<version>.yaml >> SHA256SUMS )
```

Register a new data schema file beside `roster.yaml` and add it to the action's `data_schema`. Put no address, name or secret in data.

## 2. Run the gate

```sh
just audit-catalogue
go test ./internal/audit/...
```

The recipe validates the document, fails on an action emitted and not declared, and regenerates `frontend/src/auditSentences.ts`. Commit the result, because the recipe fails on a diff. The tests fail on a released version that differs from its fixture, a rewritten fixture, and a declared action with no constructor.

## 3. Roll out

Release as usual. Roll the audit installation's grants and `workloadIdentity` only when you add a source. The log shows `audit catalogue registered`. If `the audit installation refused the catalogue` appears, the process ends and the old pod keeps serving ([check health](operate/check-health.md#2-a-rollout-that-does-not-complete)).

The release ships both parts as `sluis-audit-catalogue_<version>.tar.gz`. Vendor the whole directory.

Update [audit actions](../../reference/sluis/audit-actions.md), which is generated from the catalogue, and mention new actions in the release's upgrade page.

## Roll back

Revert the files, or roll back the release. The installation keeps the new version and still accepts the old one.
