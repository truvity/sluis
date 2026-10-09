# Recovery

Recovery is the way in on the day no directory can vouch for anybody. It exists because of a deadlock that is
otherwise complete: a console asks for a token, the answer needs the directory, the directory is not connected yet,
and it is connected *from* that console.

The proof is a ServiceAccount token minted for a mandatory audience and checked by the API server with a
**TokenReview**: the one thing left that asks the cluster anything, and deliberately so, because on the day
everything else is broken it should depend on nothing but the API server. Nothing is stored. The authority is the
cluster's RBAC: who may mint a token for that account, revocable by removing a binding and landed in the audit log.

It grants nothing by itself. A recovered sign-in completes as the ServiceAccount *subject*, and only a
`service_account` matcher in the policy puts that subject in a group.

It is also the one thing the audit trail can refuse: a recovery sign-in is written durably before it completes, and
refused when it cannot be ([audit](audit.md#recording-never-fails-what-is-being-recorded)). The procedure for using
it is [lost operator access](../../guides/sluis/operate/lost-operator-access.md).
