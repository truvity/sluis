# Documentation

Two products in one repository, one version: **sluis** and **audit**, plus the shared **storage** module. The tabs
above are the same on every page and follow what you are doing ([Diataxis](https://diataxis.fr/)): **Get started**,
**Guides**, **Reference** and **Concepts**, each with a section for sluis, audit and storage.

## Start here

- **How sluis is built:** [sluis architecture](concepts/sluis/architecture.md).
- **How audit is built:** [audit architecture](concepts/audit/architecture.md).
- **Choose where sluis runs:** [sluis deployment shapes](get-started/sluis/README.md).
- **Choose where audit runs:** [audit deployment shapes](get-started/audit/README.md).

## How the parts fit

In one line: sluis turns a caller's proof into short-lived credentials and records what it did; audit archives those
records in S3, seals them, and serves them to the console's Audit page.

**sluis.** A caller's proof goes to the issuer; the issuer mints credentials and emits audit records, and the controllers keep GitHub teams and Slack channels in step.

```mermaid
flowchart TB
  dir["Directory and<br/>machine proofs"] --> iss["sluis issuer"]
  iss --> cred["Tokens and credentials<br/>Kubernetes, AWS,<br/>GitHub Apps, Cloudflare"]
  iss --> em["audit emitter"]
  ctl["Controllers"] --> gh["GitHub teams"]
  ctl --> sl["Slack channels"]
```

**audit.** The writer archives the records in S3, the notary seals them, the indexer fills PostgreSQL, and the query service feeds the console's Audit page.

```mermaid
flowchart TB
  em["audit emitter"] --> wr["audit writer"]
  wr --> s3[("S3 archive")]
  nt["notary"] --> s3
  s3 --> ix["indexer"]
  ix --> pg[("PostgreSQL")]
  pg --> q["query"]
  q --> ui["console Audit page"]
```

## The three products

| | What it is | Start at |
|---|---|---|
| **sluis** | the identity and access service: who a caller is, what that gets them, and the way out | [sluis](concepts/sluis/README.md) |
| **audit** | the audit trail: records written once, sealed, searchable, verifiable by an auditor | [audit](concepts/audit/README.md) |
| **storage** | the state and key backends both share, chosen by the `state` and `keys` blocks | [storage](concepts/storage/README.md) |

## Deployment shapes

A shape is where a product runs. Each product's page is the owner of its list; this is the map.

| Product | Shape | State | Page |
|---|---|---|---|
| sluis | AWS Lambda | available | [AWS Lambda](get-started/sluis/deployment/aws-lambda.md) |
| sluis | AWS Lambda behind Cloudflare | available | [AWS behind Cloudflare](get-started/sluis/deployment/aws-behind-cloudflare.md) |
| sluis | Kubernetes with AWS storage | available | [Kubernetes with AWS storage](get-started/sluis/kubernetes-aws.md) |
| sluis | Kubernetes with OpenBao | planned | [Kubernetes with OpenBao](get-started/sluis/deployment/README.md#kubernetes-with-openbao) |
| audit | services in the cluster (stream broker, OpenBao, PostgreSQL) | available | [in the cluster](get-started/audit/in-cluster.md) |
| audit | AWS serverless (Lambda, SQS, DynamoDB, SSM) | available | [AWS Lambda](get-started/audit/aws-lambda.md) |
| audit | writer on Lambda, readers in the cluster | available | [run readers in Kubernetes](guides/audit/operate/aws-run-readers-in-kubernetes.md) |

The two audit shapes are peers; both need a KMS key (or OpenBao transit) and an S3 store (AWS S3 or Cloudflare R2), and
[the audit deployment page](get-started/audit/README.md) says what differs. Installing audit *beside sluis* is a
connection, not a shape: [a sluis-connected install](get-started/audit/sluis.md).

| SDK | Go | TypeScript | Kotlin | Python |
|---|---|---|---|---|
| sluis client, audit emitter and query | built | built | planned | planned |

Kotlin and Python are planned and have no code ([0042](decisions/0042-one-repository-one-release-train.md)).

## What changed

The [CHANGELOG](../CHANGELOG.md) lists every release; the upgrade pages are under each product's how-to
(for audit, [upgrade](guides/audit/upgrade/v0.13.md)).

## Sections

| You are | sluis | audit | storage |
|---|---|---|---|
| learning | [getting started](get-started/sluis/README.md) | [getting started](concepts/audit/README.md#getting-started) | [use the OpenBao backends](guides/storage/use-the-openbao-backends.md) |
| understanding | [architecture](concepts/sluis/architecture.md), [people and agents](concepts/sluis/people-and-agents.md) | [architecture](concepts/audit/architecture.md) | [architecture](concepts/storage/architecture.md) |
| deploying | [deployment shapes](get-started/sluis/deployment/README.md) | [deployment shapes](get-started/audit/README.md) | [where the backends run](get-started/storage/deployment.md) |
| running it | [operations](guides/sluis/operate/README.md) | [how-to: operate](concepts/audit/README.md#how-to) | [deployment](get-started/storage/deployment.md) |
| doing a task | [how-to](guides/sluis/operate/day-two.md) | [how-to](concepts/audit/README.md#how-to) | [how-to](guides/storage/use-the-openbao-backends.md) |
| looking something up | [reference](reference/sluis/configuration.md) | [reference](reference/audit/configuration.md) | [the adapter block](reference/storage/adapter-block.md) |

## Why it is so

[Decisions](decisions/README.md) is one series for all three: sluis 0001 to 0042, audit's records renumbered after them
(a table maps the old numbers), storage's next. A record is never edited to reverse a decision; a new one supersedes it.

## Where a shared topic lives

A topic that crosses products lives with the owner of the contract, and the others link to it.

| Topic | Owner |
|---|---|
| the roster catalogue (what sluis records, its actions and schemas) | sluis: [audit actions](reference/sluis/audit-actions.md), [change the audit catalogue](guides/sluis/change-the-audit-catalogue.md) |
| the record format, extension slots and the SDKs (emitter and query) | audit: [the record](reference/audit/record.md), [extension points](reference/audit/extension-points.md), [emitter library](sdk/go/audit-emitter.md) |
| the adapter block (`keys`, `state`) | storage: [the adapter block](concepts/storage/adapter-block.md) |
| people and agents: client classes and sign-out scopes | sluis: [people and agents](concepts/sluis/people-and-agents.md) |
