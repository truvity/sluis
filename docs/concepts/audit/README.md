# Audit documentation

Pick the road you are on.

| you are | start at |
|---|---|
| installing audit on Kubernetes | [Getting started on Kubernetes](../../get-started/audit/kubernetes.md) |
| installing audit on AWS Lambda | [Getting started on AWS Lambda](../../get-started/audit/aws-lambda.md) |
| connecting a sluis install | [Getting started with a sluis-connected install](../../get-started/audit/sluis.md) |
| connecting an application | [Connect an application](../../guides/audit/connect/connect-an-application.md), then [emit records](../../guides/audit/connect/emit-records.md) |
| reading or auditing the trail | [Read the trail](../../guides/audit/connect/read-the-trail.md), then [verify the trail](../../guides/audit/operate/verify-the-trail.md) |
| upgrading | [the upgrade pages](../../guides/audit/upgrade/v0.13.md), linked from the [CHANGELOG](../../../audit/CHANGELOG.md) |
| working on this repository | [repository layout](../../reference/audit/repository-layout.md) and [CONTRIBUTING](../../../CONTRIBUTING.md#audit) |

Audit owns the record format, the extension slots and the SDKs. Who may write what to a sluis installation's catalogue is sluis's: [change the audit catalogue](../../guides/sluis/change-the-audit-catalogue.md).

## Getting started

One tutorial per shape: [Kubernetes](../../get-started/audit/kubernetes.md), [AWS Lambda](../../get-started/audit/aws-lambda.md), [sluis-connected](../../get-started/audit/sluis.md). The [deployment shapes](../../get-started/audit/README.md) compare them.

## How-to

| task | page |
|---|---|
| Prepare | [the bucket](../../guides/audit/operate/prepare-the-bucket.md), [the database](../../guides/audit/operate/prepare-the-database.md) |
| Connect | [an application](../../guides/audit/connect/connect-an-application.md), [emit records](../../guides/audit/connect/emit-records.md), [read the trail](../../guides/audit/connect/read-the-trail.md), [change what a source records](../../guides/audit/connect/change-what-a-source-records.md) |
| Run | [stream mode](../../guides/audit/operate/run-stream-mode.md), [billing](../../guides/audit/operate/enable-billing.md), [usage quotas](../../guides/audit/operate/enable-usage-quotas.md), [OpenBAO keys](../../guides/audit/operate/configure-openbao-keys.md) |
| AWS Lambda | [ship a release](../../guides/audit/operate/aws-ship-a-release.md), [store secrets in SSM](../../guides/audit/operate/aws-store-secrets-in-ssm.md), [turn the Object Lock on](../../guides/audit/operate/aws-turn-on-object-lock.md), [switch the archive's encryption](../../guides/audit/operate/aws-switch-archive-encryption.md), [run readers in Kubernetes](../../guides/audit/operate/aws-run-readers-in-kubernetes.md), [send telemetry](../../guides/audit/operate/aws-send-lambda-telemetry.md), [redrive the ingest DLQ](../../guides/audit/operate/redrive-the-ingest-dlq.md), [archive on R2](../../guides/audit/operate/archive-on-r2.md) |
| Operate | [recover from an outage](../../guides/audit/operate/recover-from-an-outage.md), [respond to alerts](../../guides/audit/operate/respond-to-alerts.md), [replay dead-lettered records](../../guides/audit/operate/replay-dead-lettered-records.md), [rebuild the index](../../guides/audit/operate/rebuild-the-index.md), [diagnose a scheduled job](../../guides/audit/operate/diagnose-a-scheduled-job.md), [read from the replica](../../guides/audit/operate/read-from-the-replica.md) |
| Verify and hold | [verify the trail](../../guides/audit/operate/verify-the-trail.md), [investigate a failed verification](../../guides/audit/operate/investigate-a-failed-verification.md), [place a legal hold](../../guides/audit/operate/place-a-legal-hold.md) |
| Keys | [erase a tenant's keys](../../guides/audit/operate/erase-a-tenants-keys.md), [fix the key directory](../../guides/audit/operate/fix-the-key-directory.md) |
| Upgrade | [v1.75](../../guides/audit/upgrade/v1.75.md), [v1.74](../../guides/audit/upgrade/v1.74.md), [v0.13](../../guides/audit/upgrade/v0.13.md), [v0.6](../../guides/audit/upgrade/v0.6.md) |
| Contribute | [test the kind tier](../../guides/audit/operate/test-the-kind-tier.md) |

## Reference

| area | pages |
|---|---|
| Configuration | [the file and shared blocks](../../reference/audit/configuration.md), [the writer](../../reference/audit/configuration-writer.md), [observe and query](../../reference/audit/configuration-observe-query.md), [the jobs](../../reference/audit/configuration-jobs.md), [chart values](../../reference/audit/chart-values.md), [the emitter library](../../sdk/go/audit-emitter.md) |
| Contracts | [record](../../reference/audit/record.md), [catalogue](../../reference/audit/catalogue.md), [extension points](../../reference/audit/extension-points.md), [API](../../reference/audit/api.md), [bucket contract](../../reference/audit/bucket-contract.md) |
| Profiles and verification | [profiles](../../reference/audit/profiles.md), [`audit verify`](../../reference/audit/verify-command.md), [archive prefixes and IAM](../../reference/audit/archive-prefixes-and-iam.md) |
| Platforms | [AWS Pulumi library](../../reference/audit/aws-pulumi-library.md), [capabilities](../../reference/audit/capabilities.md), [telemetry](../../reference/audit/telemetry.md), [repository layout](../../reference/audit/repository-layout.md) |

## Explanation

| area | pages |
|---|---|
| Design | [architecture](architecture.md), [why](why.md), [concepts](concepts.md), [levels](levels.md), [roadmap](roadmap.md) |
| Shapes | [deployment shapes](deployment-shapes.md), [direct mode](direct-mode.md), [stream mode](stream-mode.md), [AWS Lambda](aws-lambda.md) |
| Mechanics | [split writer](split-writer.md), [search](search.md), [authentication and authorization](authn-authz.md), [integrity](integrity.md), [key custody](key-custody.md) |
| Profiles and metering | [which profiles to compose](which-profiles-to-compose.md), [billing and metering](billing.md), [usage quotas](usage-quotas.md), [the Audit page](audit-page.md) |

## Decided in

[The decisions index](../../decisions/README.md) and the [target architecture](../../decisions/0058-three-parts-installed-independently.md).
