# One process, and why

The directory reader and the token service were two deployments until 0.12. The split was built so that several
things could ask the directory of record: a controller, a hook, another cluster. The issuer became its only
consumer, and from then on the split was paid for on **every single login**: a ConnectRPC call, a TokenReview, a
NetworkPolicy hop, a second store, and a class of failure where the two halves disagreed about the same person.

So the answer about a person is a function call.

Since v1.63 there is nothing left beside it. The GitHub and Slack controllers are loops inside the same process,
switched on by the `controllers` section of the one service document, and they answer to the same health probes
([ADR 0037](../../decisions/0037-one-process-everywhere.md)). What that leaves is one chart, one Deployment (or one
Lambda function), one service document, one policy file, one health endpoint, and a console served on the issuer's
own origin. The assembly is `internal/rosterapp`; it is wiring and nothing else, and the halves keep their own
packages and tests.

What the single process gives up is the isolation the old split bought by accident. The controllers hold GitHub App
keys and write to GitHub, which is why the boundary now sits in IAM scoped to the installation, in configuration
validated before it ships, and in the release signature, not between processes
([ADR 0036](../../decisions/0036-configuration-is-immutable-per-instance.md)).

The directory's own endpoint stays unserved. The candidates for it, the GitHub and Slack controllers, read the
console's API instead, which is this very process, with the policy digest on every answer (see
[the GitHub controller](github-controller.md)). Nobody outside the process reads the directory.

Read next: [the directory model](directory-model.md), [how it is kept fresh](freshness.md), and
[what happens when it breaks](failure-semantics.md).
