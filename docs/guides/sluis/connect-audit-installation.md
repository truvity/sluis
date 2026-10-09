# Connect an audit installation

Make sluis record into an audit installation and show its Audit page in the console. Deploy the audit installation first ([audit](../../concepts/audit/README.md)).

## Before you start

- Give a writer without a registry, such as a Lambda, the whole catalogue directory, not only the YAML. Otherwise every cold start fails and records go to its dead-letter queue ([change the audit catalogue](change-the-audit-catalogue.md)).

- Upgrade the audit writer before the issuer when the catalogue moves, because recording is best effort. Catalogue 1.10.0 needs a Lambda writer to hold `roster-1.10.0.yaml` and its schemas first.

- Map the one service account to the source `roster`. The controllers record with its token.

## 1. Map the workload

In the installation's `workloadIdentity.workloads`, map this release's service account to the source `roster`, with the audience `audit.token.audience` (default `audit`).

## 2. Declare the Audit client

In the policy, declare a client whose id is `audit.audience` (default `audit`). Require the groups that may read the trail. Trust this issuer in the query service's grants file with that audience.

Verify: a member of those groups sees the page. Anyone else is told it is not theirs.

## 3. Set the chart values

Set `audit.writer` and `audit.query` and roll out. The log shows `audit installation connected` once the address answers, and `audit catalogue registered` once the installation accepts the catalogue. No `does not trust this workload's token` line appears.

Then read a record on the Audit page ([read the audit trail](operate/read-the-audit-trail.md)) and alert on `audit.emit.records.dropped`.

## Roll back

Unset `audit.writer` and `audit.query` and roll out. Records are then validated and logged only. Remove the workload mapping and the client if you no longer need them.
