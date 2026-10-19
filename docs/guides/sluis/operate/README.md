# Operations

Recurring and incident tasks for a running installation, on Kubernetes or AWS Lambda. [Day two](day-two.md) answers "I need to" by task; the table below lists every page here.

| When | Page |
|---|---|
| install, and check what you install | [Install with Helm](install-with-helm.md), [deploy from a mirrored release](deploy-from-a-mirrored-release.md), [verify a release](verify-a-release.md) |
| the first day, then every day after | [Day one](day-one.md), [day two](day-two.md) |
| somebody's access must end now | [Revoke sessions](revoke-sessions.md) |
| is it healthy | [Check health](check-health.md), [read the logs](read-the-logs.md), [read the audit trail](read-the-audit-trail.md), [telemetry](install-telemetry.md) |
| keys and credentials | [Rotate keys and credentials](rotate-keys-and-credentials.md), [rotate a client secret](rotate-a-client-secret.md), [sign with AWS KMS](sign-with-aws-kms.md) (deprecated) |
| backups | [Back up and restore the installation](back-up-on-layout-v5.md), [back up the legacy store](back-up-and-restore.md) and [export](export.md) |
| nobody can sign in | [Lost operator access](lost-operator-access.md), [recover on Lambda](recover-on-lambda.md), [turn off recovery](turn-off-recovery.md) |
| scale and availability | [High availability](high-availability.md), [scaling and cache](scaling-and-cache.md), [provide a Valkey](provide-a-valkey.md), [a cold-start herd](survive-a-cold-start-herd.md) |
| storage and identity of the pod | [Blobs on R2](blobs-on-r2.md), [switch the serving pod's role](switch-serving-pod-role.md) |
| upgrading | [v1.77.1](../upgrade/v1.77.1.md) and earlier pages, linked from the [CHANGELOG](../../../../CHANGELOG.md) |
