# Day-two operations

Each page is one task. A shell is needed for the export, a restore, a migration, and the log lines of an installation with no audit trail connected.

| I need to | Page |
|---|---|
| sign the first operator in to a standalone installation | [Day one](day-one.md) |
| get back in when nobody is an operator | [Lost operator access](lost-operator-access.md) |
| do the same on Lambda, with the recovery password | [Recover on Lambda](recover-on-lambda.md) |
| understand a console symptom, or a rollout that stalls | [Check health](check-health.md) |
| run the backup module on layout v5, verify a backup, restore one | [Back up and restore the installation](back-up-on-layout-v5.md) |
| back up or restore what the console holds on the legacy store, Slack state included | [Back up and restore the legacy store](back-up-and-restore.md) |
| write everything the service manages to one file | [Export](export.md) |
| rotate a credential, the session key or a Slack token | [Rotate keys and credentials](rotate-keys-and-credentials.md) |
| let the issuer generate a confidential client's secret | [Let the issuer generate a client's secret](../let-the-issuer-generate-a-clients-secret.md) |
| rotate such a secret | [Rotate a client secret](rotate-a-client-secret.md) |
| choose replica counts, size the snapshot cache | [Scaling and cache](scaling-and-cache.md) |
| keep a Lambda serving through a herd of cold starts | [Survive a cold-start herd](survive-a-cold-start-herd.md) |
| run more than one replica | [Run more than one replica](high-availability.md) |
| find something in the service's own log | [Read the logs](read-the-logs.md) |
| end somebody's access now | [Revoke sessions](revoke-sessions.md) |
| close the recovery sign-in | [Turn off recovery](turn-off-recovery.md) |
| send metrics and traces, install alerts | [Install telemetry](install-telemetry.md) |
| check a release before you deploy it | [Verify a release](verify-a-release.md) |
| move the State between storages | [Move the State](../migrate/migrate-state.md) |
| move a whole installation from Kubernetes to AWS | [Cutover](../migrate/cutover.md) |
| let the Slack controller act in a workspace | [Enable a Slack workspace](../enable-slack-workspace.md) |
| let the GitHub controller act in an organisation | [Enable a GitHub organisation](../enable-github-organisation.md) |
| create the Apps self-hosted runners register with | [Runner Apps](../runner-apps.md) |
| see what happened lately | [Read the audit trail](read-the-audit-trail.md) |
| connect an audit installation | [Connect an audit installation](../connect-audit-installation.md) |
| change what the audit trail records | [Change the audit catalogue](../change-the-audit-catalogue.md) |
| see what per-audience group scoping would change | [Read the groups-scoping report](../read-the-groups-scoping-report.md) |
| enforce per-audience group scoping | [Turn enforce on](../turn-enforce-on.md) |

Preview before every apply and read the preview. A Secret or ConfigMap is a projection, not a live source: see [rotate keys and credentials](rotate-keys-and-credentials.md).
