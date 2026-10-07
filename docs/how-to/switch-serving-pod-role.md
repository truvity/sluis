# Switch the serving pod to its own role

## Purpose

Give the serving pod on EKS the role the Pulumi library creates, `<prefix>-sluis`, in place of the shared
`acme-shared-audit` role (decision N9a).

## Preconditions

- The installation runs the Kubernetes build on EKS with Pod Identity ([the Pulumi library](../reference/pulumi-library.md#kubernetes-identity)).
- The library's stack is ready to apply: `NewStorage`, `NewState` and `NewKubernetesIdentity` with `ServiceAccount` set to
  the one the Deployment runs as.
- A quiet window: the pod loses its credentials for the moment between the old association and the new one.
- You can edit gitops's own managed policy for the audit events' writer.

## Before you start

- **A ServiceAccount takes one association.** The old role's association must be deleted in the same apply that creates
  the library's, or the create fails. Looks like: the apply fails creating the `PodIdentityAssociation` for a
  ServiceAccount that already has one.
- **The old role also carries the audit-events writer's grants.** They are the audit side's and are not in this library,
  which neither carries nor removes them. Looks like: audit records stop arriving after the switch.
- **Preview before the apply, and read the preview.** Expect the new role, policy, association, bucket, key and table to
  be created, and the old association to be deleted.
- **No state move is needed.** The earlier sluis resources in the eso-iam stack are empty and are deleted, not adopted.

## Steps

### 1. Create the library's resources

**Run**: apply the new stack from the library: the role `<prefix>-sluis` (v1.62 named it `<prefix>-sluis-serve`), the
bucket, the key and the table are new `sluis` resources. Delete the serving ServiceAccount's association with the old
role in the same apply.

**Expect**: the pod's next credential refresh resolves to `<prefix>-sluis`.

**Verify**: from the pod, `aws sts get-caller-identity` names `<prefix>-sluis`; sign-in works.

**Rollback**: re-create the old association (and delete the new one in the same apply); the old role and its policy are
untouched until step 3.

### 2. Keep the audit events' grants

**Run**: attach gitops's own managed policy for the audit events to the library's role with a `RolePolicyAttachment` on
`RoleName`, or give the audit writer another path.

**Expect**: the role carries the audit writer's grants again.

**Verify**: a new audit record arrives after a sign-in.

**Rollback**: detach the attachment; nothing else depends on it.

### 3. Retire the old role

**Run**: delete `acme-shared-audit` and its policy when the last object under its prefix has expired.

**Expect**: no principal uses the role.

**Verify**: its last-used date in IAM is before the switch.

**Rollback**: none, because the role and its policy are gone; re-create them from gitops's source.

## Afterwards

- Check that audit records and sign-ins still work a day later.
- Tell whoever owns the audit side that its writer no longer shares this pod's role.
