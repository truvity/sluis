# How is sluis configured?

The keys, types and defaults are in the [configuration reference](../../reference/sluis/configuration.md) and the [chart values](../../reference/sluis/chart-values.md).

## One file, one binary, one chart

sluis is one chart, `charts/sluis`, and one image. It renders the directory, the policy, the OpenID provider, the login page and the console. When `config.controllers` names them, it also runs the GitHub and Slack controllers in the same process ([one process](design.md#one-process)).

Two documents configure a process. The service document says where State lives and which keys sign. The policy says who may have what. They change for different reasons and have different owners.

## Documents are immutable per instance

The process reads each document once, at start. A change to configuration or policy is a new set of instances, never a reload.

- On Kubernetes the chart renders a `checksum/*` annotation per document, so a change rolls the Deployment.

- On AWS Lambda a change is a new configuration layer version ([AWS Lambda](../../reference/sluis/lambda.md)).

- Elsewhere a change is a restart.

Credentials and State stay live. The process reads a secret when it uses it or on a short refresh, so rotating one is not a deployment.

## Documents are validated before anything starts

A JSON Schema written by hand and embedded in the binary holds each document. An unknown key, a missing required key or a value of the wrong type refuses to start and names the path.

The chart's `values.schema.json` embeds the same schemas, so the same mistake fails `helm install`. The chart's tests hold what it renders to them. The service refuses the retired environment variables instead of ignoring them ([retired variables](../../reference/sluis/configuration.md#retired-environment-variables)).

## A secret is named, never written

A document holds a secret's name, and `secrets.source` says how the platform delivers it: a mounted Secret, SSM or OpenBao. The document stays safe to review, render and commit. Rotating a secret needs no new document. See [secrets](../../reference/sluis/secrets.md).

## What the chart does not do

- **It does not create the signing key.** cert-manager issues one, or `signingKey.existingSecret` names one that external-secrets delivers.

- **It puts no authenticating proxy in front.** Where a gateway forwards an identity (`login.forwarded`), set `networkPolicy.gatewayNamespace` so nothing else reaches the port.

- **It dictates no Secret name.** Every value that consumes a Secret the chart did not create takes the Secret's name and the keys inside it. Key names are fixed only for a Secret the service writes itself.

The service reads and writes plain Kubernetes Secrets and ConfigMaps in its own namespace. To deliver a declared Secret into the namespace, see [deliver a declared Secret](../../guides/sluis/deliver-a-declared-secret.md).

## Two routes

The console has its own `HTTPRoute`, separate from the issuer's. A gateway policy attaches to a route, and a policy on a shared route would also cover `/token`, `/keys` and discovery. The console's route renders whether or not anything attaches to it.

The console is a path, not a host, because discovery must sit at the root of the origin in every token's `iss`. The service strips the console's mount itself. A gateway that strips it too hands the console a path it never serves.

## A record and its credential are two objects

The console shows a record to anyone who may see it. A credential is written once and read once, at start. A record whose credential is missing shows as unhealthy with the reason, and the service still starts.

Each credential carries a copy of its record, so [a restore from the Secrets alone](../../guides/sluis/operate/back-up-and-restore.md) rebuilds everything.

## Who reads the directory

The service has no directory API listener. The issuer reads the directory by function call.

The GitHub controller asks the console's API (`Explain`, `ListHolders`) with its own ServiceAccount token, verified against its cluster's published key set. The policy's `service_account` matchers put it in `all:access-roster:viewer`, a legacy identifier, renamed in v1.75–v1.76, that remains the role name.

A service that needs to know who somebody is verifies the issuer's token and reads the `groups` claim ([service to service](../../guides/sluis/connect/service-to-service.md)).

## Decided in

- [ADR 0032: One configuration file, one binary, one chart](../../decisions/0032-one-configuration-file-one-binary-one-chart.md)
- [ADR 0036: Configuration is immutable per instance](../../decisions/0036-configuration-is-immutable-per-instance.md)
- [ADR 0007: Breaking changes inside 1.x](../../decisions/0007-breaking-changes-inside-1x.md)
