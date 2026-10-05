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

| Key | Default | Meaning |
|---|---|---|
| `apiVersion` | absent (v1) | `sluis.truvity.github.io/sluis/v3`. A v2 `serve` document and an absent one (v1) load as v3 with no controllers:  see [Documents and `apiVersion`](#documents-and-apiversion) |
| `issuerURL` | **required** | baked into every token and every relying party's trust. There is no default, because one would be a value nobody chose spread across an estate. An http or https URL with no credentials |
| `release` | `sluis` | the name this installation's objects carry (`<release>-github-orgs`, the prefix of its keys in a shared store). **The chart requires it to be the release's full name**, and says what to write |
| `cluster` | unset | what this cluster is called, which becomes part of a ServiceAccount's subject: `<cluster>:k8s:<namespace>:<name>`. Empty keeps the older unqualified form |
| `secrets.source` | `env` | how every secret NAME this document gives is delivered: `env`, `file` or `ssm`. See [secrets](secrets.md) |
| `secrets.root` | required with `file` and `ssm` | `file`: the directory the names are files under (the chart: `/var/run/sluis/secrets`). `ssm`: the installation's root, `/sluis/<instance>`: its configuration secrets are read from `<root>/private/config/`, so two installations share an account by their roots ([SSM layout v3](secrets.md#ssm-layout-v3)) |
| `secrets.region` / `.endpoint` / `.refresh` | the SDK's / AWS / `5m` | `ssm` only: the parameters' region; a LocalStack address; how long before every parameter under the prefix is read again, so a rotation reaches a running instance |
| `store` | `memory` (the chart: `kubernetes`) | where connected workspaces and their credentials are kept. `memory` makes a restart a fresh installation, which is right for a laptop and nothing else |
| `ports.adapter` | `legacy` | the adapter behind the storage ports ([ports](../explanation/ports.md)): `legacy` keeps state where it has always been kept (the namespace's ConfigMaps and Secrets, and Valkey when `valkey.address` is set); `dynamodb` keeps the same in one DynamoDB table shared by every replica ([design/ports.md](port-adapters.md#the-dynamodb-adapter)); `memory` keeps all of it in the process, so a restart loses every login in progress, and is refused with `store: kubernetes` or `valkey.address`. With `dynamodb` or `memory` the domain records too (directory workspaces and their credentials, GitHub organisations and Apps, people's links, the Slack records) are kept in that State, their credentials in the Secrets port (`memory` has its own), and the controllers read them there instead of from mounted files ([design/ports.md](../explanation/store.md)); a secrets adapter is then required, and the start is refused naming it without one. Another value, and the removed `ports.sealer`, are refused |
| `ports.blob.adapter` | (the Blob of `ports.adapter`) | `s3` replaces the Blob port (status reports, directory snapshots) with an S3 bucket, whatever `ports.adapter` is; `ports.blob.s3` is then required |
| `platform`, `preset`, `adapters` | absent | choose the adapters by name, per concern ([design/ports.md](../explanation/ports.md#adapters-presets-and-the-platform)). `platform: {aws, kubernetes, openbao, runtime, replicas}` answers the preset decision tree; `preset` is one of `aws-serverless`, `aws-hybrid`, `k8s-aws` (`aws-eks` is its deprecated name and logs a warning); `server`, `k8s-minimal` and `k8s-openbao` are unavailable, and loading one fails naming the adapters that are not built ([adapters](adapters.md#presets)); `adapters.<concern>: {adapter, settings}` (concerns: `state`, `secrets`, `blobs`, `signing`, `trigger`, `schedule`, `audit`) overrides one concern. Resolution: explicit override, then the preset, then the preset the answers derive. All absent, the `ports` keys decide as before. Start is refused for an adapter that needs an answer that is false, cannot run on the runtime, is `memory` with `replicas` above 1, or is planned and not built; the table is logged once and exported as `sluis_adapter_info{concern,adapter}` |
| `ports.blob.s3.bucket` | (required) | the bucket, which must exist with public access blocked |
| `ports.blob.s3.prefix` | (none) | a key prefix inside the bucket: objects are `<prefix>/reports/<target>` and `<prefix>/snapshots/<directory>` |
| `ports.blob.s3.region` | the SDK's (`AWS_REGION`) | the bucket's region |
| `ports.blob.s3.kmsKey` | (the bucket's default encryption) | a KMS key id, ARN or alias: every write asks for SSE-KMS under it |
| `ports.blob.s3.endpoint`, `ports.blob.s3.pathStyle` | (AWS) | LocalStack or an S3-compatible store: its address, and path-style addressing |
| `ports.dynamodb.table` | **required with `dynamodb`** | the table: a string partition key `pk`, a string sort key `sk` and TTL on `expires` ([design/ports.md](port-adapters.md#the-dynamodb-adapter)) |
| `ports.dynamodb.region` / `.endpoint` | the SDK's (`AWS_REGION`) / AWS | the table's region; LocalStack's or DynamoDB Local's address. Credentials are the platform's (Pod Identity, IRSA, a Lambda role) and are never configured |
| `ports.dynamodb.create` | `false` | make the table at start when it is not there (on-demand, TTL on `expires`), for a test or a development installation. Off binds to the table the infrastructure code made; the role needs `dynamodb:GetItem`, `PutItem`, `DeleteItem`, `Query` and `DescribeTable` on it, and `Scan` for `migrate` |
| `ports.export.adapter` | unset: nothing is copied out | the adapter behind the Export port ([design/ports.md](ports.md#export)): `openbao` writes to a KV version 2 mount of an OpenBao; `memory` keeps the copies in the process, for a test. The policy document's `exports` need one. See [exports](exports.md) |
| `ports.export.openbao.address` | **required with `openbao`** | the OpenBao server, `https://openbao.example`, with no path or credentials. Nothing is contacted at start |
| `ports.export.openbao.caFile` / `.mount` / `.namespace` | system authorities / `kv` / unset | a PEM bundle for the server's certificate in place of the system's; the KV version 2 mount; the OpenBao namespace an export that names none is written to |
| `ports.export.openbao.auth.method` | **required with `openbao`** | `kubernetes` (the Kubernetes auth method, with the pod's ServiceAccount token) or `jwt` (the JWT/OIDC method, with a token read from `tokenFile`). Both log in with `POST auth/<mount>/login {role, jwt}`, inside each namespace written to |
| `ports.export.openbao.auth.mount` / `.role` / `.tokenFile` | the method's name / **required** / the pod's ServiceAccount token for `kubernetes`, **required** for `jwt` | the auth mount path in each namespace; the role the login asks for; and where the JWT is read from, afresh on every login (a projected ServiceAccount token, or on AWS the web identity token of outbound federation) |
| `listen.address` | `:8080` | everything a browser and a relying party reach: discovery, the key set, the flows, the login page, and the console under `console.mount`. The chart takes the Service's and the routes' port from it, and refuses one outside 1-65535 |
| `probes.address` | `:7070` | `/healthz`, `/readyz` |
| `log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `policy.file` | built-in two groups | the one [policy document](policy-document.md) the service decides by, read once at start: the file `sluisctl policy render` writes. The chart requires `/var/run/access-issuer/policy/policy.yaml`. Unset, the built-in two groups (or the demonstration policy under `demo`) |
| `directory.workspaces[]` | unset | the workspaces the deployment declares, which the service adopts at start: see [declared workspaces](declared-workspaces.md). Each names its key by `keySecret` |
| `publicURL`, `publicRootURL` | `http://localhost:8081`, `publicURL` | where a browser reaches the console (with its mount) and the origin root, where the admin-consent callback stays. With `route.host` and `console.mount` set the chart requires `https://<host><mount>` and `https://<host>` |
| `secureCookies` | follows the scheme of `issuerURL` | mark session cookies Secure. The chart refuses `false` on a route served over TLS |
| `groupsScoping` | `report` | how far this installation has moved toward per-audience `groups` scoping ([policy.md#groups-in-a-token-scoping](policy.md#groups-in-a-token-scoping)): `off` computes and logs nothing -- quote it (`"off"`), or YAML reads the bare word as a boolean -- `report` logs what would be dropped without changing a token, `enforce` narrows the claim |
| `demo` | `false` | two tenants held in memory, which need no credential and no network |
| `allowInsecure` | `false` | accept a plain-http issuer URL, for a local run |
| `inCluster` | `false` | recovery proves access to the cluster the pod runs in. Required with `recovery.enabled` |
| `lifetimes.token` / `.refresh` / `.hold` | `1h` / `12h` / `4h` | how long a token lives, how long a refresh lives (the sliding window: a session idle longer than this ends, whatever its absolute limit, so a resource's seven-day `absolute_cap` needs `refresh` raised to match; it is also how long a browser sign-in lasts), and how long a signed-in identity keeps its last granted role while the directory cannot vouch. Caps: the policy may ask for shorter |
| `lifetimes.absolute` | `24h` | the global timeout: no per-client session, and no access or ID token, outlives `auth_time` by more than this, no matter how often it is refreshed. Refused when zero, negative, or shorter than `lifetimes.token`: at render, and at start. A resource in the policy may carry its own `absolute_cap`, longer than this only when it says `read_only: true` and never beyond `168h` ([policy.md](policy-clients.md#absolute-session-of-a-read-only-resource)); the shortest cap among a chain's resources applies, and a chain that touches any other resource falls back to this value |
| `lifetimes.session` | `12h` | how long the console's own session lasts, capped at `lifetimes.absolute` |
| `freshness.refreshInterval` / `.freshnessWindow` / `.probeInterval` | `15m` / `30m` / `5m` | how often the refresher takes a new snapshot per workspace, how old a snapshot may be before its domains stop being authoritative, and how often a credential is probed and the domain list re-read |
| `exchange.audience` | `release` | the audience a workload's ServiceAccount token must be minted for. Without one, every mounted token in every federated cluster would be a proof. The controllers' projected tokens are minted for it. The federated clusters and AWS accounts are not here: they are the policy document's `exchange.clusters` and `exchange.aws`, rows and not files |
| `recovery.enabled` | unset: off for the issuer, on for the hub | the way in for the day the ordinary one is broken. It stores nothing: a short-lived ServiceAccount token proving access to the API server, so the authority is the cluster's own RBAC. **The only thing left that asks the cluster anything.** The chart's default is `true` |
| `recovery.passwordSecret` | unset | the NAME of the recovery password, `recovery/password`, for a hub outside a cluster: delivered by `secrets`, read once at start (it keeps an Argon2id digest, compared in constant time; ten refused attempts a minute pause it for a minute per process). Unset generates one and prints it. With `recovery.enabled: false` the secret is left alone and the sign-in is refused with a message, so turning recovery back on needs no new password. The Pulumi library's Lambda shape generates it at `/sluis/<instance>/private/config/recovery/password`: [Recovery on Lambda](../how-to/recover-on-lambda.md). Every attempt, refused ones included, is the audit event `roster.recovery.signed_in` |
| `recovery.serviceAccount` / `.audience` | the chart: `access-issuer-recovery` for both | the account recovery proves access as, which the chart creates and binds to nobody, and the audience its token must be minted for. Granting `create` on `serviceaccounts/token` for the account is how an installation says who may recover |
| `login.directory` | `true` | whether the console offers a sign-in of its own, under `<mount>/login`. With `console.client` set it is a second door |
| `login.signOutURL`, `login.forwarded.*` | unset | where sign-out sends the browser, and a sign-in an authenticating proxy has already done |
| `console.client` | unset | the declared client the console signs people in as. Somebody with no session is sent to `/authorize`, signs in at the issuer's page, and comes back with the issuer's session set |
| `console.origin` | unset | the one **other** origin allowed to call `SessionService` from a browser. Obsolete on one origin, which is the shipped shape |
| `console.awsAudience` | `<issuerURL>/console` | the audience an AWS role's web identity token must be minted for to be a bearer at the console. Its own, distinct from the policy document's `exchange.aws.audience`, so a token for one door is no proof at the other; the issuer refuses to start with the same value for both ([AWS Lambda](lambda.md#two-audiences-two-doors)) |
| `oauthClient.*` | unset | the client registered once with the directory backend, for sign-in and admin consent. `provider` names its two secrets, `providers/google/<provider>/client-id` and `client-secret`; the id may instead be `id`, which is not a secret. `secretName`, `idKey` and `secretKey` name the Kubernetes Secret the console shows as declared. With none, nobody can sign in and this installation issues tokens to machines only, which is a real posture and is said at start |
| `signingKey.file` | unset: a key generated for the process | the primary signing key, provisioned and never minted here. The chart requires `/var/run/access-issuer/signing-key/<signingKey.key>` |
| `signingKey.kms.keys[]` | unset | sign with **AWS KMS** instead of a file: `ECC_NIST_P384` / `SIGN_VERIFY` keys as ids, ARNs or aliases (e.g. `alias/sluis-signing`), oldest first, **the last one signs**. Exclusive with `signingKey.file` (both is refused at load). The private key never leaves KMS: each token is a `kms:Sign` of the SHA-384 of the JWS signing input (`MessageType: DIGEST`, `ECDSA_SHA_384`), the DER signature is converted to raw `r\|\|s`, and the algorithm is ES384. The `kid` is the RFC 7638 thumbprint of the public key, the same as a file holding that key would have. A key of another spec or usage stops the start. **Rotate by appending** a key: it is published at once and signs only after `activationDelay`, the earlier one stays published for `overlap`, exactly as for files; the list is re-read every `pollInterval`, which also notices an alias moved to another key. Never insert a key before one already seen. `signingKey.additionalFiles` still works beside it for other algorithms |
| `signingKey.kms.additional[]` | unset | every OTHER algorithm signed at once, `{alg: RS256, keys: [...]}`: `RSA_2048`, `RSA_3072` or `RSA_4096` `SIGN_VERIFY` keys, oldest first, the last signing, for the relying parties that need RS256 (Kargo, EKS's OIDC provider; a client or resource pins it with `signing_alg: RS256`). Each token is `kms:Sign` with `RSASSA_PKCS1_V1_5_SHA_256` over a SHA-256 digest, verified against the public half before it is returned. The kid is the RFC 7638 thumbprint. Each algorithm is its own ring with the rotation rules above; an RS256 key here and one in `additionalFiles` clash and stop the start |
| `signingKey.kms.region` | the SDK's own | the keys' region |
| `signingKey.kmsWrapped` | unset | sign with key pairs **KMS generates and wraps under one symmetric key** (the `kms-wrapped` adapter; the AWS Lambda presets' default), rotated automatically; exclusive with `file` and `kms`. The private key is decrypted into process memory to sign, so a leaked signing role can forge offline for as long as the keys are published, and write access to the State's key ring is part of the trust boundary (a `kms` key is non-extractable); the key policy must reserve the signing context to the signing roles (mandatory on a shared key). Fields: `keyId` (the symmetric key, an id, ARN or alias; required), `stateSecret` (as for `kms`; required), `region`, `algorithms` (`ES384`, `RS256`; default both, the first is the default; EdDSA is not supported yet), `rotateEvery` (24h; longer than `prepublish`, at most 168h), `prepublish` (default `activationDelay`: how long a new key is published before it signs), `retain` (default `overlap`, i.e. `lifetimes.token` plus a skew margin; never less). Needs `kms:GenerateDataKeyPairWithoutPlaintext` and `kms:Decrypt` on the key with the encryption context `purpose=sluis-signing`. See [Signing on AWS](aws-signing-key.md) |
| `signingKey.kms.stateSecret` | required with `kms` | the NAME of the sign-in state secret, `issuer/state-secret`: base64 or hex of at least 32 random bytes (`openssl rand -base64 32`; one trailing newline is trimmed, a placeholder is refused), identical in every replica (a short fingerprint is kept in the shared state and a replica that differs refuses to start), that the sign-in state is derived from (a file key derives it from its private bytes; a KMS key has none). Not rotated with the signing key |
| `signingKey.verifyOnly[]` | unset | **public** keys published in the JWKS and never signed with, so tokens an earlier signer issued keep verifying until they expire ([cut over to kms-wrapped signing](../how-to/cut-over-to-kms-wrapped-signing.md)). Each entry: `file` (a PEM public key, `RSA PUBLIC KEY` or certificate, or a JWK; required), `kid` (the `kid` the old tokens carry; unset is the key's RFC 7638 thumbprint, which is what a file signer derived, so it is right for a key a file signer used), `alg` (unset follows the key; `ES256`, `ES384`, `ES512`, `RS256`) and `until` (**required**, an RFC 3339 instant after which the key is not published: an overlap has an end). A **private key stops the start**, in any encoding, and the error names the entry and none of the content. The chart refuses it at render too, before any ConfigMap is applied: a PEM may hold only `PUBLIC KEY` and `CERTIFICATE` blocks, nothing containing "private", and a JWK no private member (`d`, `p`, `q`, `dp`, `dq`, `qi`, `k`). **A private key that ever reached a rendered ConfigMap (a values file, a Helm release, git) must be treated as leaked and rotated.** The issuer also verifies its own old tokens with them (`id_token_hint`). Works beside any signing source |
| `signingKey.additionalFiles[]` | unset | one file per `signingKey.additional` entry, in the order they are declared; the chart requires exactly that list |
| `signingKey.pollInterval` / `.activationDelay` / `.overlap` | `30s` / `15m` / `lifetimes.token` + 5m | live rotation, with no restart: how often the mounted file (or each KMS key's public half) is re-read, how long a newly seen key is published before this replica signs with it (longer than the longest JWKS cache among the verifiers, plus the slowest kubelet projection; refused below `pollInterval`), and how long a superseded key stays published (it must cover `lifetimes.token`) |
| `valkey.address` | unset | host:port of the shared store, with no credentials; unset keeps sessions and snapshots in memory, which is one replica only under `ports.adapter: legacy`; `ports.adapter: dynamodb` shares State between replicas with no Valkey |
| `valkey.passwordSecret` | unset | the NAME of the password secret, `valkey/password`, delivered by `secrets` |
| `valkey.tls` | `false` | speak TLS to the server |
| `valkey.cluster` | `true` (the chart's guidance: `false`) | speak the cluster protocol. **Leave it off for a single-node Valkey:** with one shard it makes the client learn node addresses from `CLUSTER SLOTS` and talk to those, bypassing the Service -- the one mechanism whose job is to survive a pod moving. Turn it on when the store has three shards with a replica each (see [provide a Valkey](../how-to/provide-a-valkey.md)) |
| `audit.writer` | unset | the audit installation's receiver: one address, which takes the records and answers the catalogue's registration on the same port. Set, the service and the controller record into it, each with its own projected token; unset, nothing is kept beyond the log line every record also is |
| `adapters.audit` | derived | `connect` (the default when `audit.writer` is set), `log`, or `sqs` with `settings: {queueURL, region, endpoint, timeout}`; see [the `sqs` adapter](port-adapters.md#the-sqs-adapter). `sqs` needs no `audit.writer` or token, registers no catalogue (it travels in the writer Lambda's package) and needs `sqs:SendMessage` on the queue |
| `audit.tokenFile` | the chart: `/var/run/audit/token` | the projected token presented to the receiver; the chart requires that path when `audit.writer` is set |
| `audit.queryURL` | unset | the installation's query service, for the console's Audit page; unset shows no page. Its own setting: it needs no `audit.writer`, so the page works with every audit sink (`connect`, `log`, `sqs`). The browser never calls it: the console forwards the page's calls server-side, with a token minted for the person, so the query host needs no CORS. A path prefix is kept and the procedure path appended: `https://audit.example.org/sluis` is called as `https://audit.example.org/sluis/audit.v1.QueryService/Search` |
| `audit.audience` | `audit` | the policy client whose audience the Audit page's tokens carry. The policy must declare it, requiring the groups that may read the trail |
| `audit.forwardedForTrustedHops` | `0` | how many of the deployment's own proxies append to `X-Forwarded-For` in front of the service. A record's client address is the entry just left of them, read from the right; the left end is whatever a caller sent, so it is never taken on its own. `0` records the peer |


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

| Key | Default | Meaning |
|---|---|---|
| `controllers.github.consoleURL`, `controllers.slack.consoleURL` | `publicURL` | the console's API, which answers who holds a group. The chart requires this release's own Service plus `console.mount`, and prints it |
| `controllers.<kind>.interval` | `15m` | how long between passes. Positive. Independently, a controller looks at the mounted credentials and records every 30 seconds and passes without waiting for the interval when they change |
| `controllers.<kind>.tokenFile` | `/var/run/secrets/github-roster/token`, `/var/run/secrets/slack-roster/token` | the projected ServiceAccount token, for `exchange.audience`, read on every call (the chart mounts it for the pod's own account) |
| `controllers.<kind>.console.auth.aws.audience` | unset | on AWS Lambda, the audience the controller requests from `sts:GetWebIdentityToken` for its bearer at the console; it must equal the service's `console.awsAudience`. Unset reads `tokenFile`, as on Kubernetes |
| `controllers.github.appsDir` | `/var/run/github-roster/apps` | the mounted `<release>-github-apps` Secret, one file per connected organisation. Read only with `ports.adapter: legacy` |
| `controllers.github.recordsDir` | `/var/run/github-roster/records` | the mounted `<release>-github-orgs` ConfigMap: the organisations' records and the console's requests for a pass. Read only with `ports.adapter: legacy` |
| `controllers.slack.credentialsDir` | `/var/run/slack-roster/credentials` | the mounted `<release>-slack-credentials` Secret, one file per connected workspace; optional |
| `controllers.slack.recordsDir` | `/var/run/slack-roster/workspaces` | the mounted `<release>-slack-workspaces` ConfigMap: workspace records, Slack Connect records, console channel records, confirmations and pass requests; optional |

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
