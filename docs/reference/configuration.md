# Configuration

An installation is configured by two documents, each one YAML file: the **service document** (`sluis.yaml`,
`apiVersion: sluis.truvity.github.io/sluis/v3`), which says how the one process runs, and the **policy document**, which
says what the installation decides. This page is the reference for the service document and the way a binary loads its
documents. Why it is shaped this way (one file, one binary, one chart; immutable per instance): [Configuration: the
model](../explanation/configuration.md).

| For | See |
|---|---|
| the service document's keys, the retired variables and keys | this page |
| every value of the chart | [chart values](chart-values.md) |
| the policy document | [the policy document](policy-document.md) (the access model's tables: [policy](policy.md)) |
| what an estate writes once to render both | [the installation document](installation-document.md) |
| secret names, the `env`/`file`/`ssm` sources, the SSM layout | [secrets](secrets.md) |
| keeping secrets in OpenBao | [the OpenBao Secrets adapter](openbao-secrets-adapter.md) |
| the secrets copied to OpenBao | [exports](exports.md) |
| `directory.workspaces` | [declared workspaces](declared-workspaces.md) |
| the HTTP endpoints | [endpoints](endpoints.md) |
| the console's roles | [console roles](console-roles.md) |
| the objects the service writes in its namespace | [Kubernetes objects](kubernetes-objects.md) |
| the adapters and presets | [adapters](adapters.md) |

To change from the old forms: [migrate from environment variables](../how-to/migrate-from-environment-variables.md),
[migrate from the access-issuer chart](../how-to/migrate-from-the-access-issuer-chart.md).

## Loading

- `sluis serve` takes its service document with `--config <file>` or, with no `--config`, from the variable
  `SLUIS_CONFIG`; the service document names the policy document with `policy.file`. Those two are all that configures the
  process: `--version` and `--help` are the only other flags.
- `sluis tick github|slack <target>` runs one target's tick once and reads the same file (the controller's section of it);
  the target comes first: an organisation's login or `github:links` for GitHub, a workspace's key for Slack. It refuses to
  run until a shared State exists, because a running controller's lease would not exclude it; with the service scaled to 0,
  `--unsafe-local-lease` runs it.
- `sluis controller github` and `sluis controller slack` are deprecated: they still run a controller as a process of its
  own (and say so in the log), reading their own v2 documents or the one v3 document, whose `controllers.<kind>` section
  they then take. They are removed in a later release.
- **Validated before anything starts.** Each document is held to its JSON Schema (`schemas/config/<document>.schema.json`:
  `sluis` or `policy`, embedded in the binary; the v2 `serve`, `controller-github` and `controller-slack` schemas stay for
  the documents this release still loads). An unknown key, a missing required key or a value of the wrong type refuses to
  start and names the path to it. The chart's `values.schema.json` embeds the same schemas under `config` and `policy`.
- **A secret is named, never written** ([secrets](secrets.md)).
- **Telemetry is not here.** It is OpenTelemetry's own `OTEL_*` environment, set on the pod by the platform (the chart's
  `telemetry.otlp` values). Nothing in a document restates it.
- **The old environment is refused, not ignored.** A retired variable that is still set stops the process at start,
  naming the key that replaces it ([the table below](#retired-environment-variables)).
- **Durations** are Go duration strings (`30s`, `15m`, `168h`).
- The schemas are the exhaustive reference, with every key's description and default. The tables below give each key, its
  default when unset and what to know. A key with no default is unset by default, which is the binary's own behaviour.

## Documents and `apiVersion`

Every document carries an `apiVersion` of the form `sluis.truvity.github.io/<kind>/<version>`: the service document is
`sluis/v3` and the policy `policy/v2`. **Absent means v1.** A binary reads its documents' version N and N-1, and converts
N-1 as it loads it: this build reads the service document at v3, and the v2 `serve` document (and v1, with no
`apiVersion`) as a v3 document with no controllers. (The v2 `controller-github` and `controller-slack` documents are read
only by the deprecated `sluis controller` subcommands, and `sluis tick` and `sluis migrate` read either.) A deployment
rolls the binary first and its configuration second. A v1 document is held to the schema it was written against
(`schemas/config/v1/`, frozen as v1.61 wrote it): what v1 kept in a service document and v2 keeps in the policy document
is read from the files v1 named, and each secret is read where v1 named it. A v2 document that names a key v2 retired is
refused with where it went ([retired keys](#retired-keys)). A schema change that cannot be converted is a major step.

```yaml
apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://sluis.example
policy: {file: /etc/sluis/policy/policy.yaml}
secrets: {source: file, root: /var/run/sluis/secrets}
controllers:                       # optional: absent is no controller
  github: {consoleURL: "http://sluis.access.svc:8080/console", interval: 15m}
  slack: {consoleURL: "http://sluis.access.svc:8080/console"}
```

Layering exists in exactly one place, rendering the policy ([the policy document](policy-document.md#rendering)): a running
binary reads one finished document and merges nothing.

## The service document

What `sluis serve` reads, and the chart's `config`: the issuer, the console and the directory hub in one process, with the
GitHub and Slack controllers beside them under [`controllers`](#controllers-the-github-and-slack-controllers).

<!-- generated: config-keys -->

Source: `schemas/config/sluis.schema.json`. Generated by `just docs-generate`; keys are listed with their parents first.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `adapters` | object | — | Names the adapter of single concerns, over the preset and the `ports` keys. The names and what each needs are in the matrix of docs/reference/adapters.md. |
| `adapters.audit` | object | — | The adapter for the audit sink, replacing the preset's. |
| `adapters.audit.adapter` | string | **required** | The adapter's name, as the compatibility matrix lists it. |
| `adapters.audit.settings` | object | — | The adapter's own settings (an object; the adapter refuses a key it does not know). |
| `adapters.blobs` | object | — | The adapter for blobs, replacing the preset's. |
| `adapters.blobs.adapter` | string | **required** | The adapter's name, as the compatibility matrix lists it. |
| `adapters.blobs.settings` | object | — | The adapter's own settings (an object; the adapter refuses a key it does not know). |
| `adapters.schedule` | object | — | The adapter for the schedule of passes, replacing the preset's. |
| `adapters.schedule.adapter` | string | **required** | The adapter's name, as the compatibility matrix lists it. |
| `adapters.schedule.settings` | object | — | The adapter's own settings (an object; the adapter refuses a key it does not know). |
| `adapters.secrets` | object | — | The adapter for secrets (dynamic secrets, exports under `export/`), replacing the preset's. |
| `adapters.secrets.adapter` | string | **required** | The adapter's name, as the compatibility matrix lists it. |
| `adapters.secrets.settings` | object | — | The adapter's own settings (an object; the adapter refuses a key it does not know). |
| `adapters.signing` | object | — | The adapter for token signing, replacing the preset's. |
| `adapters.signing.adapter` | string | **required** | The adapter's name, as the compatibility matrix lists it. |
| `adapters.signing.settings` | object | — | The adapter's own settings (an object; the adapter refuses a key it does not know). |
| `adapters.state` | object | — | The adapter for state, sessions included, replacing the preset's. |
| `adapters.state.adapter` | string | **required** | The adapter's name, as the compatibility matrix lists it. |
| `adapters.state.settings` | object | — | The adapter's own settings (an object; the adapter refuses a key it does not know). |
| `adapters.trigger` | object | — | The adapter for the "run a pass now" trigger, replacing the preset's. |
| `adapters.trigger.adapter` | string | **required** | The adapter's name, as the compatibility matrix lists it. |
| `adapters.trigger.settings` | object | — | The adapter's own settings (an object; the adapter refuses a key it does not know). |
| `allowInsecure` | boolean | — | Accept a plain-http issuer URL, for a local run. |
| `apiVersion` | any | **required** | Which version of which document this is. sluis.truvity.github.io/sluis/v3 is the one service document: the process that serves the issuer and the console and, under `controllers`, runs the GitHub and Slack controllers. A binary still reads the v2 `serve` document (and v1, which has no apiVersion) as this document with no controllers (docs/reference/configuration.md). |
| `audit` | object | — | The audit installation this service records to. Unset keeps the trail in the log only. |
| `audit.audience` | string | `"audit"` | The client whose audience the Audit page's tokens carry. |
| `audit.forwardedForTrustedHops` | integer | `0` | How many of the deployment's own proxies append to X-Forwarded-For; zero records the peer. |
| `audit.queryURL` | string | — | The query service, for the console's Audit page. Its own setting: it needs no `writer`, so the page works with the `sqs` and `log` sinks. A path prefix (`https://audit.example/sluis`) is kept and the procedure path appended. |
| `audit.tokenFile` | string | — | This workload's projected service-account token, presented on every call. |
| `audit.writer` | string | — | The installation's receiver. |
| `cluster` | string | — | Names this cluster in a ServiceAccount's subject. A pod cannot discover it; unset keeps the older unqualified subject. |
| `console` | object | — | Where the console is published, for the sign-in that starts there. |
| `console.awsAudience` | string | — | The audience an AWS role's web identity token must be minted for to be a bearer at the console (a Lambda controller). Its own, distinct from the AWS federation file's, so a token for token exchange is no proof here and the reverse. Default `<issuerURL>/console`. |
| `console.client` | string | — | The client id the console signs in as. |
| `console.origin` | string | — | The console's origin, when it is not the issuer's. |
| `demo` | boolean | — | Two tenants held in memory, which need no credential and no network. |
| `directory` | object | — | The corporate directories this deployment declares: each adopted at start (the console connects the others). Unset declares none. |
| `directory.workspaces` | array | — | The declared workspaces. One that cannot be adopted stops the service. |
| `directory.workspaces[].admin` | string | **required** | The account the credential impersonates. |
| `directory.workspaces[].backend` | one of google | **required** | The implementation that reads it. |
| `directory.workspaces[].id` | string | — | The backend's tenant id. Optional: given, the adoption checks it. |
| `directory.workspaces[].keySecret` | string | **required** | The secret the service-account key is: `directory/<id>/key`. |
| `directory.workspaces[].serve` | array | — | Narrows the tenant to these domains. Empty serves every domain discovered. |
| `directory.workspaces[].syncGroups` | array | — | Narrows the tenant to these groups. Empty keeps every group in the served domains. |
| `exchange` | object | — | How the token exchange verifies workloads. Whom it trusts (the clusters, the AWS accounts, the GitHub owners) is the policy document's `exchange`. |
| `exchange.audience` | string | — | The audience a workload token must be minted for. Defaults to `release`, so two issuers in one cluster cannot accept each other's proofs. |
| `freshness` | object | — | How the directory's snapshot is kept current. |
| `freshness.freshnessWindow` | string | `"30m"` | How old a snapshot may be and still be answered from. |
| `freshness.probeInterval` | string | `"5m"` | How often the directory is probed. |
| `freshness.refreshInterval` | string | `"15m"` | How often a snapshot is refreshed. |
| `groupsScoping` | one of off, report, enforce | `"report"` | How far this installation has moved toward per-audience `groups` scoping: `off`, `report` or `enforce`. |
| `inCluster` | boolean | — | Prove recovery against the cluster the pod runs in. Set by a deployment that turns recovery on. |
| `issuerURL` | string | **required** | The issuer: baked into every token and every relying party's trust, so there is no default. No trailing slash is kept. |
| `lifetimes` | object | — | How long what the issuer hands out lives. |
| `lifetimes.absolute` | string | `"24h"` | A session, at most. Positive, and at least `token`: a session has to end SOMETIME after sign-in, and an access token cannot outlive the session that grants it. |
| `lifetimes.agent` | object | — | The refresh chains of clients the policy marks `session: agent`, such as MCP hosts (docs/decisions/0040). Their chains are held to these instead of `refresh` and `absolute`. |
| `lifetimes.agent.absolute` | string | `"720h"` | An agent chain, at most, from `auth_time`. Positive, and at most 2160h (90 days). |
| `lifetimes.agent.access` | string | `"30m"` | The longest an agent client's access or ID token lives. Positive, and at most 1h. |
| `lifetimes.agent.refresh` | string | `"336h"` | An agent chain's idle limit. Positive, and at most `absolute`. |
| `lifetimes.hold` | string | `"4h"` | How long a removal is held before it takes effect. |
| `lifetimes.refresh` | string | `"12h"` | A refresh token. |
| `lifetimes.session` | string | `"12h"` | The console's own session cookie, capped at `absolute`. |
| `lifetimes.token` | string | `"1h"` | An access token. |
| `listen` | fragment: listen.json | `{"address":":8080"}` |  |
| `log` | fragment: log.json | — |  |
| `login` | object | — | How a person signs in to the console. |
| `login.directory` | boolean | `true` | Sign in with the corporate directory. |
| `login.forwarded` | object | — | A sign-in an authenticating proxy has already done. |
| `login.forwarded.audience` | string | — | The audience its token carries. |
| `login.forwarded.emailHeader` | string | — | The header that carries the signed-in address. |
| `login.forwarded.issuer` | string | — | The proxy's issuer. |
| `login.signOutURL` | string | — | Where sign-out sends the browser. |
| `oauthClient` | object | — | The OAuth client registered once with the directory backend: it drives both admin consent and operator sign-in. |
| `oauthClient.id` | string | — | The client id, for a local run. Not a secret. |
| `oauthClient.idKey` | string | — | The key of the id in that Secret. |
| `oauthClient.provider` | string | — | Names the client's secrets: providers/google/<provider>/client-secret, and providers/google/<provider>/client-id unless `id` gives it. One segment of a secret's name. |
| `oauthClient.secretKey` | string | — | The key of the secret in that Secret. |
| `oauthClient.secretName` | string | — | The Kubernetes Secret the client is declared in, which the console shows and cannot change. |
| `platform` | object | — | What this installation has to build on. The answers pick a preset (the decision tree in docs/explanation/ports.md), and start refuses an adapter that needs an answer that is no. Absent, the `ports` keys decide and nothing is checked against the platform. |
| `platform.aws` | boolean | — | AWS is available: its credentials, DynamoDB, S3, SSM, KMS, SQS and EventBridge. |
| `platform.kubernetes` | boolean | — | A Kubernetes cluster is available. |
| `platform.openbao` | boolean | — | An OpenBao is available. |
| `platform.replicas` | integer | `1` | How many replicas share this installation's state. Above 1 start refuses an adapter that keeps its data in the process (`memory`). |
| `platform.runtime` | one of kubernetes, lambda, process | — | Where sluis itself runs. Absent: `kubernetes` with a cluster, else `lambda` on AWS, else `process`. |
| `policy` | object | — | The policy document this service decides by. Unset is the built-in two groups, or the demonstration policy under `demo`. |
| `policy.file` | string | **required** | The policy document: the one canonical file `sluisctl policy render` writes (schemas/config/policy.schema.json). Read once, at start: a change is a new instance. |
| `ports` | object | — | The adapters behind the storage ports (docs/explanation/ports.md). |
| `ports.adapter` | one of legacy, memory, dynamodb | `"legacy"` | `legacy` keeps state where it has always been kept: the namespace's ConfigMaps and Secrets and, when `valkey` is set, Valkey. `memory` keeps all of it in this process, which a restart loses: for a local run and the demonstration, and not with `store: kubernetes` or `valkey`. `dynamodb` keeps the same in one DynamoDB table (`ports.dynamodb`), with the platform's credentials, and takes its Blob from `legacy` unless `ports.blob` names its own. |
| `ports.blob` | object | — | Replaces the Blob port (status reports, directory snapshots) with an adapter of its own, whatever `ports.adapter` is. Absent, the Blob is `ports.adapter`'s. |
| `ports.blob.adapter` | one of s3 | **required** | `s3` keeps the blobs in an S3 bucket. |
| `ports.blob.s3` | object | — | Where the S3 adapter keeps its objects. Credentials are the platform's (EKS Pod Identity, IRSA, a Lambda role) and are never configured here. |
| `ports.blob.s3.bucket` | string | **required** | The bucket. It must exist, with public access blocked. |
| `ports.blob.s3.endpoint` | string | — | Overrides the S3 address: LocalStack or an S3-compatible store. |
| `ports.blob.s3.kmsKey` | string | — | A KMS key id, ARN or alias for server-side encryption (SSE-KMS) of every write. Absent, the bucket's default encryption applies. |
| `ports.blob.s3.pathStyle` | boolean | — | Addresses the bucket in the path and not the host name, which LocalStack and most S3-compatible stores need. |
| `ports.blob.s3.prefix` | string | — | A key prefix inside the bucket, for an installation that shares it. Names are `<prefix>/reports/<target>` and `<prefix>/snapshots/<directory>`. |
| `ports.blob.s3.region` | string | — | The bucket's region. Absent, the SDK's own resolution (`AWS_REGION`). |
| `ports.dynamodb` | object | — | The DynamoDB table of the `dynamodb` adapter: one table with a string partition key `pk`, a string sort key `sk` and the TTL attribute `expires`. Credentials are the platform's (EKS Pod Identity, IRSA, a Lambda role) and are never configured here. |
| `ports.dynamodb.create` | boolean | `false` | Create the table (on-demand, TTL on `expires`) at start when it is not there, for a test or a development installation. Off, the table must exist: production uses the one the infrastructure code made, and the role needs `dynamodb:DescribeTable` on it. |
| `ports.dynamodb.endpoint` | string | — | Overrides the DynamoDB address: LocalStack or DynamoDB Local. |
| `ports.dynamodb.region` | string | — | The table's region. Absent, the SDK's own resolution (`AWS_REGION`). |
| `ports.dynamodb.table` | string | **required** | The table's name. |
| `ports.export` | object | — | The store the copies of `exports` are written to (docs/decisions/0034). Absent, nothing is copied out of the service, and `exports` must be empty. |
| `ports.export.adapter` | one of openbao, memory | **required** | `openbao` writes to a KV version 2 mount of an OpenBao. `memory` keeps the copies in this process and is for a test or the demonstration. |
| `ports.export.openbao` | object | — | The OpenBao the copies are written to. Nothing is contacted at start: an OpenBao that is down must not stop the service, since a copy is never a dependency. |
| `ports.export.openbao.address` | string | **required** | The server, https only and with no path: `https://openbao.example`. A token and a login JWT cross this connection. |
| `ports.export.openbao.auth` | object | **required** | How the service logs in, inside each namespace it writes to. The `kubernetes` and `jwt` methods take the same request (`auth/<mount>/login` with a role and a JWT) and differ in the mount they default to and where the JWT comes from. |
| `ports.export.openbao.auth.method` | one of kubernetes, jwt | **required** | `kubernetes`: the Kubernetes auth method, with this pod's ServiceAccount token. `jwt`: the JWT/OIDC method, with a token the platform projects (a ServiceAccount token of another audience, or, on AWS Lambda, the web identity token of outbound federation) from `tokenFile`. |
| `ports.export.openbao.auth.mount` | string | — | The auth method's mount path in each namespace. Absent, the method's name. |
| `ports.export.openbao.auth.role` | string | **required** | The role the login asks for. It must be bound to this workload's identity and carry a policy that reads, creates, updates and patches only the paths `exports` names. |
| `ports.export.openbao.auth.tokenFile` | string | — | Where the JWT is read from, afresh on every login. Absent with `kubernetes`, the pod's ServiceAccount token; required with `jwt`. |
| `ports.export.openbao.caFile` | string | — | A PEM bundle of the authorities that sign the server's certificate, in place of the system's. |
| `ports.export.openbao.mount` | string | `"kv"` | The KV version 2 mount. |
| `ports.export.openbao.namespace` | string | — | The OpenBao namespace an export that names none is written to. |
| `preset` | one of server, k8s-minimal, k8s-openbao, aws-serverless, aws-hybrid, k8s-aws, aws-eks | — | The adapters of a whole platform, one per concern: `server` (no AWS, no Kubernetes), `k8s-minimal`, `k8s-openbao`, `aws-serverless`, `aws-hybrid` (sluis on Lambda, a cluster for the workloads), `k8s-aws` (sluis as a pod on Kubernetes with AWS storage: DynamoDB, S3, KMS-wrapped signing; SSM secrets, or OpenBao with `adapters.secrets`). `aws-eks` is the deprecated name of `k8s-aws`. Absent, the preset the `platform` answers lead to; with neither, the `ports` keys decide. `adapters` and the `ports` keys override single concerns. |
| `probes` | fragment: probes.json | `{"address":":7070"}` |  |
| `publicRootURL` | string | — | The host's root, never carrying the console's mount: the bootstrap surface stays there. Unset follows `publicURL`. |
| `publicURL` | string | — | Where a browser reaches the console, including its mount. The admin-consent redirect URI and the values the setup steps show are built from it. Default http://localhost:8081. |
| `recovery` | object | — | The sign-in that needs no directory: in a cluster, a token for a ServiceAccount proven against the API server; anywhere else, a password. Unset is off for the issuer and, for the hub, on. |
| `recovery.audience` | string | — | The audience its token must carry. |
| `recovery.enabled` | boolean | — | Turn recovery on. Needs `inCluster`, `serviceAccount` and `audience` to be usable. |
| `recovery.passwordSecret` | string | — | Outside a cluster: the secret the recovery password is (`recovery/password`), read at start (the hub keeps only an Argon2id digest of it). Unset generates one and prints it, except on a function, where recovery is then off. `enabled: false` leaves it untouched and refuses the sign-in, so turning it back on needs no new password. |
| `recovery.serviceAccount` | string | — | The ServiceAccount whose token signs in. |
| `release` | string | `"sluis"` | The name this installation's objects carry: the Kubernetes object names (`<release>-github-orgs`, ...) and the prefix of its keys in a shared store. The chart requires it to be the release's full name. |
| `secrets` | object | — | How the secrets this document names are delivered (truvity/policy config.md section 5). Absent is `env`. |
| `secrets.endpoint` | string | — | `ssm`: overrides the SSM address, for LocalStack. |
| `secrets.refresh` | string | `"5m"` | `ssm`: how old the copy may be before it is read again: a rotated secret reaches every instance within it. |
| `secrets.region` | string | — | `ssm`: the region. Unset follows the AWS SDK's own resolution. |
| `secrets.root` | string | — | `file`: the directory the secrets are mounted under. `ssm`: the installation's root, /sluis/<instance>. |
| `secrets.source` | one of env, file, ssm | `"env"` | `env`: the variable SLUIS_SECRET_<NAME> (the name upper-cased, every other character an underscore), for a local run. `file`: the file <root>/<name>, read on every use, so a rotated mount takes effect without a restart. `ssm`: the SecureString <root>/private/config/<name> in AWS SSM Parameter Store, every one under the prefix read at once and again after `refresh` (layout v3, root /sluis/<instance>). |
| `secureCookies` | boolean | — | Mark session cookies Secure. Unset follows the scheme the browser will use: https in the URL. |
| `signingKey` | object | — | The issuer's signing keys: provisioned, never minted here. |
| `signingKey.activationDelay` | string | `"15m"` | How long a newly published key waits before a replica signs with it. At least `pollInterval`. |
| `signingKey.additionalFiles` | array | — | Every OTHER algorithm this installation signs with at once, one file per algorithm. |
| `signingKey.file` | string | — | The primary key. Unset generates one for this process, which a local run may do and nothing else should. Exclusive with `kms`. |
| `signingKey.kms` | object | — | Sign with AWS KMS keys instead of a file: the private key never leaves KMS. Exclusive with `file`. |
| `signingKey.kms.additional` | array | — | Every OTHER algorithm this installation signs with at once, each on its own rotation track, as `signingKey.additionalFiles` does for files. RS256 is the one that exists: for relying parties that need it (Kargo, EKS's OIDC provider). |
| `signingKey.kms.additional[].alg` | one of RS256 | **required** | The algorithm. |
| `signingKey.kms.additional[].keys` | array | **required** | RSA_2048, RSA_3072 or RSA_4096 SIGN_VERIFY keys, oldest first, the last signing; same rotation rules as `keys`. |
| `signingKey.kms.keys` | array | **required** | ECC_NIST_P384 SIGN_VERIFY keys, as ids, ARNs or aliases, oldest first. The LAST signs; the earlier ones stay published until `overlap` after the next one activates. Rotation appends a key. The role needs kms:Sign and kms:GetPublicKey on each. |
| `signingKey.kms.region` | string | — | The keys' region. Unset follows the AWS SDK's own resolution. |
| `signingKey.kms.stateSecret` | string | **required** | The secret (`issuer/state-secret`) holding at least 32 random bytes as base64 or hex (`openssl rand -base64 32`), the same in every replica (a replica whose secret differs refuses to start), from which the sign-in state is derived: a KMS key has no private bytes to derive from. |
| `signingKey.kmsWrapped` | object | — | Sign with key pairs AWS KMS generates and wraps under ONE symmetric key (the `kms-wrapped` adapter): a new pair per algorithm every `rotateEvery`, published before it signs and kept after it is replaced. The private key is decrypted into process memory to sign. Exclusive with `file` and `kms`. |
| `signingKey.kmsWrapped.algorithms` | array | — | The algorithms signed with, the first the installation default. Unset is ES384 and RS256. EdDSA is not supported yet. |
| `signingKey.kmsWrapped.keyId` | string | **required** | The symmetric application key (SYMMETRIC_DEFAULT, ENCRYPT_DECRYPT), as an id, an ARN or an alias. The role needs kms:GenerateDataKeyPairWithoutPlaintext and kms:Decrypt on it, with the encryption context purpose=sluis-signing. |
| `signingKey.kmsWrapped.prepublish` | string | `"15m"` | How long a new key is published before anything signs with it: longer than a verifier caches the key set (Envoy's jwt_authn: 10m). Unset is `activationDelay`. |
| `signingKey.kmsWrapped.region` | string | — | The key's region. Unset follows the AWS SDK's own resolution. |
| `signingKey.kmsWrapped.retain` | string | — | How long a replaced key stays published: at least `lifetimes.token` plus a skew margin. Unset is `overlap`. |
| `signingKey.kmsWrapped.rotateEvery` | string | `"24h"` | How often a new key pair is generated for each algorithm. Longer than `prepublish`, at most 168h. |
| `signingKey.kmsWrapped.stateSecret` | string | **required** | The secret (`issuer/state-secret`) holding at least 32 random bytes as base64 or hex, the same in every replica, from which the sign-in state is derived: a wrapped key is replaced daily and the state must outlive it. |
| `signingKey.overlap` | string | — | How long a rotated key stays published. Unset is `lifetimes.token` plus a margin for clock skew. |
| `signingKey.pollInterval` | string | `"30s"` | How often the files are re-read. |
| `signingKey.verifyOnly` | array | — | PUBLIC keys published in the JWKS and never signed with, so tokens an earlier signer issued keep verifying until they expire: the overlap of a cutover from file keys to `kmsWrapped`. Each is dropped from the JWKS at its `until`. A private key stops the start. |
| `signingKey.verifyOnly[].alg` | one of ES256, ES384, ES512, RS256 | — | The key's algorithm. Unset follows the key. |
| `signingKey.verifyOnly[].file` | string | **required** | A PEM public key (`PUBLIC KEY`, `RSA PUBLIC KEY`, or a certificate) or a JWK file. Never a private key. |
| `signingKey.verifyOnly[].kid` | string | — | The `kid` the old tokens carry. Unset is the RFC 7638 thumbprint of the key, which is what a file signer derived for it. |
| `signingKey.verifyOnly[].until` | string (date-time) | **required** | An RFC 3339 instant after which the key is no longer published: the old tokens' last expiry, plus the verifiers' cache. Required: an overlap has an end. |
| `store` | one of memory, kubernetes | `"memory"` | Where what an operator connected is kept: `memory` keeps nothing (a restart is a fresh installation), `kubernetes` keeps it in this namespace. |
| `valkey` | object | — | The shared store for logins in progress and snapshots. Unset keeps both in memory, correct for one replica. |
| `valkey.address` | string | — | host:port, with no credentials. |
| `valkey.cluster` | boolean | `true` | Speak the cluster protocol. A plain single server needs it off. |
| `valkey.passwordSecret` | string | — | The secret the password is (`valkey/password`). Unset connects with none. |
| `valkey.tls` | boolean | — | Speak TLS to the server. |
<!-- /generated -->

### `controllers`: the GitHub and Slack controllers

`controllers.github` and `controllers.slack` of the service document each run one
controller as a loop of its own in `sluis serve`; **a section that is absent is a
controller that is off**, and an empty one (`github: {}`) takes the defaults. A controller
holds only what is its own below; the release, the policy, `ports`, `platform`, `preset`,
`adapters`, `audit`, `log` and `probes` are the process's, which a controller shares, so
the controllers' `/readyz` is the process's (ready only once each controller has begun),
a controller that fails to start (a refused audit catalogue, an enabled organisation the
policy does not bind) stops the process, and a controller waits for the console to
answer before its first pass. `policy.file` is required when a controller is named.
The code of a controller runs with the service's permissions and in its pod: that cost is
accepted ([0037](../decisions/0037-one-process-everywhere.md)).

What a controller may *change* is the policy document's `controllers.github.enabledOrgs`
and `controllers.slack.enabledWorkspaces`, as before: each organisation or workspace is a
dry run until listed.

<!-- generated: config-keys-controllers -->

Source: `schemas/config/sluis.schema.json`. Generated by `just docs-generate`; keys are listed with their parents first.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `controllers` | object | — | The controllers this process runs beside the issuer and the console, each in its own loop. A controller that is absent is off. Which organisations and workspaces a running controller may CHANGE is the policy document's `controllers.<kind>.enabledOrgs` and `enabledWorkspaces`, as before. A controller reads the console's API as a workload, so the policy's exchange must admit its proof (a ServiceAccount token on Kubernetes, the function role's web identity token on Lambda). |
| `controllers.github` | object | — | The GitHub controller: makes each GitHub organisation's teams match the policy's github table. |
| `controllers.github.appsDir` | string | `"/var/run/github-roster/apps"` | One file per connected organisation: its App's credentials. Read only with `ports.adapter: legacy`; with another adapter they are on the State port. |
| `controllers.github.console` | object | — | How the controller proves itself to the console, when the pod's `tokenFile` is not the way. |
| `controllers.github.console.auth` | object | — | The proof. Absent, `tokenFile`. |
| `controllers.github.console.auth.aws` | object | — | The function role's AWS outbound web identity token (`sts:GetWebIdentityToken`), re-minted every four minutes. The console's issuer must federate the account, and its policy must declare an `aws` matcher for the role. |
| `controllers.github.console.auth.aws.audience` | string | **required** | The audience requested from STS. It must equal `console.awsAudience` (default `<issuerURL>/console`), NOT the audience of the issuer's AWS federation file. |
| `controllers.github.consoleURL` | string | — | The console's API, which answers who holds a group. Unset is `publicURL`. On Kubernetes it is this release's own Service, which the chart writes. |
| `controllers.github.interval` | string | `"15m"` | How long between passes. Positive. |
| `controllers.github.recordsDir` | string | `"/var/run/github-roster/records"` | The console's records, mounted. Read only with `ports.adapter: legacy`. |
| `controllers.github.tokenFile` | string | `"/var/run/secrets/github-roster/token"` | This pod's projected ServiceAccount token, presented to the console and read afresh on every call. |
| `controllers.slack` | object | — | The Slack controller: makes each Slack workspace's user groups match the policy's slack table. |
| `controllers.slack.console` | object | — | How the controller proves itself to the console, when the pod's `tokenFile` is not the way. |
| `controllers.slack.console.auth` | object | — | The proof. Absent, `tokenFile`. |
| `controllers.slack.console.auth.aws` | object | — | The function role's AWS outbound web identity token (`sts:GetWebIdentityToken`), re-minted every four minutes. The console's issuer must federate the account, and its policy must declare an `aws` matcher for the role. |
| `controllers.slack.console.auth.aws.audience` | string | **required** | The audience requested from STS. It must equal `console.awsAudience` (default `<issuerURL>/console`), NOT the audience of the issuer's AWS federation file. |
| `controllers.slack.consoleURL` | string | — | The console's API, which answers who holds a group. Unset is `publicURL`. On Kubernetes it is this release's own Service, which the chart writes. |
| `controllers.slack.credentialsDir` | string | `"/var/run/slack-roster/credentials"` | One file per connected workspace: the app's credentials and its bot token. Read only with `ports.adapter: legacy`; with another adapter they are on the State port. |
| `controllers.slack.interval` | string | `"15m"` | How long between passes. Positive. |
| `controllers.slack.recordsDir` | string | `"/var/run/slack-roster/workspaces"` | The console's records, mounted. Read only with `ports.adapter: legacy`. |
| `controllers.slack.tokenFile` | string | `"/var/run/secrets/slack-roster/token"` | This pod's projected ServiceAccount token, presented to the console and read afresh on every call. |
<!-- /generated -->

(The directory names keep the controllers' old names: they are paths the chart mounts,
and the chart refuses a `tokenFile`, `appsDir`, `credentialsDir` or `recordsDir` that is
not where it mounts them.)

On Kubernetes the controllers run as the release's own ServiceAccount, which the chart gives, by name, `get`, `update` and
`patch` on the ConfigMaps they report into (`<release>-github-status`, `<release>-slack-status`) and `get` and `update` on
the Secret `<release>-github-links` the GitHub controller rewrites as it checks links. The Apps' keys and the console's
records are volumes, so there is no permission to read any other Secret or ConfigMap. The policy's `exchange` must admit
that ServiceAccount (the controllers read the console as it), and the audit installation's `workloadIdentity` map must
name the one account. More than one replica needs `config.ports.adapter: dynamodb`, and the chart refuses it otherwise.
See [Enable a GitHub organisation](../how-to/enable-github-organisation.md) and
[Enable a Slack workspace](../how-to/enable-slack-workspace.md).


## Retired environment variables

Everything the subcommands read from the environment is a key of a document. A retired variable that is still set is
refused at start with the key that replaces it; nothing is ignored (`internal/config/retired.go`, which a test holds to
this page). The platform-supplied `NAMESPACE` (read from the pod's mounted service-account namespace) and `POD_NAME` (the
pod's hostname) are not configuration, and the chart no longer sets them. The procedure:
[migrate from environment variables](../how-to/migrate-from-environment-variables.md).

### The service

| Old variable | Now, in the service document (the policy document where said) |
|---|---|
| `ISSUER_URL` | `issuerURL` |
| `PORT` | `listen.address` (`:<port>`) |
| `HEALTH_PORT` | `probes.address` |
| `API_PORT`, `CONSOLE_PORT` | none: the directory's own listeners are not served by `sluis serve`; the console is on the issuer's listener |
| `DEMO` | `demo` |
| `ALLOW_INSECURE` | `allowInsecure` |
| `IN_CLUSTER` | `inCluster` |
| `PUBLIC_URL`, `PUBLIC_ROOT_URL` | `publicURL`, `publicRootURL` |
| `SECURE_COOKIES` | `secureCookies` |
| `STORE` | `store` |
| `OVERLAY_FILE` | `directory.workspaces`, the declared workspaces themselves (each key by `keySecret`) |
| `POLICY_DIR` | `policy.file`, the one rendered [policy document](policy-document.md) |
| `GROUPS_SCOPING` | `groupsScoping` |
| `CLUSTER` | `cluster` |
| `RELEASE_NAME` | `release` |
| `CLIENT_SECRETS_DIR` | none: a confidential client's secret is the name `clients/<client-id>/secret`, delivered by `secrets` |
| `ADMIN_PASSWORD` | `recovery.passwordSecret`, which names the secret (`recovery/password`) |
| `RECOVERY_ENABLED`, `RECOVERY_SERVICE_ACCOUNT`, `RECOVERY_AUDIENCE` | `recovery.enabled`, `recovery.serviceAccount`, `recovery.audience` |
| `API_AUDIENCE`, `CONSUMERS_FILE` | none: `sluis serve` serves no directory API listener, so its guard configured nothing |
| `LOGIN_DIRECTORY`, `SIGN_OUT_URL` | `login.directory`, `login.signOutURL` |
| `FORWARDED_EMAIL_HEADER`, `FORWARDED_ISSUER`, `FORWARDED_AUDIENCE` | `login.forwarded.emailHeader`, `.issuer`, `.audience` |
| `CONSOLE_ORIGIN`, `CONSOLE_CLIENT_ID` | `console.origin`, `console.client` |
| `OAUTH_CLIENT_ID`, `OAUTH_CLIENT_ID_FILE` | `oauthClient.id`, or the secret `providers/google/<provider>/client-id` named by `oauthClient.provider` |
| `OAUTH_CLIENT_SECRET`, `OAUTH_CLIENT_SECRET_FILE` | the secret `providers/google/<provider>/client-secret`, named by `oauthClient.provider` |
| `OAUTH_CLIENT_SECRET_NAME`, `OAUTH_CLIENT_ID_KEY`, `OAUTH_CLIENT_SECRET_KEY` | `oauthClient.secretName`, `.idKey`, `.secretKey` |
| `SIGNING_KEY_FILE`, `SIGNING_KEY_FILES` | `signingKey.file`, `signingKey.additionalFiles` |
| `SIGNING_KEY_POLL_INTERVAL`, `SIGNING_KEY_ACTIVATION_DELAY`, `SIGNING_KEY_OVERLAP` | `signingKey.pollInterval`, `.activationDelay`, `.overlap` |
| `TOKEN_LIFETIME`, `REFRESH_LIFETIME`, `ABSOLUTE_LIFETIME`, `HOLD_WINDOW` | `lifetimes.token`, `.refresh`, `.absolute`, `.hold` |
| `SESSION_LIFETIME` | `lifetimes.session` |
| `REFRESH_INTERVAL`, `FRESHNESS_WINDOW`, `PROBE_INTERVAL` | `freshness.refreshInterval`, `.freshnessWindow`, `.probeInterval` |
| `EXCHANGE_AUDIENCE`, `CLUSTERS_FILE`, `AWS_FEDERATION_FILE` | `exchange.audience`; and, in the policy document, `exchange.clusters` and `exchange.aws`, the rows themselves |
| `VALKEY_ADDRESS`, `VALKEY_TLS`, `VALKEY_CLUSTER` | `valkey.address`, `.tls`, `.cluster` |
| `VALKEY_PASSWORD` | `valkey.passwordSecret`, which names the secret (`valkey/password`) |
| `GITHUB_OWNERS`, `GITHUB_RUNNER_TIERS`, `GITHUB_APPS_CATALOGUE_FILE` | the policy document's `exchange.github.owners`, `apps.github.runnerTiers`, `apps.github.catalogue` |
| `SLACK_APPS_CATALOGUE_FILE` | the policy document's `apps.slack.catalogue` |
| `AUDIT_WRITER_URL`, `AUDIT_TOKEN_FILE` | `audit.writer`, `audit.tokenFile` |
| `AUDIT_QUERY_URL`, `AUDIT_AUDIENCE`, `AUDIT_FORWARDED_FOR_TRUSTED_HOPS` | `audit.queryURL`, `.audience`, `.forwardedForTrustedHops` |
| `LOG_LEVEL` | `log.level` |
| `SECRET_MANAGERS_FILE` | none: the console's secret-store view was removed in v1.30.0 ([0002](../decisions/0002-mission-boundary-tokens-and-memberships.md)) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` (and the other `OTEL_*`) | unchanged: set by the platform, not by the file. The chart's `telemetry.otlpEndpoint` is removed |

### The controllers

The v1.62 and earlier `controller-github` and `controller-slack` documents' keys, which are the `controllers.github` and
`controllers.slack` sections of the service document since v1.63 (the keys other than `consoleURL`, `tokenFile`,
`appsDir`, `credentialsDir`, `recordsDir`, `interval` and `console` are the service document's own):

| Old variable | `controller-github` | `controller-slack` |
|---|---|---|
| `RELEASE_NAME` | `release` | `release` |
| `POLICY_DIR` | `policy.file` | `policy.file` |
| `CONSOLE_URL` | `consoleURL` | `consoleURL` |
| `TOKEN_FILE` | `tokenFile` | `tokenFile` |
| `APPS_DIR` | `appsDir` | none |
| `CREDENTIALS_DIR` | none | `credentialsDir` |
| `RECORDS_DIR` | `recordsDir` | `recordsDir` |
| `INTERVAL` | `interval` | `interval` |
| `ENABLED_ORGS` | the policy document's `controllers.github.enabledOrgs` | none |
| `ENABLED_WORKSPACES` | none | the policy document's `controllers.slack.enabledWorkspaces` |
| `GITHUB_APPS_CATALOGUE_FILE` | none: the catalogue is the policy document's `apps.github.catalogue` | none |
| `AUDIT_WRITER_URL`, `AUDIT_TOKEN_FILE` | `audit.writer`, `audit.tokenFile` | `audit.writer`, `audit.tokenFile` |
| `LOG_LEVEL` | `log.level` | `log.level` |

### The function's environment (AWS Lambda)

Retired for every role in v1.62.0, with the configuration layer and the `secrets` source: a function that still sets one
stops at start, naming the v1.62 Pulumi library to deploy with, because the binary and the library move together
([AWS Lambda](lambda.md#version-coupling)).

| Old variable | Now |
|---|---|
| `SLUIS_CONFIG_FILE` | `SLUIS_CONFIG`, which names the service document: `/opt/sluis/sluis.yaml` in the configuration layer |
| `SLUIS_SECRET_FILES` | the document's `secrets` source (`ssm`, root `/sluis/<instance>`) and the names its keys give: `signingKey.kms.stateSecret`, `recovery.passwordSecret` |
| `SLUIS_ROLE` | nothing: there is one function, which serves the issuer and the console and runs the controllers' passes (deploy with the v1.63 Pulumi library) |
| any variable set to `ssm:<path>` | the same: a secret is named in the document and read by its `secrets` source |

## Retired keys

A v2 document that names a key v2 retired is refused with where it went. A v1
document (no `apiVersion`) keeps working with every one of them until it moves.

| Document | Retired key | Now |
|---|---|---|
| `serve` | `policyDir` | `policy.file`: render the directory with `sluisctl policy render <dir>` and name the result |
| `serve` | `overlayFile` | `directory.workspaces`, in the document |
| `serve` | `api` (`api.audience`, `api.consumersFile`) | removed: `sluis serve` serves no directory API listener |
| `serve` | `github.owners` | policy `exchange.github.owners` |
| `serve` | `github.runnerTiers`, `github.catalogueFile` | policy `apps.github.runnerTiers`, `apps.github.catalogue` |
| `serve` | `slack.catalogueFile` | policy `apps.slack.catalogue` |
| `serve` | `exports` | policy `exports` |
| `serve` | `exchange.clustersFile`, `exchange.awsFile` | policy `exchange.clusters`, `exchange.aws`, the rows themselves |
| `serve` | `valkey.passwordEnv` | `valkey.passwordSecret` (`valkey/password`) |
| `serve` | `oauthClient.idFile`, `.secretFile`, `.secretEnv` | `oauthClient.provider`: the secrets `providers/google/<provider>/client-id` and `client-secret` |
| `serve` | `adminPasswordEnv`, `recovery.passwordFile` | `recovery.passwordSecret` (`recovery/password`) |
| `serve` | `clientSecretsDir` | none: `clients/<client-id>/secret`, delivered by `secrets` |
| `serve` | `signingKey.kms.stateSecretFile`, `signingKey.kmsWrapped.stateSecretFile` | `stateSecret` (`issuer/state-secret`); the `kms` and `kms-wrapped` adapters' setting is `stateSecret` too |
| the v1 overlay file | `directory.workspaces[].keyFile` | `keySecret` (`directory/<id>/key`) |
| `controller-github` | `policyDir`, `catalogueFile`, `enabledOrgs` | `policy.file`; the policy's `apps.github.catalogue`; the policy's `controllers.github.enabledOrgs` |
| `controller-slack` | `policyDir`, `enabledWorkspaces` | `policy.file`; the policy's `controllers.slack.enabledWorkspaces` |
| `policy` | `version: 1` | `apiVersion: sluis.truvity.github.io/policy/v2` |
| `policy` | `access`, `overlay` | an access document is a layer, not a policy document: `sluisctl policy render` reshapes it into one |
