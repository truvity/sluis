# What is recovery?

Recovery is the way in on the day no directory can vouch for anybody. A console asks for a token, the answer needs the
directory, and the directory is connected from that console. Recovery breaks that deadlock.

## What proves you?

The proof is a ServiceAccount token minted for a mandatory audience and checked by the API server with a TokenReview.
It is the only check that still asks the cluster anything. It depends on nothing but the API server.

Nothing is stored. The authority is the cluster's RBAC: who may mint a token for that account. You revoke it by removing
a binding, and the audit log records it.

## What does it grant?

Nothing by itself. A recovered sign-in completes as the ServiceAccount subject. Only a `service_account` matcher in the
policy puts that subject in a group.

## What can the audit trail do to it?

A recovery sign-in is written durably before it completes, and refused when it cannot be written
([audit](audit.md#recording-never-fails-what-is-being-recorded)). To use recovery, follow
[lost operator access](../../guides/sluis/operate/lost-operator-access.md).

## See also

- [Trust](trust.md#recovery-is-the-root-not-a-back-door): where recovery sits among the anchors
