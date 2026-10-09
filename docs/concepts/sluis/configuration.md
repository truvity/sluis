# Configuration: the model

Why sluis is configured the way it is. The keys, types and defaults are reference: [configuration](../../reference/sluis/configuration.md)
and [chart values](../../reference/sluis/chart-values.md).

## One file, one binary, one chart

sluis is one chart, `charts/sluis`, and one image, for the whole product. It renders the directory, the policy, the OpenID
provider, the login page and the console, and, when `config.controllers` names them, the GitHub and Slack controllers in
the same process ([why one process](one-process.md)). It keeps no audit trail of its own: it records into an installation
of [audit](../audit/README.md) that the deployment provides, and reads that installation's query
service for the console's Audit page.

A process is configured by one file. [ADR 0032](../../decisions/0032-one-configuration-file-one-binary-one-chart.md) replaced
three binaries, three images, environment variables and flat chart values with one binary, one image and one chart, and
one document for how the process runs and one for what the installation decides. Two documents because they change for
different reasons and are owned by different people: the service document says where State lives and which keys sign;
the policy says who may have what. truvity/policy's
[configuration contract](https://github.com/truvity/policy/blob/master/docs/contracts/config.md) is the rule both follow.

## Immutable per instance

A document is read once, at start. A change to configuration or policy is a new set of instances, never a reload
([ADR 0036](../../decisions/0036-configuration-is-immutable-per-instance.md)). On Kubernetes the chart renders a
`checksum/*` annotation per document, so a change rolls the Deployment; on AWS Lambda it is a new configuration layer
version ([AWS Lambda](../../reference/sluis/lambda.md)); elsewhere it is a restart. The reason is that an installation must be able
to show that what runs is what was released and configured: a process that reloads can drift from its files, and two
replicas that reload at different moments disagree. What stays live is credentials and State: a secret is read when it is
used or on a short refresh, so rotating one is not a deployment.

## Validated before anything starts

Each document is held to a JSON Schema authored by hand and embedded in the binary. An unknown key, a missing required
key or a value of the wrong type refuses to start and names the path. The chart's `values.schema.json` embeds the same
schemas, so the same mistake fails `helm install`, and the chart's tests hold what it renders to them. A mistake found at
render costs nothing; the same mistake found by a crash-looping pod costs an incident.

The old environment is refused, not ignored, for the same reason: a deployment that still sets a retired variable believes
it is configuring something, and an unknown setting ignored is the silence
[0007](../../decisions/0007-breaking-changes-inside-1x.md) and 0032 exist to end.

## A secret is named, never written

A document holds a secret's name, and `secrets.source` says how a name is delivered. The platform's own mechanism
delivers (a mounted Secret, SSM, OpenBao), so the document is safe to review, render and commit, and rotating a secret
needs no new document. [Secrets](../../reference/sluis/secrets.md) is the reference.

## What the chart will not do

- **It does not create the signing key.** cert-manager issues one, or `signingKey.existingSecret` names one delivered by
  external-secrets, because a service that mints its own credential is an exception to how every other credential in this
  estate is provisioned, and two replicas with two minted keys hand out tokens half the fleet cannot verify.
- **It puts no authenticating proxy in front.** This is the thing that authenticates; a proxy would have nowhere to send
  anyone. Where a gateway forwards an identity (`login.forwarded`), set `networkPolicy.gatewayNamespace` so nothing else can
  reach the port; the authentication layer is the deployment's.
- **It names no Secret a deployment cannot choose.** Every value that consumes a Secret the chart did not create lets you
  name both the Secret and the keys inside it. The producer is free (external-secrets, a 1Password operator, sealed-secrets,
  a hand `kubectl create secret`, a Job), because a chart that dictated key names could not read a Secret already sitting in
  the namespace. Key names are fixed only for a Secret the service writes itself, where it is the producer.

The service itself reads and writes plain Kubernetes Secrets and ConfigMaps in its own namespace. It has no dependency on
an external-secrets operator, a cloud parameter store or a backup mechanism. Delivering a declared Secret into the
namespace is the deployment's business: [deliver a declared Secret](../../guides/sluis/deliver-a-declared-secret.md).

## Two routes

A gateway policy attaches to an `HTTPRoute`, so the console's path is a separate object: anything put in front of the
console on a shared route would also sit in front of `/token`, `/keys` and discovery, and every relying party in the
estate would be asked to sign in to fetch a key set. The console's route renders whether or not anything attaches to it.
The console is a path and not a host because discovery must be at the root of the origin named in every token's `iss`.

The service strips the console's mount itself, so a gateway that stripped it too would hand the console a path it never
serves.

## A record and its credential are two objects

A record is shown to anyone who may see the console; a credential is written once and read once, at start. Keeping them
apart means the type the console handles cannot carry a secret by accident, and it makes the failure modes independent: a
record whose credential is missing is a workspace with no reader, which the console shows as unhealthy with the reason,
not a service that will not start. Each credential carries a copy of its record, which is what lets
[a restore from the Secrets alone](../../guides/sluis/operate/back-up-and-restore.md) rebuild everything.

## Who reads the directory

There is no directory API listener: the issuer was its only consumer and is now the same process, so the question a
consumer used to ask over the network is a function call. The one machine that reads the directory's answers, the GitHub
controller, asks the console's API (`Explain`, `ListHolders`) with its own ServiceAccount token, verified against its
cluster's published key set; the policy's `service_account` matchers put it in `all:access-roster:viewer`. A service that
needs to know who somebody is does not ask the directory at all: it verifies the issuer's token and reads the `groups`
claim ([service to service](../../guides/sluis/connect/service-to-service.md)). The shape the grant model would take is in
[trust](trust.md#admission-is-not-authorization).

## The roads not taken

- **A reload on change** was rejected for the reason in [immutable per instance](#immutable-per-instance).
- **Configuration through environment variables** was the form until v1.52.4; those variables are refused now ([retired variables](../../reference/sluis/configuration.md#retired-environment-variables)).
- **A chart that mints the signing key** was rejected as above.
