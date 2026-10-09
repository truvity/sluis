# Switch the serving pod to its own role

On EKS, give the serving pod the role the Pulumi library creates, `<prefix>-sluis`, in place of the shared `acme-shared-audit` role.

## Before you start

- The installation runs the Kubernetes build on EKS with Pod Identity ([Pulumi library](../../../reference/sluis/pulumi-library.md#kubernetes-identity)). The stack is ready: `NewStorage`, `NewState` and `NewKubernetesIdentity` with `ServiceAccount` set to the Deployment's.

- Pick a quiet window. The pod loses its credentials between the old association and the new one.

- A ServiceAccount takes one association. Delete the old one in the same apply that creates the new one, or the `PodIdentityAssociation` create fails.

- The old role carries the audit-events writer's grants, which this library neither carries nor removes. Audit records stop arriving after the switch unless you keep them (step 2).

- No state move is needed. The earlier sluis resources in the eso-iam stack are empty: delete them, do not adopt them.

## Steps

1. Apply the new stack. Read the preview first: it creates the role `<prefix>-sluis`, policy, association, bucket, key and table, and deletes the old association. The same apply deletes the serving ServiceAccount's association with the old role.

2. Keep the audit grants: attach your managed policy for the audit events to the new role with a `RolePolicyAttachment` on `RoleName`, or give the audit writer another path.

3. Delete `acme-shared-audit` and its policy when the last object under its prefix has expired.

## Verify

From the pod, `aws sts get-caller-identity` names `<prefix>-sluis`, and sign-in works. A new audit record arrives after a sign-in. IAM shows the old role last used before the switch. Check again a day later.

## Roll back

Before step 3, re-create the old association and delete the new one in the same apply. The old role and policy are untouched until step 3. After it, re-create them from source. Detach the attachment from step 2 if you undo only that.
