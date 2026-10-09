# Connect an audit installation

## Purpose

Make sluis record into an audit installation and show its Audit page in the console.

## Preconditions

- The audit installation is deployed and has its deployment document, from which its profiles come (see its deploy
  guide, [audit](../../concepts/audit/README.md)).
- You can edit the policy and the chart's values.

## Before you start

- **The audit writer refuses to start without the catalogue's `.json` schemas.** The document `roster.yaml` and every
  schema it references ship together as the release asset `sluis-audit-catalogue_<version>.tar.gz` (in `checksums.txt`).
  On Kubernetes sluis sends the document and schemas itself when it registers; a writer that has no registry (a Lambda)
  must be given the whole directory, not only the YAML, or it fails every cold start and the records go to its dead-letter
  queue. See [change the audit catalogue](change-the-audit-catalogue.md).
- **Upgrade the audit writer before the issuer when the catalogue moves.** Recording is best effort, so an issuer that
  emits fields the writer's catalogue lacks loses those records. Catalogue 1.10.0 (agent class and deadline on
  `roster.person.signed_in`, `spared` on `roster.session.ended`) needs the writer to hold it first: a Lambda writer
  needs the release asset `roster-1.10.0.yaml` and its schemas.
- **The one service account must be mapped to the source `roster`**: the controllers run as the one process and record
  with its token.

## Steps

### 1. Map the workload

**Run** map this release's service account to the source `roster` in the installation's `workloadIdentity.workloads`,
with the audience `audit.token.audience` (default `audit`).
**Expect** the installation to accept the token.
**Verify** no `does not trust this workload's token` line after step 3.
**Rollback**: remove the mapping.

### 2. Declare the Audit client

**Run** declare a client for the page in the policy, its id the value of `audit.audience` (default `audit`), requiring the
groups that may read the trail, and trust this issuer in the query service's grants file with that audience.
**Expect** the policy to validate.
**Verify** a member of those groups sees the page; somebody else is told it is not theirs.
**Rollback**: remove the client.

### 3. Set `audit.writer` and `audit.query`

**Run** set both in the chart's values and roll out.
**Expect** at start the service registers its catalogue on the writer's own address.
**Verify** `audit installation connected` in the log says the address answered on the first try, `audit catalogue
registered` that the installation accepted the catalogue.
**Rollback**: unset them and roll out; records are then validated and logged only.

## Afterwards

- Read a record on the Audit page ([read the audit trail](operate/read-the-audit-trail.md)), and alert on
  `audit.emit.records.dropped`.
