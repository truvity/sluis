# Connect a sluis install

[sluis](https://github.com/truvity/sluis) is the first product that records to an audit installation. This page lists
what the installation must hold. Configure sluis itself with [its documentation](https://github.com/truvity/sluis/tree/master/docs).

## What you need

An installation from [Kubernetes](kubernetes.md) or [AWS Lambda](aws-lambda.md).

| What | Where | How |
|---|---|---|
| sluis's catalogue (source `roster`, a legacy identifier, renamed in v1.75–v1.76) | the writer | Kubernetes: sluis registers it at start-up. Lambda: unpack the release bundle `sluis-audit-catalogue_<version>.tar.gz`, check its checksum, and pass the directory to `Writer.CatalogueDirs` ([change what a source records](../../guides/audit/connect/change-what-a-source-records.md)) |
| The right to write | the writer | Kubernetes: a `workloadIdentity.workloads` entry mapping sluis's ServiceAccount to the source, and its issuer in `workloadIdentity.issuers`. Lambda: sluis's role ARN in `Ingest.Senders` |
| The right to read | the query service | `query.grants` naming the issuer and audience of sluis's console ([read the trail](../../guides/audit/connect/read-the-trail.md)) |
| The profiles sluis's actions fall under | the deployment document | `security`, and `history` if the console shows tenant-facing activity |

## 1. Install audit

Follow a tutorial above, with the entries from the table.

## 2. Connect sluis

Point sluis's audit settings at the writer's address (Kubernetes) or the ingest queue URL (Lambda).

## 3. Check it works

Expect three things: the writer logs the registration (Kubernetes), a record appears under `records/security/`, and
`audit verify` passes ([verify the trail](../../guides/audit/operate/verify-the-trail.md)).

## When it fails

- A record naming a catalogue version the writer lacks is dead-lettered. `event=unknown_catalogue` and the
  `<name>-writer-unknown-catalogue` alarm report it. Give the writer the catalogue first, then roll sluis
  ([change what a source records](../../guides/audit/connect/change-what-a-source-records.md)).
- Messages in the Lambda DLQ: [redrive the ingest DLQ](../../guides/audit/operate/redrive-the-ingest-dlq.md).
- sluis refuses to start when the installation refused its catalogue. The writer's log gives the reason.
