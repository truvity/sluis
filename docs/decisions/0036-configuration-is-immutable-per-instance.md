# 0036 — Configuration and policy are immutable per instance; credentials and State are read live

**Status:** Accepted; amended by [0037](0037-one-process-everywhere.md); refined by [0038](0038-estates-render-through-sluis.md); follows up [0007](0007-breaking-changes-inside-1x.md) (the deferred deprecation window), refines [0032](0032-one-configuration-file-one-binary-one-chart.md), amends [0028](0028-nothing-writes-configmaps-or-secrets.md) and [0031](0031-a-generic-migration-tool.md) only where they name SSM paths or configuration documents
**Date:** 2026-10-05

## Context

0032 made configuration one file per command. It did not say how a file reaches
a running instance, when an instance notices a change, or where a secret lives
once there is more than one platform. Three pressures arrived together:

- **Lambda.** The function's code is the released artefact, and an installation
  must be able to show that what runs is the release. Baking the installation's
  configuration into the zip makes the deployed code something nobody released.
- **Two installations in one account.** Two estates (one on Kubernetes, one on AWS Lambda) both run
  sluis; their SSM parameters must not collide.
- **Policy-like keys in the service documents.** Exchange clusters and AWS
  accounts, GitHub owners and runner tiers, catalogues, exports and the
  enabled orgs and workspaces are policy, but sit in the three service files, so
  one change touches several documents that nothing keeps consistent.

The truvity/policy contract (service.md section 1: configuration is a file, its
path from one argument or one environment variable; config.md section 2:
schemas authored, tests binding the structs) already answers part of this.

## Decision

1. **Configuration and policy are immutable for the life of an instance. A
   change is a new set of instances, never a reload.**
   - Kubernetes: the chart renders `checksum/*` annotations from the config and
     policy, so a change rolls the Deployment. A file a process reads once at
     start may be mounted with `subPath`: the `checksum/*` annotation replaces
     the pod on any change, so an in-place update would never be read. Mount a
     directory only where a file is meant to change in place (secret files read
     per call, signing keys).
   - AWS Lambda: the configuration and policy are delivered as an **immutable
     Lambda layer** mounted at `/opt/<app>/`. The function's code is the
     **released zip, byte-identical to the release and sha256-verified** before
     it is applied. A change publishes a new layer version and updates the
     function; AWS replaces every instance at once. No aliases and no canary:
     the function stays on `$LATEST` and the change is all at once (the owner's
     choice).
   - Server: a restart.
2. **Credentials and State are read live.** Signing-key files, client secrets,
   projected tokens and the State (DynamoDB or the configured store) are read
   when used or on a short refresh, not frozen at start. Rotating a credential
   is not a deployment.
3. **Configuration is a file everywhere.** Its path comes from an argument or
   one variable (`SLUIS_CONFIG`), per the contract. Documents: the three service
   documents (`serve`, `controller-github`, `controller-slack`) plus **one
   canonical `policy.yaml`**. The policy-like keys move out of the service
   documents into it: exchange clusters and AWS accounts, GitHub owners and
   runner tiers, catalogues, exports, `enabledOrgs` and `enabledWorkspaces`.
   Layering exists only at render time, in `sluisctl policy render`; a running
   binary reads one finished document and does not merge.
4. **Versioning.** Every document carries `apiVersion`; absent means `v1`. A
   binary accepts versions N and N-1 and converts N-1 on load. This is the
   deprecation window 0007 deferred, now taken for configuration only; the
   removal-is-refused rule of 0007 still applies to anything outside the window.
   The schemas stay authored by hand, with tests binding them to the Go structs.
5. **Secrets.** A declared secret is delivered by an environment variable, a
   mounted file, or a declared secret source (an amendment to contract section
   5). On Lambda sluis resolves `/sluis/<instance>/private/config/...` by
   prefix and re-reads it every five minutes. SSM layout v3, with `<instance>`
   the installation's name (for example `acme` and `prod`), so two
   installations can share an account:

   ```
   /sluis/<instance>/private/config/clients/<id>/secret
   /sluis/<instance>/private/config/providers/google/<id>/client-id
   /sluis/<instance>/private/config/providers/google/<id>/client-secret
   /sluis/<instance>/private/config/issuer/state-secret
   /sluis/<instance>/private/config/recovery/password
   /sluis/<instance>/private/config/directory/<id>/key
   /sluis/<instance>/private/config/valkey/password
   /sluis/<instance>/private/credentials/<kind>/<id>/<ref>
   /sluis/<instance>/export/<path>
   ```

## Consequences

- Restarts are cheap because State is external. To keep replacement from
  refusing sign-ins, **the resolver's hold-window memory moves into State**:
  a new instance that cannot reach the directory still honours the hold the
  last one recorded.
- On Lambda a policy change remains a **manual Pulumi apply** (owner decision
  O1c); nothing deploys it on merge.
- A layer is the unit of rollback: re-point the function at the previous layer
  version.
- The version window needs a converter per supported N-1; a schema change that
  cannot be converted is a major step, not a minor one.
- SSM paths named in 0028 and 0031 are superseded by the layout above.

## Alternatives considered

**Config baked into the zip.** Rejected: it breaks the separation of build,
release and run. The deployed code is no longer the release, and a hash
cannot say otherwise.

**S3 documents pinned by `versionId`.** Rejected: a fetch on every cold start,
extra IAM, and a new way to fail at init. For the audit service that failure
sends the SQS ingest to the DLQ.

**AWS AppConfig.** Not rejected on merit: staged rollout with alarm rollback is
real value. It needs an extension layer and about six resources per estate, and
it is AWS-only. Deferred; it can be added later on top of this decision.

**Environment-only configuration (caarlos0/env).** Rejected: Lambda's 4 KB
environment cap, lists flattened into `_0_` variables, and it contradicts
contract section 1.

**Policy in SSM.** Rejected: 4 KB (standard) or 8 KB (advanced) per parameter,
and a tree of parameters is not read atomically.

**Lambda aliases with CodeDeploy canary.** Deferred; the owner chose all at
once. Nothing here prevents adding it.

**Runtime policy reload.** Rejected: an eventual-consistency window in which
instances disagree about policy, and more code to get right. Replacing
instances is cheap here, so the reload buys little.
