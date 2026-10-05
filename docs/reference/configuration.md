# sluis — chart and configuration

How the service is configured: the chart's boundary, its values, the four
documents (the service document and the policy), how a secret is named
and delivered, the Kubernetes objects it owns, the roles, and the two things it
expects the deployment to provide.

**One chart, `charts/sluis`, and one image, for the whole product.** It renders the
whole of sluis — the directory, the policy, the OpenID provider,
the login page and the console — and, when `config.controllers` names them, the GitHub
controller and the Slack controller, in the same process. It keeps no audit trail of its own: it records
into an installation of [truvity/audit](https://github.com/truvity/audit)
that the deployment provides, and reads that installation's query service
for the console's Audit page.

## What the chart includes, what it expects

| Included (standard Kubernetes APIs) | Expected to exist |
|---|---|
| Deployment, Service, ServiceAccount + namespaced Role/RoleBinding (with the Kubernetes store) + a cluster-scoped TokenReview role (with recovery), NetworkPolicy, the policy / overlay / federated-cluster / GitHub App catalogue, a Gateway and two `HTTPRoute`s — one for the issuer's own endpoints and one for the console's path — and, only where a `push` block is written, External Secrets `PushSecret`s | a **Valkey** to point at; the Secrets a declared workspace, a declared OAuth client or an externally delivered signing key name; External Secrets and the store each `push` block names |

The service itself reads and writes plain Kubernetes Secrets and
ConfigMaps in its own namespace. It has no dependency on an external-secrets operator, a
cloud parameter store or a backup mechanism. Delivering a *declared* Secret
into the namespace is the deployment's business; an example with
external-secrets is at the end of this page.

Two things it will not do. **It does not create the signing key** —
cert-manager issues one, or `signingKey.existingSecret` names one (RSA, or
ECDSA on P-256, P-384 or P-521) external-secrets delivered — because a
service that mints its own credential is an exception to how every other
credential in this estate is provisioned. And **it puts no authenticating
proxy in front**: this is the thing that authenticates, and a proxy would
have nowhere to send anyone.

**The convention that follows from that:** every value
that consumes a Secret this chart did not create lets you name both **the
Secret and the keys inside it**. The producer is free — external-secrets, a
1Password operator, sealed-secrets, a hand `kubectl create secret`, a Job —
because a chart that dictated key names could not read a Secret already
sitting in the namespace. Key names are only ever fixed for a Secret the
service writes *itself*, where it is the producer and gets to choose.

## Values

| Value | Default | Meaning |
|---|---|---|
| `documents.service`, `documents.policy` | `""` | **the rendered documents** of one installation (`sluisctl render`, [0038](../decisions/0038-estates-render-through-sluis.md)): the service document and the policy document as strings (`--set-file documents.service=rendered/sluis.yaml`). The chart puts each into its ConfigMap **unchanged** and keeps only the deployment-level values (replicas, the service account, mounts, probes, routes, alerts); it holds the documents to what it mounts (the release's full name, `policy.file`, the public URLs the route's, the audit token, the verify-only keys) and refuses a disagreement, naming the key to change in the installation. Set both or neither; with them `config` is not read, and `policy`, `exchange.clusters` and `exchange.aws.accounts` are refused. A secret an `openbao` or `ssm` secrets adapter delivers is not projected from a Kubernetes Secret. Deprecated: the values-mode below (`config`, `policy`, `exchange`, `githubApps.catalogue`, `slackApps`) keeps working for one minor, and NOTES.txt says so while it is used |
| `config` | see [the file](#the-service-document-sluisyaml) | **the service document** (`sluis serve`, with its controllers), rendered as it stands into the ConfigMap `<release>-config` and mounted at `/etc/sluis/config.yaml`, read once at start (a change rolls the pods by their checksum annotation). Validated by `values.schema.json` against the schema the binary uses. It carries `apiVersion: sluis.truvity.github.io/sluis/v3`, and says everything about how the process runs (the issuer URL, lifetimes, the store, Valkey, recovery, audit, the signing key's rotation, `secrets`); `issuerURL` is **required** and has no default, because one would be a value nobody chose spread across an estate. The values below that look policy-like (`exchange.*`, `githubApps`, `slackApps`, `policy`) are rendered into the policy document, not into this one |
| `secrets[]` | `[]` | the secrets the config names, projected as files under `/var/run/sluis/secrets`: `{name, secretName, key}` puts a Secret's key at `<name>`, a secret NAME of the [layout](#secrets) (`valkey/password`, `issuer/state-secret`, `directory/<id>/key`). `config.secrets` must then be `{source: file, root: /var/run/sluis/secrets}`, the default. Each confidential client's Secret is projected as `clients/<id>/secret` without being listed (its key is `policy.clients.<id>.secretKey`, default `client-secret`, and it is left out of the rendered policy document). A secret is never in `config` |
| `secretEnv[]` | `[]` | for a config whose `secrets.source` is `env`: `{name, secretName, key, optional}` puts a Secret's key in the variable `name`, which must be `SLUIS_SECRET_<NAME>` for the name it delivers (`SLUIS_SECRET_VALKEY_PASSWORD`) |
| `secretMounts[]` | `[]` | `{secretName, mountPath}`: a Secret mounted read-only as a directory, for a file a document names by path that is not a secret of the layout (a signing key's `file`). Each key is a file |
| `config.controllers.github`, `config.controllers.slack` | absent (off) | the controllers that run **inside the one process** (v1.63): present is on, `github: {}` takes the defaults, which are the paths the chart mounts. `consoleURL` is required by the chart and is this release's own Service plus `console.mount`; the chart prints it when it is wrong. [The `controllers` section](#controllers-the-github-and-slack-controllers) lists the keys. A change to a controller's section rolls the pod. Refused at render without an `exchange.clusters` row for this cluster or a `console.mount`, because either is a controller that can read nothing. The policy must put the release's ServiceAccount in `all:access-roster:viewer` (`exchange` must admit it); without it every pass fails on the first read. `controllerGithub` and `controllerSlack` are **removed** (v1.63): [the move](#moving-from-v162-three-processes-to-one) |
| `replicaCount` | `2` | two replicas need Valkey (or `config.ports.adapter: dynamodb`); one may use the in-memory store. With a controller in `config.controllers` the chart refuses more than one unless `config.ports.adapter` is `dynamodb`: the controllers' tick leases must be in a State every replica shares, or every replica acts on every target ([high-availability](../operations/high-availability.md#the-controllers-how-they-roll-and-when-a-second-replica-is-safe)). Every replica serves every workspace, including one connected through the console on the other replica: a reader missing locally is opened from the stored credential on first use |
| `image.repository` / `tag` | `ghcr.io/truvity/sluis/sluis` / app version | the one image: one Deployment runs `sluis serve`, and the controllers run in that process |
| `nameOverride` / `fullnameOverride` | `""` / `""` | replace the chart name (the `app.kubernetes.io/name` label and the controllers' selectors) and the release's full name (the prefix of every object, and the value `config.release` must carry). For an installation moving from the access-issuer chart: [see below](#migrating-from-the-access-issuer-chart) |
| `signingKey.existingSecret` / `.key` | `""` / `tls.key` | a Secret holding a PEM private key -- RSA, or ECDSA on P-256, P-384 or P-521. Empty renders a cert-manager `Certificate` instead. **Never minted by the service**: two replicas with two keys hand out tokens half the fleet cannot verify |
| `signingKey.certificate.issuerName` / `.issuerKind` | `selfsigned` / `ClusterIssuer` | the cert-manager issuer that produces the key, when no `existingSecret` is named. The certificate is a by-product; only the key is used |
| `signingKey.certificate.algorithm` / `.size` / `.encoding` | `ECDSA` / `384` / `PKCS8` | the key, and so what the issuer signs with BY DEFAULT: RSA signs RS256, and P-256, P-384 and P-521 sign ES256, ES384 and ES512. ECDSA takes 256, 384 or 521; RSA takes 2048, 3072 or 4096; PKCS1 encodes only RSA. A combination cert-manager would not issue is refused at render. The default P-384 key means ES384 for every audience that pins no `signing_alg` of its own — for the one relying party that lags (Kargo; EKS's associated OIDC provider; both RS256-only), pin **that audience's** policy row instead of this whole installation's default; see `signingKey.additional` below and [policy.md#signing-algorithm-per-audience](policy.md#signing-algorithm-per-audience). Changing THIS value changes the default for every audience that names none, and is not "just a rotation" the way adding a `signingKey.additional` entry is: every other algorithm now lives on its own track, and this value decides which one is "the default" |
| `signingKey.additional[]` | `[]` | every OTHER algorithm this installation signs with AT THE SAME TIME as the default above: `{algorithm, size, encoding, issuerName, issuerKind, renewBefore, duration}`, one cert-manager `Certificate` and `Secret` per entry, each on its OWN rotation track — renewing one never disturbs another's schedule, including the default's. Two entries (or one entry and the default) naming the same algorithm are refused at render: each algorithm publishes only one key at a time. This is how a client or a resource's `signing_alg` (RS256, ES256 or ES384 — [policy.md#signing-algorithm-per-audience](policy.md#signing-algorithm-per-audience)) has a key to actually sign with; naming an algorithm nothing here configures is refused **at issuer start**, not on the first request that reaches it |
| `signingKey.certificate.renewBefore` / `.duration` | `720h` / `8760h` | how long before expiry cert-manager replaces the key, and the certificate's life. A renewal is a **new key** (`rotationPolicy: Always`). `renewBefore` only decides how OFTEN that happens; `config.signingKey.overlap` is what has to be kept longer than `config.lifetimes.token` |
| `directory.push` | absent | **Deprecated** (needs `config.store: kubernetes`; a State-backed deployment uses [`exports`](#exports-and-the-export-port)). a **recovery copy** of `Secret <release>-workspace-credentials`: `{secretStore: {name, kind}, remoteKey, refreshInterval}` renders `PushSecret <release>-workspace-copy`, which writes the whole Secret as one JSON object at `remoteKey` — bundled, because the keys inside are `<workspace-id>.json` and a reconnect mints a new id, so a per-key mapping would go stale while reporting healthy. `kind` defaults to `SecretStore`, `refreshInterval` to `1h`; `deletionPolicy` is fixed at `None`, because the case this exists for is the Secret going away. Refused at render without `directory.store: kubernetes`, without a store or a key, or for two pushes sharing one path. It is a push and not an `ExternalSecret` because the service is the writer: a pull would let a stale copy overwrite a freshly connected workspace. What lands there **is** the credential |
| `githubApps.catalogue[]` | `[]` | GitHub Apps declared as data — `{id, org, name, description, public, permissions, events, installation, grants, push}` each — created and installed by an operator on the GitHub page (the Apps tab: the App's own page). Rendered into the policy document's `apps.github.catalogue` (or write it there, in `policy.apps`); the document is refused, at render and at start, for a malformed entry or a grant naming a group the policy does not declare. A default set to copy ships as the chart's `examples/github-apps.yaml`. See [connect/github-apps-catalogue.md](../connect/github-apps-catalogue.md) |
| `githubApps.catalogue[].grants[]` | `[]` | who may ask for that App's installation tokens, and for how much: `{group, repositories[], permissions{}}` each. `group` is an internal group the policy declares; `repositories` are names in the App's organisation, `["*"]` for all; `permissions` is `{name: level}`. A request is served by the first grant, in catalogue order, that covers all of it ([contract](contracts.md#installation-tokens-at-token)) |
| `githubApps.catalogue[].push` | absent | **Deprecated** (needs `config.store: kubernetes`; a State-backed deployment uses [`exports`](#exports-and-the-export-port)). copy one App's credential to a secret store: `{secretStore: {name, kind}, remoteKey, refreshInterval, deletionPolicy}` renders `PushSecret <release>-github-app-<id>`, which writes `app_id`, `installation_id` and `private_key` at `remoteKey` — that App's three property keys and nothing else. Off unless written, and refused at render for two entries sharing one path in one store, or without `directory.store: kubernetes`. The copy is a real credential, rotated as one. See [connect/infrastructure-as-code.md](../connect/infrastructure-as-code.md) |
| `githubApps.push` | absent | **Deprecated** (needs `config.store: kubernetes`; a State-backed deployment uses [`exports`](#exports-and-the-export-port)). a **recovery copy** of `Secret <release>-github-apps` — the link App and one App per bound organisation, the identities this service acts as — with the same shape and rules as `directory.push`, rendering `PushSecret <release>-github-apps-copy`. Distinct from `catalogue[].push`, which copies one catalogue App's three keys for a consumer that must act as it; this copies the service's own Apps, and only so they can be restored. An App cannot be re-created with its old id, so losing them means every grant rebinds and every installation is re-authorised by hand |
| `slackState.push` | absent | **Deprecated** (needs `config.store: kubernetes`; a State-backed deployment uses [`exports`](#exports-and-the-export-port)). a **recovery copy** of the Slack state, in the shape of `directory.push` with one more key: `{secretStore: {name, kind}, remoteKey, recordsRemoteKey, refreshInterval}` renders `PushSecret <release>-slack-credentials-copy` (the whole of `Secret <release>-slack-credentials`, at `remoteKey`) and `PushSecret <release>-slack-records-copy` (the whole of `Secret <release>-slack-records`, at `recordsRemoteKey`). Each is bundled under one remote key because the keys inside are `<workspace-id>.json`. `recordsRemoteKey` is required and must differ from `remoteKey`. `kind` defaults to `SecretStore`, `refreshInterval` to `1h`; `deletionPolicy` is fixed at `None`. Refused at render without `directory.store: kubernetes`, without a store or either key, or for two pushes sharing one path. The records are a mirror Secret because they live in a ConfigMap and a `PushSecret` reads Secrets only. What lands there **is** every workspace's credential |
| `slackApps[]` | `[]` | Slack Apps declared as data — `{id, workspace, name, description, botScopes, push}` each — created (with a throwaway app configuration token, used once and never stored) and installed (by an owner of the workspace) by an operator on the console's Slack area (the Apps tab). `id` is `[a-z0-9-]`, at most 32, unique, and never changes; `workspace` is a key of the policy's `slack.workspaces` (lowercase letters, digits and `-`, at most 40, as the policy itself requires); `name` defaults to `<workspace>-<id>`, at most 35; `description` at most 140. Rendered into the policy document's `apps.slack.catalogue`; the document is refused for a malformed entry or an entry for a workspace the policy does not name. Needs `directory.store: kubernetes`. See [connect/slack-apps-catalogue.md](../connect/slack-apps-catalogue.md) |
| `slackApps[].push` | absent | **Deprecated** (needs `config.store: kubernetes`; a State-backed deployment uses [`exports`](#exports-and-the-export-port)). copy one App's bot token — one key, `bot_token`, never the client secret or the record — to a secret store: `{secretStore: {name, kind}, remoteKey, refreshInterval, deletionPolicy}`, rendering `PushSecret <release>-slack-app-<id>`. Refused at render for two entries sharing one path in one store, or without `directory.store: kubernetes`. The copy is a real credential, rotated as one |
| `console.mount` | `/console` | where the console sits on this origin. A **path** and not a host, because discovery must be at the root of the origin named in every token's `iss`. It is also what the console prefixes onto every link it hands a browser — `/login` resolves against the origin, where the issuer's page is. Empty serves no console |
| `exchange.clusters[]` | `[]` | the clusters whose workloads may exchange: `{name, issuer, jwksUri}` per cluster, verified against the key set that cluster publishes. Rendered into the policy document's `exchange.clusters` (write them here or in `policy.exchange`, not both). **No secret in any row**, and this service holds access to no cluster — including its own, which is a row like any other |
| `exchange.aws.accounts[]` | `[]` | the AWS accounts whose IAM roles may exchange their outbound-identity-federation token: `{account, name, issuer, jwksUri, orgId, algs}` per account, verified against the key set that account's issuer publishes. Rendered into the policy document's `exchange.aws`. **No secret in any row. Empty verifies no AWS token at all**: any AWS account can mint a valid token for a role of its own, so the row is the trust boundary. See [connect/aws-workloads.md](../connect/aws-workloads.md) |
| `exchange.aws.audience` | the issuer URL | the audience the role must request from `sts:GetWebIdentityToken`; a token for any other is refused |
| `exchange.aws.maxAge` | `5m` | refuse a token whose `iat` is older, whatever its `exp` allows (AWS permits an hour). At most `1h` |
| `route.host` | `""` | the hostname on the gateway. Empty renders no Gateway, HTTPRoute or Certificate, which is right for an installation reached by port-forward |
| `route.rootRedirect` | `""` | where a bare GET of the host goes. The issuer serves nothing at `/` — every endpoint it answers is a named one — so point this at `/console/` and somebody who types the domain lands somewhere useful |
| `route.gatewayClassName`, `route.certificate.issuerName` / `.issuerKind` | `internal`, `internal-ca` / `ClusterIssuer` | which class the Gateway joins, and who issues its TLS certificate |
| `route.certificate.privateKey` | `{}` | the key that TLS certificate is issued for: `{algorithm, size, encoding, rotationPolicy}`, cert-manager's own fields. Empty leaves every one to cert-manager's defaults, an RSA 2048 key. Set it when the issuer will only sign one kind of key — a PKI role pinned to an algorithm refuses at issuance, long after the render succeeded, and the listener stays dark with the reason on the `CertificateRequest`. The same combinations as the signing key are refused at render |
| `route.sharedWith[]` | `[]` | namespaces besides this one allowed to attach an HTTPRoute to this Gateway. A **gateway-level** admission, not a ReferenceGrant: whether a Gateway accepts a route from another namespace is entirely its own `allowedRoutes` |
| `route.parentRefs[]` | `[]` | parents for the issuer's routes, written out in full (e.g. a platform `ListenerSet` carrying `route.host`). When set the chart renders **no Gateway and no TLS Certificate**: the parent owns the listener and its certificate, and `gatewayClassName`, `certificate` and `sharedWith` have no effect. Write `group` and `kind` out |
| `policy` | `{}` | the policy document without its `apiVersion`, which the chart writes: [the policy document](#the-policy-document), held by `values.schema.json` to `schemas/config/policy.schema.json`, rendered into `<release>-policy` and mounted where each process's `policy.file` names it (`config.policy.file` must be `/var/run/access-issuer/policy/policy.yaml`; the controllers read the process's own policy). Render a directory of layers first with `sluisctl policy render` and pass the result here |
| `networkPolicy.enabled` | `false` | |
| `networkPolicy.clients[]` | `[]` | namespaces allowed to reach the service in-cluster: the proxies verifying tokens and the workloads exchanging them |
| `networkPolicy.gatewayNamespace` | `""` | the gateway's namespace, admitted to the service's port besides `clients`. Empty admits no gateway, so with the policy enabled nothing with a browser reaches it |
| `serviceAccount.annotations` | `{}` | annotations on the ServiceAccount, which is how a cloud identity reaches this service: an admission webhook (EKS Pod Identity, GKE Workload Identity, the self-hosted `amazon-eks-pod-identity-webhook`) reads one and injects credentials into every pod using the account. Without it a self-hosted installation cannot give the service an AWS identity, and `audit.s3` has nothing to authenticate with; the chart mounts no credential of its own and takes none as a value. On AWS: `eks.amazonaws.com/role-arn: <the role's ARN>` |
| `resources`, `replicaCount` | as `values.yaml` | the one Deployment's. The controllers' own `resources`, `replicas`, `strategy`, `minReadySeconds` and `podDisruptionBudget` are gone with their Deployments. The Deployment's readiness probe (`/readyz` on `config.probes.address`) is ready only once the whole process, each controller included, has begun, so a release that crash-loops leaves the running pods alone ([runbook](../operations/runbook.md#a-controller-release-that-crash-loops)) |
| `audit.token.audience` / `.expirationSeconds` | `audit` / `3600` | the projected token presented to the receiver |
| `exports.openbao.caBundle` | `""` | PEM of the authorities that sign OpenBao's certificate, for the service's [exports](#exports-and-the-export-port): a ConfigMap `<release>-openbao-ca` mounted at `/var/run/access-issuer/openbao-ca/ca.pem`, which `config.ports.export.openbao.caFile` must then be (the chart refuses another path). Empty mounts nothing |
| `exports.openbao.token.audience` / `.expirationSeconds` | `""` / `3600` | a ServiceAccount token projected at `/var/run/openbao/token` for the `jwt` auth method, which `config.ports.export.openbao.auth.tokenFile` must then be. Empty projects nothing, which is what the `kubernetes` method wants |
| `alerts.rules.exportFailing` / `.exportStale` | enabled, `warning`: 3 failures in `30m` for `15m`; no copy for `10800`s for `10m` | the two rules over the exports ([telemetry](../operations/telemetry.md#alerts)) |
| `image.pullPolicy`, `serviceAccount.name`, `resources`, `podAnnotations`, `nodeSelector`, `tolerations` | | passthrough |

**Two routes, and the second is not tidiness.** A gateway policy attaches
to an `HTTPRoute`, so the console's path is a separate object: anything
put in front of the console on a shared route would also sit in front of
`/token`, `/keys` and discovery, and every relying party in the estate
would be asked to sign in to fetch a key set. The console's route renders
whether or not anything attaches to it.

**The mount is not rewritten away** by the gateway, unlike the older
split chart. The service strips it itself, so a gateway that stripped it
too would hand the console a path it never serves.

## Endpoints

**Three grants, and the six things they add up to.** `grant_types_supported`
names exactly `authorization_code`, `refresh_token` and token exchange,
because those are the three grants. The other three of the six — userinfo,
`end_session` and revocation — are ENDPOINTS, and discovery advertises them
in their own fields. They are counted together because they answer the same
question, *what does this issuer serve*, but listing an endpoint under
`grant_types_supported` would be the metadata lying in a new way.

| Path | Standard | Purpose |
|---|---|---|
| `/.well-known/openid-configuration`, `/keys` | OIDC discovery, JWKS | what relying parties read |
| `/authorize`, `/token`, `/userinfo`, `/end_session` | OIDC | login, tokens, RP-initiated logout |
| `/logout` | ours | the same sign-out for a person rather than a relying party, on GET and on POST. It needs no `id_token_hint`, which the console could not supply anyway — it never redeems the code it gets back, so it holds no ID token. The console's sign-out button points here |
| `/token` with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` | RFC 8693 | CI and workload exchange; the requested `audience` is a client, gated by its `requires` |
| `/token`, the same exchange with `requested_token_type=urn:access-roster:params:oauth:token-type:github-installation-token` and `audience=github-app:<id>` | RFC 8693 | a GitHub App installation token of a catalogue App, under its grants ([contract](contracts.md#installation-tokens-at-token)) |
| `/revoke` | RFC 7009 | revokes a refresh token; what Revoke and "sign out everywhere" call underneath |
| `/login`, `/logout`, `/signed-out` | ours | **the whole of the issuer's HTML**: the sign-in chooser — which names the application being signed in to from the client's declared `display_name` and `description`, and the host its redirect returns to, or says a program on this computer is asking when that redirect is loopback ([policy](policy.md#what-the-sign-in-page-calls-a-client)) — the sign-out a person follows, and where a sign-out lands when the client declares no page of its own. Each runs before there is anyone to authorize, which is why none can be a console page. Minimal HTML, same theme |
| `/account` | ours | a redirect into the console's page for the signed-in person. The page itself lived here through 0.11 because it had to be same-origin with `SessionService`; the console is same-origin and now the same process, and its page for a person already lists the sessions and offers *sign out everywhere*. The address stays because it was linked to and bookmarked; the two POSTs behind it are gone |
| `/.access/grants` | ours | the clients the caller's groups admit it to, and which group admits each — read by `sluisctl kubeconfig` and `aws-config` so a laptop writes a context per cluster and a profile per role without keeping a list that drifts from the policy. A bearer, and it discloses nothing the caller could not work out from its own token |
| `/.access/simulate` | ours | **not built**: what would this identity get, for somebody other than the caller. The console's Rules page has a simulator that answers it, and `/.access/grants` answers it for the caller's own identity |
| `SessionService` (ConnectRPC): `ListSessions{identity? \| client? \| contains?}` → `{sessions, sign_ins}`, `RevokeSessions{identity, client?, session_id?, sso?}` | ours | sessions per identity and per client, with client, how obtained, issued, expires, last refreshed; revoke per identity, per client, or one. Listing and revoking others is operator; listing and revoking your own is any signed-in identity; listing **every** session (neither identity nor client named) is operator-only, paged by `page_size`/`page_token`, and audited. `contains` reads `identity` and `client_id` as SUBSTRINGS rather than exact values — prefix, suffix and middle — and is **operator-only**, because a substring names an unknown set where an exact identity names the caller's own or nobody's. `RevokeSessions` has no such field: a revoke scoped to *anything containing this* is the control that ends more than its caller meant, with no undo. Authorized by the browser's SSO cookie on a same-origin call — which is what the console is — or by a bearer. The `console.origin` CORS gate is unused on one origin, which is the shipped shape, and stays empty there; it remains for a console served from somewhere else, and is the one other origin admitted |
| back-channel logout | OIDC Back-Channel Logout 1.0 | opt-in per client with `backchannel_logout_uri`: a signed `logout+jwt` POSTed to every such client that signed the person in, by the `sid` it saw, when the sign-in ends |
| not served | RFC 8628 device flow, client credentials, RFC 7523 JWT bearer, RFC 7662 introspection, implicit and hybrid flows, session-management iframe, RFC 7591 dynamic client registration | The first three were served through 0.11 and are gone: the device flow is for a machine with no browser, and both headless cases here — a CI job and a workload — are token exchange; client credentials is a machine with a stored secret, which is the thing this design exists not to have; JWT bearer is token exchange with a different spelling, and two ways to say one thing is two things to keep truthful. Introspection never applied — these are JWTs, verified offline against the key set. RFC 7591 is deliberate and stays refused — the Model Context Protocol deprecated it in favour of Client ID Metadata Documents, which this issuer serves instead when `client_documents` names an origin: no endpoint, nothing stored, and an allow-list that keeps the set of origins answerable by reading the repository |

## Declared workspaces (`directory.workspaces`)

```yaml
# the serve document
directory:
  workspaces:
    - id: C0example                  # the backend's tenant id (Google: the customer id)
      backend: google
      admin: admin@example.com       # the account the key impersonates
      keySecret: directory/C0example/key   # the NAME of the key's secret
      serve:                         # optional; omitted serves every domain it owns
        - example.com
      syncGroups:                    # optional; omitted keeps every group in them
        - platform@example.com
```

The service adopts the declared workspaces at start. The key is a secret by
name, `directory/<id>/key`, delivered by `secrets`: on Kubernetes the chart
projects it from the Secret a `secrets` entry names
(`{name: directory/C0example/key, secretName: example-sa-key, key: key.json}`),
read on every use. The service merges declared workspaces with the ones connected
through the console: declared ones are read-only in the console, cannot be
disconnected there (remove them from the document instead) and win when a
domain is claimed twice. Domains are discovered from the backend, exactly
as for a connected workspace.

`serve` narrows a tenant to a subset of the domains it owns. Leave it out
and the service serves all of them, including ones the company adds later —
the ordinary case. Name a subset and the rest are still discovered and
shown, but nothing routes to them and their accounts are never cached:
that is how one installation reads a single domain of a company whose
other domains are none of its business. A domain named here that the
tenant does not own routes nothing and is reported as no longer owned,
which makes it safe to declare a domain that is about to move between
tenants.

`syncGroups` is the same subtraction applied to groups. A company's
directory holds every mailing list it ever made and an installation's
policy speaks about a handful; naming them keeps the rest out of the
cache, out of the pickers and off the pages. It narrows what is **kept**,
not what is read — the service still lists the tenant's groups, because that
list is what an operator chooses from, so the saving is in storage and
attention rather than in the directory's quota. Only a group the last
read held may be named. Empty keeps every group in the served domains.

For a workspace connected through the console the choice is made
**at connect time**, before the first snapshot: the consenting
administrator's own domain is pre-selected, the tenant's other domains
are listed and off, and *all, including ones added later* is an explicit
option. It can be changed afterwards on the directory's page.

This is how an installation that already holds service-account keys goes
live on day one, and connects through consent later at its own pace.

## Consumers of the directory

**There is no directory API listener.** It went with the merge:
the issuer was its only consumer and is now the same process, so the
question a consumer used to ask over the network is a function call.
What the grant model protected is not lost, only unused; the shape stays
written down in [design/trust.md](../design/trust.md#admission-is-not-authorization)
for the day something needs it.

The one machine that reads the directory's answers today, the GitHub
controller, asks the **console's** API — `Explain`, `ListHolders` — with
its own ServiceAccount token, verified against its cluster's published
key set, and the policy's `service_account` matchers put it in
`all:access-roster:viewer`. Any other workload may do the same.

A service that needs to know who somebody is does not ask the directory
at all: it verifies the issuer's token with the `identity` package and
reads the `groups` claim. That is [../connect/service-to-service.md](../connect/service-to-service.md).

## The policy

The service loads one [policy document](#the-policy-document) — the family's
[policy](policy.md) (`groups`, `claims`, `lifetimes`, `clients`, `github` and
the rest) plus the sections that say whom the exchange trusts, which Apps an
operator may make, what the controllers may change and what is exported — from the
file the serve document's `policy.file` names. There is one layer: the console is
read-only, so nothing it does can add to what is declared here. The chart renders
the document from `policy:` in values, which is the same YAML without its
`apiVersion`:

```yaml
policy:
  groups:
    all:access-roster:operator: { members: [platform-admins@example.com] }
    all:access-roster:viewer:   { members: [all@example.com] }
  lifetimes: { default: 12h }
```

## Kubernetes objects the service owns

Everything an operator adds in the console lives here. `<release>` is the
chart's full name, so two hubs in one namespace do not write over each
other, and `<tenant>` is a readable part of the tenant id followed by a
short hash of it — a tenant id belongs to the backend, not to Kubernetes,
so the hash carries the uniqueness the readable part may have lost.

| Object | Holds | Written by |
|---|---|---|
| `ConfigMap <release>-workspace-<tenant>` | backend, domains, served domains, admin, connected by/at, last health, credential type | the service |
| `Secret <release>-workspace-credentials` | one entry per console-connected workspace, key `<tenant>.json`: the credential (refresh token, or service-account key) and a copy of the workspace's record without its health | the service (Connect, UploadKey), created empty at start. Releases before 1.7 kept a `Secret <release>-credential-<tenant>` each; start-up moves them in |
| `Secret <release>-slack-records` | a MIRROR of the Slack records ConfigMap `<release>-slack-workspaces`: exactly its `<workspace>.json` records, `_shared.<name>.json` Slack Connect definitions and `_channel.<workspace>.<name>.json` console channel records, never `_confirm.*` or `_pass.*`. It exists because a `PushSecret` reads Secrets only; nothing reads it but the chart's recovery copy and the service's own start | the service, in the same code path that writes the ConfigMap, and reconciled at start. If the ConfigMap holds no record and this Secret does, start repopulates the ConfigMap from it |
| `ConfigMap <release>-slack-workspaces` | one record per connected Slack workspace (`<workspace>.json`), the Slack Connect records (`_shared.<name>.json`), the console channel records (`_channel.<workspace>.<name>.json`), and the transient operator markers: confirmations (`_confirm.*`), pass requests (`_pass.*`) and consumed install states | the service (Connect, the Slack Connect and channel editors, Refresh, Confirm), created empty at start |
| `Secret <release>-slack-credentials` | one entry per connected workspace, `<workspace-id>.json`: the App's client id and secret and, once installed, the bot token | the service (Connect, the install callback), created empty at start; mounted read-only into the Slack controller |
| `Secret <release>-oauth-client` | OAuth client id and secret | declared via `oauthClient.secret.name` and read-only. The console used to be able to write one; it cannot since the console became read-only, because a credential a console can change is one somebody can change from a browser |
| `Secret <release>-session-key` | signs the session cookie and the consent-flow state | the service, generated on first start; rotate by deleting |
| the signing key | a PEM private key, mounted as a file | **not the issuer** — cert-manager issues one, or external-secrets delivers one. The issuer reads it from the file, never through the API; its key id is the key's own RFC 7638 thumbprint, so nothing has to carry one beside it |
| `ConfigMap <release>-policy` | the policy document: the declared policy, the exchange's clusters and AWS accounts (**no secret in any row**), the GitHub and Slack App catalogues, what the controllers may change and the exports. One file, `policy.yaml`, read once by each process | the chart |
| `PushSecret <release>-github-app-<id>` | the instruction to copy one catalogue App's `app_id`, `installation_id` and `private_key` to the store and path its entry names — that App's three property keys and nothing else. **What lands there is the App's key**, a second durable copy, rotated as one | the chart, for each `githubApps.catalogue` entry carrying `push`; External Secrets does the copying |
| `PushSecret <release>-workspace-copy` | the instruction to copy the whole of `Secret <release>-workspace-credentials`, every key as one JSON object, to the store and path `directory.push` names. A recovery copy: restoring is an operator writing it back, deliberately. `deletionPolicy: None`, so the copy outlives the Secret it is for | the chart, when `directory.push` is written; External Secrets does the copying |
| `PushSecret <release>-github-apps-copy` | the same for `Secret <release>-github-apps` — the link App and one App per bound organisation — to where `githubApps.push` names | the chart, when `githubApps.push` is written; External Secrets does the copying |
| `PushSecret <release>-slack-credentials-copy` | the instruction to copy the whole of `Secret <release>-slack-credentials` to the store and path `slackState.push` names. `deletionPolicy: None` | the chart, when `slackState.push` is written; External Secrets does the copying |
| `PushSecret <release>-slack-records-copy` | the same for `Secret <release>-slack-records`, to `slackState.push.recordsRemoteKey` | the chart, when `slackState.push` is written; External Secrets does the copying |
| `ConfigMap <release>-slack-status` | the Slack controller's last report, one document per workspace | created empty by the service at start; its data replaced by the controller, which is granted this one name (get, update, patch) |
| `Secret <release>-slack-catalogue-apps` | every catalogue Slack App: `<id>.client_id`, `<id>.client_secret` and, once installed, `<id>.slack_bot_token`, beside `<id>.record.json` | the service (a catalogue App's Create and Install), created empty at start |
| `PushSecret <release>-slack-app-<id>` | the instruction to copy one catalogue Slack App's bot token (property `bot_token`, nothing else) to the store and path its entry names. What lands there is the token | the chart, for each `slackApps` entry carrying `push` |
| `ConfigMap <release>-github-status` | the GitHub controller's last report, one document per organisation | created empty by the service at start; its data replaced by the controller, which is granted this one name |
| `ConfigMap <release>-github-orgs` | one record per connected GitHub organisation: App id and slug, installation, connected by and at | the service (Connect a GitHub organisation), created empty at start |
| `Secret <release>-github-apps` | one credential per connected organisation: the App's id, installation and private key, and a copy of the organisation's record; the link App's likewise | the service (Connect), created empty at start so the controller's volume always has a Secret behind it; read by the service only to uninstall on Disconnect |
| `Secret <release>-github-links` | one link per GitHub account (`<id>.json`): its login, the addresses it proves, its state, the person's token pair | the service, which writes a link; the controller, which rewrites it as it checks — the one Secret its Role may update, by name |
| `Secret <release>-github-runner-apps` | every runner App. An installed App is `<tier>.<org>.github_app_id`, `.github_app_installation_id` and `.github_app_private_key` — the names gha-runner-scale-set's `githubConfigSecret` reads — beside `<tier>.<org>.record.json`. An App created and not yet installed has its record and `<tier>.<org>.pending_private_key` only, so a copy never hands runners an App they cannot register with | the service (a runner App's Create and Install), created empty at start; read by the service only to find the installation and to uninstall on Disconnect. A deployment copies the three keys to its runners, for example with an External Secrets `PushSecret` |
| `Secret <release>-github-catalogue-apps` | every catalogue App, by its catalogue id. An installed App is `<id>.github_app_id`, `<id>.github_app_installation_id` and `<id>.github_app_private_key` beside `<id>.record.json` (`version, id, org, app_id, app_slug, installation_id, html_url, connected_at, connected_by`). An App created and not yet installed has its record and `<id>.pending_private_key` only | the service (a catalogue App's Create and Install), created empty at start; read by the service to ask GitHub, as the App, what the App and its installation hold, and to uninstall on Disconnect. A deployment copies it for backup, for example with an External Secrets `PushSecret`; one App's three property keys are projected to a store by `githubApps.catalogue[].push` |

The record and the credential are two objects on purpose. A record is
shown to anyone who may see the console; a credential is written once and
read once, at start. Keeping them apart means the type the console handles
cannot carry a secret by accident, and it makes the failure modes
independent: a record whose credential is missing is a workspace with no
reader, which the console shows as unhealthy with the reason — not a service
that will not start.

Labels on every service-written object: `app.kubernetes.io/managed-by=directory-roster`,
`app.kubernetes.io/part-of=<release>`, and
`access-roster.truvity.github.io/kind` = `workspace`, `workspace-credentials`,
`settings`, `github-status`, `github-orgs` (both the records ConfigMap and
the Apps Secret), `github-links`, `github-runner-apps`, `github-catalogue-apps`, `slack-workspaces` (the records ConfigMap and the credentials Secret), `slack-records`, `slack-status`, `slack-catalogue-apps`, or `credential`
on a per-workspace Secret a release before 1.7 wrote. The workspace id as the backend spells it is the annotation
`access-roster.truvity.github.io/workspace-id`. Releases before these keys
wrote the kind label and the annotation under an older prefix; the service
moves every object of its release to the keys above when it starts, before
it reads any of them, so an upgrade, or a restore of objects an older
release wrote, needs no step of its own (a rollback past it does: see the
CHANGELOG). Export everything with

```sh
kubectl -n directory-roster get secret,configmap -l app.kubernetes.io/managed-by=directory-roster -o yaml
```

### Restoring from the Secrets alone

Five Secrets, and the Slack state below, hold everything a console added that
cannot be minted again, each under a name a deployment knows in advance:

- `<release>-workspace-credentials`;
- `<release>-github-apps`;
- `<release>-github-links`;
- `<release>-github-runner-apps` and `<release>-github-catalogue-apps`,
  whose records are already beside their keys.

A deployment backs them up by copying those five objects. The chart
renders the copy for two of them — `<release>-workspace-credentials`
through [`directory.push`](#values) and `<release>-github-apps` through
[`githubApps.push`](#values), each an External Secrets `PushSecret` of
the whole Secret under one remote key — because those two are the ones
nothing upstream can re-deliver; the other three are a `PushSecret` of
the deployment's own. Nothing in the service depends on the copy.

On a State adapter there are no Secrets to copy, and the same five bundles are
written into OpenBao by the service itself, entry for entry as the Secret held
them: [`exports`](#exports-and-the-export-port) with `source: bundle`. Restore
from one by writing its entries back into the Secret of that name.

Each credential carries a copy of its record. So after the five Secrets
are put back into an empty namespace, the next start does the rest before
reopening anything:

- it restores every console-connected workspace's ConfigMap;
- it restores every GitHub organisation's record and the link App's.

Slack keeps its state in `<release>-slack-credentials`, `<release>-slack-records`
(a mirror of the records ConfigMap) and `<release>-slack-catalogue-apps`; the
chart renders the copy for the first two through `slackState.push`. Restore:
[runbook, Slack state](../operations/runbook.md#slack-state).

A restored workspace shows as never probed until its first probe. A link
token may have rotated since the copy; that person links again. A
declared Secret is re-delivered by whatever declared it.

**`store: memory`** turns all of it off: nothing is written, and a restart
is a fresh installation. It is the default for the binary, because a local
run and the demonstration should need no cluster; the chart's `config` defaults
to `kubernetes`. A service started on the memory store says so at WARN on its
first line, naming what a restart would lose.

## The configuration file

An installation is configured by **two documents**, each one YAML file: the
**service document** (`sluis.yaml`, `apiVersion: sluis.truvity.github.io/sluis/v3`),
which says how the one process runs, and the **policy document**, which says what the
installation decides. Since v1.63 there is one process everywhere
([0037](../decisions/0037-one-process-everywhere.md)): `sluis serve` is the issuer, the
console, the directory hub and, as loops of its own in the same process, the GitHub and
Slack controllers, each on when the service document's `controllers` section names it.
`sluis serve` takes its service document with `--config <file>` or, with no
`--config`, from the variable `SLUIS_CONFIG`; the service document names the policy
document with `policy.file`. Those two are the only thing that configures the
process: `--version` and `--help` are the only other flags.
`sluis tick github|slack <target>` runs one target's tick once and reads the same file
(the controller's section of it); the target comes first: an organisation's login or
`github:links` for GitHub, a workspace's key for Slack. Until a shared State exists a tick
refuses to run (a running controller's lease would not exclude it); with the service scaled to 0, `--unsafe-local-lease` runs it.
**Deprecated for one release:** `sluis controller github` and `sluis controller slack`
still run a controller as a process of its own (and say so in the log), reading either
their own v2 documents or the one v3 document, whose `controllers.<kind>` section they
then take. They are removed in the next release.
[0032](../decisions/0032-one-configuration-file-one-binary-one-chart.md) is the
decision, [0036](../decisions/0036-configuration-is-immutable-per-instance.md) the
rules for how the documents reach an instance, and truvity/policy's
[configuration contract](https://github.com/truvity/policy/blob/master/docs/contracts/config.md)
the rule both follow.

- **Immutable per instance.** A document is read once, at start. A change to
  configuration or policy is a new set of instances, never a reload: on
  Kubernetes the chart renders a `checksum/*` annotation per document, so a
  change rolls the Deployment; on AWS Lambda it is a new configuration layer
  version ([AWS Lambda](../integrations/aws-lambda.md)); elsewhere it is a
  restart. What stays live is credentials and State: a secret is read when it
  is used or on a short refresh, so rotating one is not a deployment.
- **Validated before anything starts.** Each document is held to its JSON Schema
  (`schemas/config/<document>.schema.json`: `sluis` or `policy`, embedded in the
  binary; the v2 `serve`, `controller-github` and `controller-slack` schemas stay for the
  documents this release still loads) first. An
  unknown key, a missing required key or a value of the wrong type refuses to
  start and names the path to it. The chart's `values.schema.json` embeds the
  same schemas under `config` and under `policy`, so the same
  mistake fails `helm install`, and the chart's tests hold what it renders to them.
- **A secret is named, never written.** A key ending in `Secret` holds a secret's
  NAME (`valkey.passwordSecret: valkey/password`), and `secrets.source` says how a
  name is delivered ([Secrets](#secrets)). A key ending in `File` or `Dir` holds a
  path to something that is not a secret, or to a key the platform mounts. A
  secret in a document is refused: there is no key to put it in, and a URL or
  address with a password in it does not match the schema.
- **Telemetry is not here.** It is OpenTelemetry's own `OTEL_*` environment
  (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, ...), read by the SDK and
  set on the pod by the platform. Nothing in a document restates it.
- **The old environment is refused, not ignored.** A variable of the old
  configuration that is still set (`ISSUER_URL`, `VALKEY_PASSWORD`, ...) stops
  the process at start, naming the key that replaces it. The tables
  [below](#migrating-from-environment-variables) are that list.
- **Durations** are Go duration strings (`30s`, `15m`, `168h`).

The schemas are the exhaustive reference, with every key's description and
default. What follows is the orientation: each key, its default when unset, and
what to know. A key the table gives no default for is unset by default, which
is the binary's own behaviour.

### Documents and `apiVersion`

Every document carries an `apiVersion` of the form
`sluis.truvity.github.io/<kind>/<version>`: the service document is `sluis/v3` and the
policy `policy/v2`. **Absent means v1.** A binary
reads its documents' version N and N-1, and converts N-1 as it loads it: this
build reads the service document at v3, and the v2 `serve` document (and v1, with no
`apiVersion`) **as a v3 document with no controllers**, so `sluis serve` on an unchanged
document behaves exactly as before. (The v2 `controller-github` and `controller-slack`
documents are read only by the deprecated `sluis controller` subcommands, and `sluis tick`
and `sluis migrate` read either.) So a deployment rolls the binary first and its
configuration second. A v1 document is held to the schema it was written against
(`schemas/config/v1/`, frozen as v1.61 wrote it) and keeps working unchanged:
what v1 kept in a service document and v2 keeps in the policy document is read
from the files v1 named, and each secret is read where v1 named it. A v2 document
that still names a key v2 retired is refused with where it went, not with the
schema's bare "not a key this service reads" ([Retired keys](#retired-keys)).
A schema change that cannot be converted is a major step, not a minor one.

```yaml
apiVersion: sluis.truvity.github.io/sluis/v3
issuerURL: https://sluis.example
policy: {file: /etc/sluis/policy/policy.yaml}
secrets: {source: file, root: /var/run/sluis/secrets}
controllers:                       # optional: absent is no controller
  github: {consoleURL: "http://sluis.access.svc:8080/console", interval: 15m}
  slack: {consoleURL: "http://sluis.access.svc:8080/console"}
```

Layering exists in exactly one place, rendering the policy
([The policy document](#the-policy-document)): a running binary reads one finished
document and merges nothing.

### Secrets

A document never holds a secret. It gives a secret's **name** (a path of
segments of letters, digits, `.`, `_` and `-`, separated by `/`), and the serve
document's `secrets` says how a name is delivered:

```yaml
secrets:
  source: ssm          # env | file | ssm
  root: /sluis/hive    # file: a directory; ssm: the installation's root
  region: eu-west-1    # ssm only
  refresh: 5m          # ssm only
```

| `source` | A name is delivered as | Read |
|---|---|---|
| `env` (the default) | the variable `SLUIS_SECRET_<NAME>`: the name upper-cased, every character that is not a letter or a digit an underscore (`valkey/password` is `SLUIS_SECRET_VALKEY_PASSWORD`). Two names may not share a variable | once, at start; for a local run |
| `file` | the file `<root>/<name>` | on every use, so a rotated Secret the platform mounts takes effect without a restart. The chart's `secrets` value projects each name as a file under `/var/run/sluis/secrets` |
| `ssm` | the SecureString `<root>/private/config/<name>` of AWS SSM Parameter Store | every parameter under `<root>/private/config/` at once (decrypted, paged), and again once `refresh` (`5m`) has passed |

The names are the layout's ([0036](../decisions/0036-configuration-is-immutable-per-instance.md)),
and the key that holds each is:

| Name | Held by | What it is |
|---|---|---|
| `clients/<client-id>/secret` | a confidential client of the policy | the client's secret. The service reads one per confidential client of the policy it loaded |
| `providers/google/<provider>/client-id`, `.../client-secret` | `oauthClient.provider` | the Google OAuth client registered for the directory |
| `issuer/state-secret` | `signingKey.kms.stateSecret`, `signingKey.kmsWrapped.stateSecret` | the sign-in state secret |
| `recovery/password` | `recovery.passwordSecret` | the recovery password |
| `directory/<id>/key` | `directory.workspaces[].keySecret` | a declared workspace's service-account key |
| `valkey/password` | `valkey.passwordSecret` | the shared store's password |

A name that is not delivered stops the start, naming the name and where it was
looked for; a value is never in an error or a log line. On Kubernetes the usual
source is `file`, the chart projecting each Secret key a `secrets` entry names;
on AWS Lambda it is `ssm`, from the layout below.

### Cutting over to kms-wrapped signing without signing everyone out

Tokens signed by the cert-manager file keys (say ES384 and RS256) carry the `kid`
of those keys. When the installation moves to `signingKey.kmsWrapped`, the old
keys leave the JWKS with the file mounts, and every token they signed stops
verifying. To avoid that, keep the old PUBLIC keys published for a bounded
overlap:

1. Extract the public keys from the live Secrets, and never the private ones:
   `kubectl get secret sluis-signing-key -o jsonpath='{.data.tls\.key}' | base64 -d | openssl pkey -pubout`
   (the same for the additional RSA key).
2. Put them in the chart as `signingKey.verifyOnly[].pem` and name them in the config as
   `config.signingKey.verifyOnly[]` (`file: /var/run/access-issuer/verify-keys/<i>.pem`,
   `until:`), with `signingKey.file: null` and `signingKey.kmsWrapped` as in the
   [chart README](https://github.com/truvity/sluis/blob/master/charts/sluis/README.md). They are mounted from a ConfigMap.
3. Set `until` to the last expiry of a token the old keys signed (`lifetimes.refresh`
   or `lifetimes.absolute`, whichever is longer, counted from the cutover), plus the
   verifiers' cache time. After it the keys are not published; remove the entries at
   leisure.

The `kid` need not be stated: it is the RFC 7638 thumbprint of the public key, the
derivation the file signer used, so the published `kid` is the one the old tokens
carry. Confirm with the JWKS (the discovery document's `jwks_uri`) before the cutover and after: the old `kid`s
must be present in both.

### The OpenBao Secrets adapter

`adapters.secrets: {adapter: openbao}` keeps the Secrets port (the credentials
sluis writes and reads back, and the exports) in an OpenBao KV version 2 mount,
in the layout of [SSM v3](#ssm-layout-v3). It needs no `platform.openbao`
answer. The configuration secrets can stay where they are (`secrets.source:
file`, the chart projecting them), so the two are independent: the source
*delivers the inputs*, the adapter *holds what sluis writes*.

```yaml
secrets: {source: file, root: /var/run/sluis/secrets}
adapters:
  secrets:
    adapter: openbao
    settings:
      address: https://openbao.example        # https, no path
      caFile: /var/run/access-issuer/openbao-ca/ca.pem   # the CA the server is verified against
      namespace: kernel                       # the OpenBao namespace
      mount: kv                               # the KV v2 mount (default kv)
      root: sluis                             # see below
      auth:
        method: jwt                           # jwt | kubernetes
        mount: jwt-kernel                     # the auth mount (default: the method's name)
        role: sluis
        tokenFile: /var/run/openbao/token     # a projected ServiceAccount token, read again at every login
```

| Setting | Meaning |
|---|---|
| `address` | the OpenBao, `https://host[:port]`, no path. **https only**: a token and a login JWT cross the connection, and a redirect is never followed |
| `caFile` | PEM authorities the server's certificate is verified against, instead of the system's. TLS verification is always on: there is no insecure flag |
| `namespace` | the OpenBao namespace the secrets live in; empty is the root namespace |
| `mount` | the KV version 2 mount; `kv` |
| `root` | required, no default: the installation's key hierarchy under the mount |
| `auth.method`, `.mount`, `.role` | the login: `POST auth/<mount>/login {role, jwt}`; the token it returns is kept until 80% of its lease has passed, per namespace |
| `auth.tokenFile` | the JWT; `jwt` requires it, `kubernetes` defaults to the pod's own token |

**The root.** OpenBao namespaces already separate installations, so **the
recommendation is root `sluis` in the installation's own namespace**:
namespace `kernel`, mount `kv`, and

```text
kv/sluis/private/config/<name>                       what an operator seeds (read only if used)
kv/sluis/private/credentials/<kind>/<id>/<ref>       what sluis writes and reads back
kv/sluis/export/<path>                               what sluis copies out, for consumers
```

`sluis/<instance>` is the option for an OpenBao without a namespace per
installation (`kv/sluis/kernel/export/...`). For SSM, which has no namespaces,
the root is `/sluis/<instance>`. Either way `private` and `export` are reserved:
a root with such a segment is refused at start. `secrets.root` is not used by
this adapter (with `source: file` it is a directory).

**Values.** A secret is one KV key with one field, `value` (text) or `value_b64`
(bytes that are not UTF-8), so `bao kv put kv/sluis/private/config/<name>
value=...` seeds one. An export of properties (the JSON object the secrets
export writes) is stored as the **properties themselves**, one field each, so a
consumer's External Secrets reads `property: botToken` of the key exactly as it
does a copy made by `ports.export: openbao`; `Get` puts the object back together.
`PutIfVersion` is KV's check-and-set, atomic on the server. The mount must not
set `cas_required`, or unconditional writes are refused. A value is never
logged, and an error names the operation, the path and the status only.

**An export in another namespace.** An export entry's `namespace` is honoured
on the default destination (`ports.export` unset): the same `export/<path>` of
the same installation is written in that namespace, over the same connection,
with a login of its own there. A preview runner App can thus land in `devel`:

```yaml
exports:
  - source: runner-app
    tier: preview
    org: truvity
    namespace: devel
    path: github-runner-app/preview/truvity   # kv/sluis/export/github-runner-app/preview/truvity in devel
```

A `403` is put down to the token (and costs one new login) only when the token is older than 30 seconds; a fresh token's `403` is the policy's. After an empty listing or a delete of an absent key the adapter asks the server whether the mount exists, and fails loudly with the mount and namespace if it does not (a wrong `mount` or `namespace` otherwise reads as "nothing there"). The role must exist in that namespace too, with the policy below there. The
`ssm` adapter has no namespaces and refuses such an entry.

**The policy** (least privilege; with root `sluis`, mount `kv`, in the
namespace, and again in every namespace an export names):

```hcl
# what sluis writes and reads back
path "kv/data/sluis/private/credentials/*"     { capabilities = ["create", "read", "update"] }
path "kv/metadata/sluis/private/credentials/*" { capabilities = ["list", "delete"] }
# the exports: written, never read by sluis for any other purpose
path "kv/data/sluis/export/*"     { capabilities = ["create", "read", "update"] }
path "kv/metadata/sluis/export/*" { capabilities = ["list", "delete"] }
# List("") (every secret) lists the two directories themselves; List of a
# prefix under credentials/ or export/ needs only the lines above
path "kv/metadata/sluis/private" { capabilities = ["list"] }
path "kv/metadata/sluis/private/" { capabilities = ["list"] }
path "kv/metadata/sluis/export"  { capabilities = ["list"] }
path "kv/metadata/sluis/export/" { capabilities = ["list"] }
# only if configuration secrets are read from OpenBao
path "kv/data/sluis/private/config/*" { capabilities = ["read"] }
```

`read` on the export keys is the idempotence check (an identical write makes no
new version); `delete` and `list` on the metadata are for `Delete` and `List`,
and may be left out when nothing deletes or lists. Consumers get `read` on
`kv/data/sluis/export/*` and nothing else.

### SSM layout v3

An installation has **one root**, `secrets.root`, which is `/sluis/<instance>`
with `<instance>` the installation's name (hive: `hive`, Truvity's: `kernel`), so
two installations share an account without colliding. Under it:

```text
/sluis/<instance>/private/config/<name>           what an operator seeds: the names above
/sluis/<instance>/private/credentials/<kind>/<id>/<ref>   what sluis writes: the credentials of the Secrets port
/sluis/<instance>/export/<path>                   what sluis copies out, for consumers
```

The `ssm` Secrets adapter of the ports takes its root from the same
`secrets.root` (`source: ssm`): naming another root under
`adapters.secrets.settings` is refused, and with neither the start is refused. A
v1 document that names no root keeps `/sluis` (layout v2) until it moves.

**`private` and `export` are reserved:** an instance may not be named either,
since `/sluis/private/...` would then be the tree of another installation, and
a root with such a segment is refused at start. An instance is lower-case letters,
digits and dashes.

The IAM boundary follows the tree. A function that only runs a controller needs
`private/credentials` and `export` and never `private/config`; the one that signs
tokens reads `private/config` as well ([AWS Lambda](../integrations/aws-lambda.md#iam-one-role)).
The paths in [storage layout](storage-layout.md) are v2's; v3 puts the instance
between `/sluis` and `private`.

**Moving to v3.** Copy the configuration secrets, then the credentials:

```sh
sluis migrate ssm-layout --to-root /sluis/<instance> --dry-run   # the report is JSON: names, never a value
sluis migrate ssm-layout --to-root /sluis/<instance>
sluis migrate --from <config on /sluis> --to <config on /sluis/<instance>>
```

`ssm-layout` copies `<from-root>/private/config/...` (default `--from-root
/sluis`) to `<to-root>/private/config/...` under the names the documents give:
`oauth/client-id` and `client-secret` become `providers/google/default/client-id`
and `client-secret`, `clients/<id>` becomes `clients/<id>/secret`, and every other
name keeps its own. It deletes nothing, so remove the v2 parameters once the
installation runs on v3; it writes where the destination is absent, refuses one
that holds another value unless `--overwrite`, and encrypts each copy with the
source parameter's own KMS key unless `--kms-key` names one. `sluis migrate`
([operations/migrate.md](../operations/migrate.md)) then moves the credentials,
through two serve documents whose Secrets adapters name the two roots. The
exports are written again by the next exports pass.

### The policy document

The policy document says what the installation decides. Its tables are the access
model's ([policy.md](policy.md): `vocabulary`, `groups`, `claims`, `lifetimes`,
`resources`, `clients`, `github`, `slack`, `people`, ...), unchanged; beside
them are four sections that used to be spread over the service documents and the
files they named, so that one change no longer touches several documents that
nothing kept consistent:

| Section | Holds | Was |
|---|---|---|
| `exchange.clusters[]` | `{name, issuer, jwksUri}` per federated cluster | `exchange.clustersFile` |
| `exchange.aws` | `audience`, `maxAge` and `accounts[]` (`{account, name, issuer, jwksUri, orgId, algs}`) | `exchange.awsFile` |
| `exchange.github.owners[]` | the organisations whose CI tokens are verified. None verifies none | `github.owners` |
| `apps.github.runnerTiers[]` | the tiers an operator may create a runner App for | `github.runnerTiers` |
| `apps.github.catalogue[]` | the GitHub App catalogue ([connect/github-apps-catalogue.md](../connect/github-apps-catalogue.md)) | `github.catalogueFile`, the GitHub controller's `catalogueFile` |
| `apps.slack.catalogue[]` | the Slack App catalogue ([connect/slack-apps-catalogue.md](../connect/slack-apps-catalogue.md)) | `slack.catalogueFile` |
| `controllers.github.enabledOrgs[]` | the organisations the GitHub controller changes; every other bound organisation is a dry run | the controller's `enabledOrgs` |
| `controllers.slack.enabledWorkspaces[]` | the workspaces the Slack controller changes | the controller's `enabledWorkspaces` |
| `exports[]` | the secrets copied out of the service ([Exports](#exports-and-the-export-port)) | `exports` of `serve` |

There is no secret in any of them: every row is a name and a URL.

```yaml
apiVersion: sluis.truvity.github.io/policy/v2
groups:
  all:access-roster:operator: { members: [platform-admins@example.com] }
  all:access-roster:viewer:   { members: [all@example.com] }
lifetimes: { default: 12h }
github:
  example-org: { members: [all:access-roster:viewer] }
exchange:
  clusters:
    - {name: devel, issuer: "https://kubernetes.default.svc", jwksUri: "https://devel.example/openid/v1/jwks"}
  aws:
    audience: sluis-exchange
    accounts:
      - {account: "111122223333", name: apps, issuer: "https://example-id.tokens.sts.global.api.aws"}
  github: {owners: [example-org]}
apps:
  github: {runnerTiers: [preview, stable]}
controllers:
  github: {enabledOrgs: [example-org]}
```

Whatever the service checked at start across these now belongs to the document,
and runs wherever it is loaded (the binary, `sluisctl policy render`, the Pulumi
library before it publishes one): a catalogue grant naming an undeclared group,
a Slack App for an undeclared workspace, an enabled organisation or workspace the
policy does not bind, an export of an undeclared App, two clusters for one
issuer.

**Rendering.** A process loads exactly one document. `sluisctl policy render
<file or directory> [-o <file>]` writes it from the layers a deployment declares:
a policy file of v1 (`version: 1`), an access document (`access:` and `overlay:`,
reshaped into tables) or a policy document fragment, alone or a directory of them
merged in name order ([sluisctl](sluisctl.md#policy-render-the-one-policy-document)).
Tables merge by key and a key declared twice is an error naming the file; of the
sections, a list concatenates and a list of names unions. The result is held to
every check above, and is the same bytes for the same layers. Name it in each
service document's `policy.file`. On Kubernetes the chart renders it from
`policy:` in values and the Apps' catalogues; on AWS Lambda it is the policy of
the configuration layer.

### The installation document

What an estate knows about one installation, written once, from which both documents
above are rendered
([0038](../decisions/0038-estates-render-through-sluis.md)):
`apiVersion: sluis.truvity.github.io/installation/v1`, held to
`schemas/config/installation.schema.json`. `sluisctl render --installation <file> --out
<dir>` writes `sluis.yaml` and `policy.yaml` from it
([sluisctl](sluisctl.md#render-an-installation-in-the-two-documents-out)); the Pulumi
library takes it as `LambdaArgs.Installation`; a Go program calls `config.Render` from
`github.com/truvity/sluis/config`. It holds no secret.

It is a superset of the two documents: a section is under the key the document gives it,
with that document's own schema, and the installation adds what a document cannot say.

| Key | What it is |
|---|---|
| `instance`, `shape` | the installation's name (its SSM root is `/sluis/<instance>`) and where it runs: `lambda`, `kubernetes` or `server`. Required. |
| `preset`, `release`, `cluster`, `policyFile` | the service document's own keys. `preset` follows the shape when unset (`aws-hybrid`, `k8s-aws` with `aws`, `k8s-openbao` with `openbao`, `k8s-minimal`, `server`); `policyFile` follows it too (`/opt/sluis/policy.yaml` in the layer, where the chart mounts the policy, `/etc/sluis/policy.yaml`). |
| `issuer` | `url` (required), `consoleURL` (default `<url>/console`), `rootURL`, `secureCookies`, `groupsScoping`. |
| `log`, `lifetimes`, `freshness`, `secrets`, `recovery`, `login`, `console`, `oauthClient`, `valkey`, `directory`, `audit`, `signingKey` | the service document's, unchanged. For a KMS signer the state secret is named `issuer/state-secret` when left out. |
| `aws` | `account`, `region` (required for `lambda`), `functionName`, and the resources `table`, `bucket`, `auditQueueURL`, each becoming the setting of the adapter that uses it (`state: dynamodb`, `blobs: s3`, `audit: sqs`). |
| `openbao` | the `openbao` secrets adapter's settings (`address`, `caFile`, `mount`, `namespace`, `root`, `auth`); it chooses that adapter for `secrets`. |
| `adapters` | names single concerns, over what the preset and the resources above give. |
| `exchange` | `audience` (the service document's) and the policy document's `clusters`, `aws` and `github.owners`. |
| `apps`, `exports` | the policy document's, unchanged. |
| `controllers` | `github` and `slack`, each present when the controller is on: its own keys go to the service document's `controllers`, `enabledOrgs` and `enabledWorkspaces` to the policy document's. |
| `access` | the access model's tables (`groups`, `clients`, `resources`, `github`, `slack`, ...), the policy document's, unchanged. |

For shape `lambda` the renderer writes what the library owns: `secrets` (`ssm`, the
instance's root, the region), `recovery.passwordSecret` and the `invoke` trigger's
function, and refuses a different value. A named adapter replaces what a resource
stands for. The output is deterministic and held to the service's loader; the renderer
does not check what only a running process knows (an adapter's platform answer, an
OpenBao's reachability), which is checked at start.

### The service document (`sluis.yaml`)

The chart's `config`, and what `sluis serve` reads: the issuer, the console and the directory hub, one process, with the GitHub and Slack controllers beside them in the same process. The top-level keys below are the old `serve` document's, unchanged; [`controllers`](#controllers-the-github-and-slack-controllers) is what v3 adds.

| Key | Default | Meaning |
|---|---|---|
| `apiVersion` | absent (v1) | `sluis.truvity.github.io/sluis/v3`. A v2 `serve` document and an absent one (v1) load as v3 with no controllers:  see [Documents and `apiVersion`](#documents-and-apiversion) |
| `issuerURL` | **required** | baked into every token and every relying party's trust. There is no default, because one would be a value nobody chose spread across an estate. An http or https URL with no credentials |
| `release` | `sluis` | the name this installation's objects carry (`<release>-github-orgs`, the prefix of its keys in Valkey). **The chart requires it to be the release's full name**, and says what to write |
| `cluster` | unset | what this cluster is called, which becomes part of a ServiceAccount's subject: `<cluster>:k8s:<namespace>:<name>`. Empty keeps the older unqualified form |
| `secrets.source` | `env` | how every secret NAME this document gives is delivered: `env`, `file` or `ssm`. See [Secrets](#secrets) |
| `secrets.root` | required with `file` and `ssm` | `file`: the directory the names are files under (the chart: `/var/run/sluis/secrets`). `ssm`: the installation's root, `/sluis/<instance>`: its configuration secrets are read from `<root>/private/config/`, so two installations share an account by their roots ([SSM layout v3](#ssm-layout-v3)) |
| `secrets.region` / `.endpoint` / `.refresh` | the SDK's / AWS / `5m` | `ssm` only: the parameters' region; a LocalStack address; how long before every parameter under the prefix is read again, so a rotation reaches a running instance |
| `store` | `memory` (the chart: `kubernetes`) | where connected workspaces and their credentials are kept. `memory` makes a restart a fresh installation, which is right for a laptop and nothing else |
| `ports.adapter` | `legacy` | the adapter behind the storage ports ([design/ports.md](../design/ports.md)): `legacy` keeps state where it has always been kept (the namespace's ConfigMaps and Secrets, and Valkey when `valkey.address` is set); `dynamodb` keeps the same in one DynamoDB table shared by every replica ([design/ports.md](../design/ports.md#the-dynamodb-adapter)); `memory` keeps all of it in the process, so a restart loses every login in progress, and is refused with `store: kubernetes` or `valkey.address`. With `dynamodb` or `memory` the domain records too (directory workspaces and their credentials, GitHub organisations and Apps, people's links, the Slack records) are kept in that State, their credentials in the Secrets port (`memory` has its own), and the controllers read them there instead of from mounted files ([design/ports.md](../design/ports.md#the-domain-stores)); a secrets adapter is then required, and the start is refused naming it without one. The `nats` adapter and `ports.sealer` were removed: a file that names either is refused |
| `ports.blob.adapter` | (the Blob of `ports.adapter`) | `s3` replaces the Blob port (status reports, directory snapshots) with an S3 bucket, whatever `ports.adapter` is; `ports.blob.s3` is then required |
| `platform`, `preset`, `adapters` | absent | choose the adapters by name, per concern ([design/ports.md](../design/ports.md#adapters-presets-and-the-platform)). `platform: {aws, kubernetes, openbao, runtime, replicas}` answers the preset decision tree; `preset` is one of `server`, `k8s-minimal`, `k8s-openbao`, `aws-serverless`, `aws-hybrid`, `k8s-aws` (`aws-eks` is its deprecated name and logs a warning); `adapters.<concern>: {adapter, settings}` (concerns: `state`, `secrets`, `blobs`, `signing`, `trigger`, `schedule`, `audit`) overrides one concern. Resolution: explicit override, then the preset, then the preset the answers derive. All absent, the `ports` keys decide as before. Start is refused for an adapter that needs an answer that is false, cannot run on the runtime, is `memory` with `replicas` above 1, or is planned and not built; the table is logged once and exported as `sluis_adapter_info{concern,adapter}` |
| `ports.blob.s3.bucket` | (required) | the bucket, which must exist with public access blocked |
| `ports.blob.s3.prefix` | (none) | a key prefix inside the bucket: objects are `<prefix>/reports/<target>` and `<prefix>/snapshots/<directory>` |
| `ports.blob.s3.region` | the SDK's (`AWS_REGION`) | the bucket's region |
| `ports.blob.s3.kmsKey` | (the bucket's default encryption) | a KMS key id, ARN or alias: every write asks for SSE-KMS under it |
| `ports.blob.s3.endpoint`, `ports.blob.s3.pathStyle` | (AWS) | LocalStack or an S3-compatible store: its address, and path-style addressing |
| `ports.dynamodb.table` | **required with `dynamodb`** | the table: a string partition key `pk`, a string sort key `sk` and TTL on `expires` ([design/ports.md](../design/ports.md#the-dynamodb-adapter)) |
| `ports.dynamodb.region` / `.endpoint` | the SDK's (`AWS_REGION`) / AWS | the table's region; LocalStack's or DynamoDB Local's address. Credentials are the platform's (Pod Identity, IRSA, a Lambda role) and are never configured |
| `ports.dynamodb.create` | `false` | make the table at start when it is not there (on-demand, TTL on `expires`), for a test or a development installation. Off binds to the table the infrastructure code made; the role needs `dynamodb:GetItem`, `PutItem`, `DeleteItem`, `Query` and `DescribeTable` on it, and `Scan` for `migrate` |
| `ports.export.adapter` | unset: nothing is copied out | the adapter behind the Export port ([design/ports.md](../design/ports.md#export)): `openbao` writes to a KV version 2 mount of an OpenBao; `memory` keeps the copies in the process, for a test. The policy document's `exports` need one. See [Exports and the export port](#exports-and-the-export-port) |
| `ports.export.openbao.address` | **required with `openbao`** | the OpenBao server, `https://openbao.example`, with no path or credentials. Nothing is contacted at start |
| `ports.export.openbao.caFile` / `.mount` / `.namespace` | system authorities / `kv` / unset | a PEM bundle for the server's certificate in place of the system's; the KV version 2 mount; the OpenBao namespace an export that names none is written to |
| `ports.export.openbao.auth.method` | **required with `openbao`** | `kubernetes` (the Kubernetes auth method, with the pod's ServiceAccount token) or `jwt` (the JWT/OIDC method, with a token read from `tokenFile`). Both log in with `POST auth/<mount>/login {role, jwt}`, inside each namespace written to |
| `ports.export.openbao.auth.mount` / `.role` / `.tokenFile` | the method's name / **required** / the pod's ServiceAccount token for `kubernetes`, **required** for `jwt` | the auth mount path in each namespace; the role the login asks for; and where the JWT is read from, afresh on every login (a projected ServiceAccount token, or on AWS the web identity token of outbound federation) |
| `listen.address` | `:8080` | everything a browser and a relying party reach: discovery, the key set, the flows, the login page, and the console under `console.mount`. The chart takes the Service's and the routes' port from it, and refuses one outside 1-65535 |
| `probes.address` | `:7070` | `/healthz`, `/readyz` |
| `log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `policy.file` | built-in two groups | the one [policy document](#the-policy-document) the service decides by, read once at start: the file `sluisctl policy render` writes. The chart requires `/var/run/access-issuer/policy/policy.yaml`. Unset, the built-in two groups (or the demonstration policy under `demo`) |
| `directory.workspaces[]` | unset | the workspaces the deployment declares, which the service adopts at start: see [Declared workspaces](#declared-workspaces-directoryworkspaces). Each names its key by `keySecret` |
| `publicURL`, `publicRootURL` | `http://localhost:8081`, `publicURL` | where a browser reaches the console (with its mount) and the origin root, where the admin-consent callback stays. With `route.host` and `console.mount` set the chart requires `https://<host><mount>` and `https://<host>` |
| `secureCookies` | follows the scheme of `issuerURL` | mark session cookies Secure. The chart refuses `false` on a route served over TLS |
| `groupsScoping` | `report` | how far this installation has moved toward per-audience `groups` scoping ([policy.md#groups-in-a-token-scoping](policy.md#groups-in-a-token-scoping)): `off` computes and logs nothing -- quote it (`"off"`), or YAML reads the bare word as a boolean -- `report` logs what would be dropped without changing a token, `enforce` narrows the claim |
| `demo` | `false` | two tenants held in memory, which need no credential and no network |
| `allowInsecure` | `false` | accept a plain-http issuer URL, for a local run |
| `inCluster` | `false` | recovery proves access to the cluster the pod runs in. Required with `recovery.enabled` |
| `lifetimes.token` / `.refresh` / `.hold` | `1h` / `12h` / `4h` | how long a token lives, how long a refresh lives (the sliding window: a session idle longer than this ends, whatever its absolute limit, so a resource's seven-day `absolute_cap` needs `refresh` raised to match; it is also how long a browser sign-in lasts), and how long a signed-in identity keeps its last granted role while the directory cannot vouch. Caps: the policy may ask for shorter |
| `lifetimes.absolute` | `24h` | the global timeout: no per-client session, and no access or ID token, outlives `auth_time` by more than this, no matter how often it is refreshed. Refused when zero, negative, or shorter than `lifetimes.token`: at render, and at start. A resource in the policy may carry its own `absolute_cap`, longer than this only when it says `read_only: true` and never beyond `168h` ([policy.md](policy.md#a-longer-absolute-session-for-a-read-only-resource)); the shortest cap among a chain's resources applies, and a chain that touches any other resource falls back to this value |
| `lifetimes.session` | `12h` | how long the console's own session lasts, capped at `lifetimes.absolute` |
| `freshness.refreshInterval` / `.freshnessWindow` / `.probeInterval` | `15m` / `30m` / `5m` | how often the refresher takes a new snapshot per workspace, how old a snapshot may be before its domains stop being authoritative, and how often a credential is probed and the domain list re-read |
| `exchange.audience` | `release` | the audience a workload's ServiceAccount token must be minted for. Without one, every mounted token in every federated cluster would be a proof. The controllers' projected tokens are minted for it. The federated clusters and AWS accounts are not here: they are the policy document's `exchange.clusters` and `exchange.aws`, rows and not files |
| `recovery.enabled` | unset: off for the issuer, on for the hub | the way in for the day the ordinary one is broken. It stores nothing: a short-lived ServiceAccount token proving access to the API server, so the authority is the cluster's own RBAC. **The only thing left that asks the cluster anything.** The chart's default is `true` |
| `recovery.passwordSecret` | unset | the NAME of the recovery password, `recovery/password`, for a hub outside a cluster: delivered by `secrets`, read once at start (it keeps an Argon2id digest, compared in constant time; ten refused attempts a minute pause it for a minute per process). Unset generates one and prints it. With `recovery.enabled: false` the secret is left alone and the sign-in is refused with a message, so turning recovery back on needs no new password. The Pulumi library's Lambda shape generates it at `/sluis/<instance>/private/config/recovery/password`: [Recovery on Lambda](../operations/recovery-on-lambda.md). Every attempt, refused ones included, is the audit event `roster.recovery.signed_in` |
| `recovery.serviceAccount` / `.audience` | the chart: `access-issuer-recovery` for both | the account recovery proves access as, which the chart creates and binds to nobody, and the audience its token must be minted for. Granting `create` on `serviceaccounts/token` for the account is how an installation says who may recover |
| `login.directory` | `true` | whether the console offers a sign-in of its own, under `<mount>/login`. With `console.client` set it is a second door |
| `login.signOutURL`, `login.forwarded.*` | unset | where sign-out sends the browser, and a sign-in an authenticating proxy has already done |
| `console.client` | unset | the declared client the console signs people in as. Somebody with no session is sent to `/authorize`, signs in at the issuer's page, and comes back with the issuer's session set |
| `console.origin` | unset | the one **other** origin allowed to call `SessionService` from a browser. Obsolete on one origin, which is the shipped shape |
| `console.awsAudience` | `<issuerURL>/console` | the audience an AWS role's web identity token must be minted for to be a bearer at the console. Its own, distinct from the policy document's `exchange.aws.audience`, so a token for one door is no proof at the other; the issuer refuses to start with the same value for both ([AWS Lambda](../integrations/aws-lambda.md#two-audiences-two-doors)) |
| `oauthClient.*` | unset | the client registered once with the directory backend, for sign-in and admin consent. `provider` names its two secrets, `providers/google/<provider>/client-id` and `client-secret`; the id may instead be `id`, which is not a secret. `secretName`, `idKey` and `secretKey` name the Kubernetes Secret the console shows as declared. With none, nobody can sign in and this installation issues tokens to machines only, which is a real posture and is said at start |
| `signingKey.file` | unset: a key generated for the process | the primary signing key, provisioned and never minted here. The chart requires `/var/run/access-issuer/signing-key/<signingKey.key>` |
| `signingKey.kms.keys[]` | unset | sign with **AWS KMS** instead of a file: `ECC_NIST_P384` / `SIGN_VERIFY` keys as ids, ARNs or aliases (e.g. `alias/sluis-signing`), oldest first, **the last one signs**. Exclusive with `signingKey.file` (both is refused at load). The private key never leaves KMS: each token is a `kms:Sign` of the SHA-384 of the JWS signing input (`MessageType: DIGEST`, `ECDSA_SHA_384`), the DER signature is converted to raw `r\|\|s`, and the algorithm is ES384. The `kid` is the RFC 7638 thumbprint of the public key, the same as a file holding that key would have. A key of another spec or usage stops the start. **Rotate by appending** a key: it is published at once and signs only after `activationDelay`, the earlier one stays published for `overlap`, exactly as for files; the list is re-read every `pollInterval`, which also notices an alias moved to another key. Never insert a key before one already seen. `signingKey.additionalFiles` still works beside it for other algorithms |
| `signingKey.kms.additional[]` | unset | every OTHER algorithm signed at once, `{alg: RS256, keys: [...]}`: `RSA_2048`, `RSA_3072` or `RSA_4096` `SIGN_VERIFY` keys, oldest first, the last signing, for the relying parties that need RS256 (Kargo, EKS's OIDC provider; a client or resource pins it with `signing_alg: RS256`). Each token is `kms:Sign` with `RSASSA_PKCS1_V1_5_SHA_256` over a SHA-256 digest, verified against the public half before it is returned. The kid is the RFC 7638 thumbprint. Each algorithm is its own ring with the rotation rules above; an RS256 key here and one in `additionalFiles` clash and stop the start |
| `signingKey.kms.region` | the SDK's own | the keys' region |
| `signingKey.kmsWrapped` | unset | sign with key pairs **KMS generates and wraps under one symmetric key** (the `kms-wrapped` adapter; the AWS Lambda presets' default), rotated automatically; exclusive with `file` and `kms`. The private key is decrypted into process memory to sign, so a leaked signing role can forge offline for as long as the keys are published, and write access to the State's key ring is part of the trust boundary (a `kms` key is non-extractable); the key policy must reserve the signing context to the signing roles (mandatory on a shared key). Fields: `keyId` (the symmetric key, an id, ARN or alias; required), `stateSecret` (as for `kms`; required), `region`, `algorithms` (`ES384`, `RS256`; default both, the first is the default; EdDSA is not supported yet), `rotateEvery` (24h; longer than `prepublish`, at most 168h), `prepublish` (default `activationDelay`: how long a new key is published before it signs), `retain` (default `overlap`, i.e. `lifetimes.token` plus a skew margin; never less). Needs `kms:GenerateDataKeyPairWithoutPlaintext` and `kms:Decrypt` on the key with the encryption context `purpose=sluis-signing`. See [Signing on AWS](../deployment/aws.md#signing-on-aws) |
| `signingKey.kms.stateSecret` | required with `kms` | the NAME of the sign-in state secret, `issuer/state-secret`: base64 or hex of at least 32 random bytes (`openssl rand -base64 32`; one trailing newline is trimmed, a placeholder is refused), identical in every replica (a short fingerprint is kept in the shared state and a replica that differs refuses to start), that the sign-in state is derived from (a file key derives it from its private bytes; a KMS key has none). Not rotated with the signing key |
| `signingKey.verifyOnly[]` | unset | **public** keys published in the JWKS and never signed with, so tokens an earlier signer issued keep verifying until they expire ([cutover](#cutting-over-to-kms-wrapped-signing-without-signing-everyone-out)). Each entry: `file` (a PEM public key, `RSA PUBLIC KEY` or certificate, or a JWK; required), `kid` (the `kid` the old tokens carry; unset is the key's RFC 7638 thumbprint, which is what a file signer derived, so it is right for a key a file signer used), `alg` (unset follows the key; `ES256`, `ES384`, `ES512`, `RS256`) and `until` (**required**, an RFC 3339 instant after which the key is not published: an overlap has an end). A **private key stops the start**, in any encoding, and the error names the entry and none of the content. The chart refuses it at render too, before any ConfigMap is applied: a PEM may hold only `PUBLIC KEY` and `CERTIFICATE` blocks, nothing containing "private", and a JWK no private member (`d`, `p`, `q`, `dp`, `dq`, `qi`, `k`). **A private key that ever reached a rendered ConfigMap (a values file, a Helm release, git) must be treated as leaked and rotated.** The issuer also verifies its own old tokens with them (`id_token_hint`). Works beside any signing source |
| `signingKey.additionalFiles[]` | unset | one file per `signingKey.additional` entry, in the order they are declared; the chart requires exactly that list |
| `signingKey.pollInterval` / `.activationDelay` / `.overlap` | `30s` / `15m` / `lifetimes.token` + 5m | live rotation, with no restart: how often the mounted file (or each KMS key's public half) is re-read, how long a newly seen key is published before this replica signs with it (longer than the longest JWKS cache among the verifiers, plus the slowest kubelet projection; refused below `pollInterval`), and how long a superseded key stays published (it must cover `lifetimes.token`) |
| `valkey.address` | unset | host:port of the shared store, with no credentials; unset keeps sessions and snapshots in memory, which is one replica only |
| `valkey.passwordSecret` | unset | the NAME of the password secret, `valkey/password`, delivered by `secrets` |
| `valkey.tls` | `false` | speak TLS to the server |
| `valkey.cluster` | `true` (the chart's guidance: `false`) | speak the cluster protocol. **Leave it off for a single-node Valkey:** with one shard it makes the client learn node addresses from `CLUSTER SLOTS` and talk to those, bypassing the Service -- the one mechanism whose job is to survive a pod moving. Turn it on when the store has three shards with a replica each (see [Valkey: a recommendation](#valkey-a-recommendation)) |
| `audit.writer` | unset | the audit installation's receiver: one address, which takes the records and answers the catalogue's registration on the same port. Set, the service and the controller record into it, each with its own projected token; unset, nothing is kept beyond the log line every record also is |
| `adapters.audit` | derived | `connect` (the default when `audit.writer` is set), `log`, or `sqs` with `settings: {queueURL, region, endpoint, timeout}`; see [the `sqs` adapter](../design/ports.md#the-sqs-adapter). `sqs` needs no `audit.writer` or token, registers no catalogue (it travels in the writer Lambda's package) and needs `sqs:SendMessage` on the queue |
| `audit.tokenFile` | the chart: `/var/run/audit/token` | the projected token presented to the receiver; the chart requires that path when `audit.writer` is set |
| `audit.queryURL` | unset | the installation's query service, for the console's Audit page; unset shows no page. Its own setting: it needs no `audit.writer`, so the page works with every audit sink (`connect`, `log`, `sqs`). The browser never calls it: the console forwards the page's calls server-side, with a token minted for the person, so the query host needs no CORS. A path prefix is kept and the procedure path appended: `https://audit.example.org/sluis` is called as `https://audit.example.org/sluis/audit.v1.QueryService/Search` |
| `audit.audience` | `audit` | the policy client whose audience the Audit page's tokens carry. The policy must declare it, requiring the groups that may read the trail |
| `audit.forwardedForTrustedHops` | `0` | how many of the deployment's own proxies append to `X-Forwarded-For` in front of the service. A record's client address is the entry just left of them, read from the right; the left end is whatever a caller sent, so it is never taken on its own. `0` records the peer |

### Exports and the export port

The policy document's `exports` copy the secrets the console keeps (they are in the Secrets port, and no
Kubernetes Secret holds them) into OpenBao, where the programs that act as an App
and cannot ask the service read them, and where the recovery bundles are kept
([0034](../decisions/0034-exports-go-to-openbao-directly.md)). The serve document's `ports.export`
says which OpenBao and how to log in. It needs a `ports.adapter` other than `legacy`: on
`legacy` the Secrets still exist and the chart's `push` values (deprecated) copy
them.

| `source` | Fields | Copies | Written as | Mode |
|---|---|---|---|---|
| `slack-app` | `app` | a catalogue Slack App's bot token, once it is installed | `bot_token` | patch |
| `github-app` | `app` | a catalogue GitHub App, once it is installed | `app_id`, `installation_id`, `private_key` | patch |
| `runner-app` | `tier`, `org` | a runner App, once it is installed | `github-app-id`, `github-installation-id`, `github-private-key` | patch |
| `bundle` | `bundle` | `workspace-credentials`, `github-apps`, `github-links`, `github-runner-apps`, `github-catalogue-apps`, `slack-credentials` or `slack-records`, whole | one JSON document per entry, as the Secret of that name held them | replace |

`path` is the key under the KV mount and `namespace` the OpenBao namespace (a
runner App of the preview tier goes to `devel`, the rest to `kernel`). `properties`
maps a property of the App to the name it is written as and writes only those.
`interval` (default `1h`, at least `1m`) is how often the copy is made again with
nothing changed. `name` (default `<source>.<what>`, for instance
`slack-app.alerts`) identifies the export in the log, the metrics and its lease.

The blocks that reproduce, on the kernel cluster, the External Secrets
PushSecrets the estate ran before (`ports.adapter: dynamodb` with a secrets adapter, the
`slackApps` and `apps.github.runnerTiers` declared as usual), first in the serve
document, then in the policy document:

```yaml
# the serve document
ports:
  export:
    adapter: openbao
    openbao:
      address: https://openbao.kernel.truvity.private
      caFile: /var/run/access-issuer/openbao-ca/ca.pem   # exports.openbao.caBundle
      mount: kv
      namespace: kernel
      auth:
        method: jwt                      # or kubernetes
        mount: jwt-kernel
        role: sluis-writer
        tokenFile: /var/run/openbao/token                # exports.openbao.token.audience
```

```yaml
# the policy document
exports:
  - {source: slack-app, app: alerts, path: slack-apps/alerts}
  - {source: slack-app, app: alerts-trustform, path: slack-apps/alerts-trustform}
  - {source: slack-app, app: deadman, path: slack-apps/deadman}
  - {source: runner-app, tier: preview, org: trust-form, namespace: devel, path: arc/trustform}
  - {source: runner-app, tier: preview, org: truvity, namespace: devel, path: arc/truvity}
  - {source: runner-app, tier: stable, org: trust-form, path: arc/trustform}
  - {source: runner-app, tier: stable, org: truvity, path: arc/truvity}
  - {source: bundle, bundle: workspace-credentials, path: sluis-backup/workspace-credentials}
  - {source: bundle, bundle: github-apps, path: sluis-backup/github-apps}
  - {source: bundle, bundle: github-links, path: sluis-backup/github-links}
  - {source: bundle, bundle: github-runner-apps, path: sluis-backup/github-runner-apps}
  - {source: bundle, bundle: github-catalogue-apps, path: sluis-backup/github-catalogue-apps}
  - {source: bundle, bundle: slack-credentials, path: slack-state/credentials}
  - {source: bundle, bundle: slack-records, path: slack-state/records}
```

What the service does, and does not do:

- **A copy, asynchronous, never a dependency.** An export runs out of band after
  the State write that changed its source, so sign-in, a tick and a console action
  neither wait for it nor learn of its failure. An OpenBao outage changes nothing
  live: the copy is stale until the next attempt, which is retried after 5 seconds,
  doubling to 5 minutes. The service starts with OpenBao down.
- **When.** Every export is made once at start, again within seconds of a change to
  its source (a watch of the State, at most once in 30 seconds per export), and again
  every `interval`. Where the State cannot be watched, only the interval remains.
- **Exactly one writer.** Each export runs under a lease on the State, so replicas
  divide the exports; an identical write changes nothing, so a State that is not
  shared costs nothing but a read.
- **Nothing to copy is not written.** An App created and not installed, and an empty
  bundle, are `skipped`: the key is not touched and a good copy is never emptied.
- **Never deleted.** Removing an export, or an App, leaves the copy where it is.
- **KV version 2 semantics.** `replace` is a `POST` of the whole key; `patch` is a
  `PATCH` with a JSON merge patch (the other properties of the key stay), and a
  `POST` when the key is absent. Both read the key first and write nothing when it
  already holds the data, so the reconcile makes no new KV version. External Secrets'
  vault provider does the same read-merge-write for a PushSecret with a `property`
  (it reads the key, sets the property and writes the key back), and a `POST` of the
  whole data for one without: assumed from its documented behaviour, and the reason a
  patch is the right mode for `runner-app`, whose key other properties may share.
- **No Kubernetes RBAC.** The service reads State and writes OpenBao; it holds no
  permission on Secrets or ConfigMaps for this, and the chart's Roles are unchanged.
  The `kubernetes` auth method has OpenBao itself review the pod's token.
- **Checked with the policy.** An export of an App the catalogues do not declare, or
  of a runner tier `apps.github.runnerTiers` does not list, is refused where the
  policy document is loaded, not at the first copy.
- **OpenBao policy.** The role needs `read`, `create`, `update` and `patch` on
  `kv/data/<prefix>/*` in each namespace it writes to. Never `list` or `delete`.

The metrics, the two alerts and the dashboard row are in
[telemetry](../operations/telemetry.md#the-exports).

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

(The directory names keep the controllers' old names: they are paths the chart mounts,
and the chart refuses a `tokenFile`, `appsDir`, `credentialsDir` or `recordsDir` that is
not where it mounts them.)

On Kubernetes the controllers run as the release's own ServiceAccount, which the chart
gives, by name, `get`, `update` and `patch` on the ConfigMaps they report into
(`<release>-github-status`, `<release>-slack-status`) and `get` and `update` on the Secret
`<release>-github-links` the GitHub controller rewrites as it checks links. The Apps' keys
and the console's records are volumes, so there is no permission to read any other Secret
or ConfigMap. The policy's `exchange` must admit that ServiceAccount (the controllers read
the console as it), and the audit installation's `workloadIdentity` need map the one
account. More than one replica needs `config.ports.adapter: dynamodb`, and the chart
refuses it otherwise. See [Connect a GitHub organisation](../connect/github-organisation.md)
and [Connect a Slack workspace](../connect/slack-workspace.md).

### Moving from v1.62 (three processes to one)

1. **Kubernetes.** Move each controller's values into `config.controllers.github` /
   `.slack` (present is on; the keys are the table above), fold
   `controllerX.config.{policy,release,log,ports,platform,preset,adapters,audit,probes}`
   into the one `config`'s own keys, and delete `controllerGithub` and `controllerSlack`
   (refused at render if left). Three Deployments become one; the controllers' ServiceAccounts,
   ConfigMaps (`<release>-github-roster-config`, `<release>-slack-roster-config`) and
   PodDisruptionBudgets go. [Chart README](../../charts/sluis/README.md#moving-from-v162-three-deployments-to-one).
2. **Policy.** `exchange` must admit the one ServiceAccount where it admitted
   `<release>-github-roster` and `<release>-slack-roster`, and the audit installation's
   `workloadIdentity` map likewise ([runbook](../operations/runbook.md)).
3. **AWS Lambda.** One function, one role, one document: [AWS Lambda](../integrations/aws-lambda.md#moving-a-v162-installation-to-v163-one-function) and [deployment on AWS](../deployment/aws.md#moving-from-v162-three-functions-to-one). Keep `FunctionName: "<prefix>-http"` to avoid replacing the function, role, log group and API integration.
4. **Telemetry.** The controllers' series report as `access-issuer`: a dashboard or alert that
   selects `service_name` `github-roster` or `slack-roster` must select `access-issuer`.
5. **Not yet.** A v2 `serve` document loads unchanged (no controllers); `sluis controller
   github|slack` still work, deprecated, for this one release.

## Migrating from the access-issuer chart

[0032](../decisions/0032-one-configuration-file-one-binary-one-chart.md)
replaces three binaries, three images and the `access-issuer` chart with **one
binary, `sluis`, one image and one chart**. (This section is the v1.5x history; the `controllerGithub` and
`controllerSlack` values and the `controller github|slack` commands it names were themselves
replaced in v1.63 by one process: see [Moving from v1.62](#moving-from-v162-three-processes-to-one).) This is a breaking change
in one release; nothing is kept as an alias.

| Before | After |
|---|---|
| `oci://ghcr.io/truvity/charts/access-issuer` | `oci://ghcr.io/truvity/charts/sluis` |
| `ghcr.io/truvity/sluis/access-issuer` | `ghcr.io/truvity/sluis/sluis`, run as `sluis serve --config <file>` |
| `ghcr.io/truvity/sluis/github-roster` | the same image, run as `sluis controller github --config <file>` |
| `ghcr.io/truvity/sluis/slack-roster` | the same image, run as `sluis controller slack --config <file>` |
| `schemas/config/access-issuer.schema.json`, `github-roster.schema.json`, `slack-roster.schema.json` | `serve.schema.json`, `controller-github.schema.json`, `controller-slack.schema.json` |
| values `githubRoster:`, `slackRoster:` | `controllerGithub:`, `controllerSlack:` (`enabled`, `config`, `resources` as before) |
| values `githubRoster.image`, `slackRoster.image` | removed: a controller runs the chart's one `image` |
| `config.release` and each controller's `config.release`, unset: `access-issuer` | unset: `sluis` |

`sluis migrate --from <config> --to <config>`
([0031](../decisions/0031-a-generic-migration-tool.md),
[operations/migrate.md](../operations/migrate.md)) reads two `serve` files, one
per storage, and takes its own flags beside them. `sluis tick` (one reconciler
pass, [0032](../decisions/0032-one-configuration-file-one-binary-one-chart.md))
is a later change; the controllers' loops are `controller github` and
`controller slack`.

**What the chart renames.** The chart's name is part of the full name
(`<release>-<chart>`, or the release alone when it is the chart's name), and so
of every object. Moving to the `sluis` chart without a value renames
**every** object: the Deployments, the Service, the ServiceAccounts and their
Roles, every ConfigMap and the Secrets the chart generates (the cert-manager
signing key's Secret among them, which would be a new key). `config.release`,
the name the service writes its Kubernetes objects under (`<release>-github-orgs`,
`<release>-github-apps`, `<release>-slack-workspaces`, the status ConfigMaps and
the links Secret), must be the full name, so it moves with them and the service
would start with an empty store. Two values keep all of it:

```yaml
nameOverride: access-issuer
fullnameOverride: <the release's current full name>   # `kubectl get deploy` shows it
config:
  release: <the same>                                  # unchanged: it is what it was
controllerGithub:
  config:
    release: <the same>
```

With them, every object keeps its name, every Deployment keeps its immutable
selector, and the upgrade is a rollout of the same objects onto the new image.
Without them, it is a new installation beside the old, with a new store and a
new signing key.

**Names that change either way.**

| Object | Before | After |
|---|---|---|
| Container names | `access-issuer`, `github-roster`, `slack-roster` | `serve`, `controller-github`, `controller-slack` |
| `app.kubernetes.io/component` on the config ConfigMaps and the controllers' objects | `access-issuer`, `github-roster`, `slack-roster` | `serve`, `controller-github`, `controller-slack` |
| Where the config file is mounted | `/etc/access-issuer/config.yaml`, `/etc/github-roster/config.yaml`, `/etc/slack-roster/config.yaml` | `/etc/sluis/config.yaml`, `/etc/sluis/controller-github.yaml`, `/etc/sluis/controller-slack.yaml` |

**Names that do not change** (given the two overrides, or in a fresh
installation they are `<full name>` plus): the controllers' Deployments,
ServiceAccounts, Roles and ConfigMaps (`-github-roster`, `-slack-roster`,
`-github-roster-config`, `-slack-roster-config`), `-config`, `-policy`,
`-signing-key`, the Service, the status and records objects the
service writes, the paths the chart mounts the policy, the signing key and
the controllers' credentials at (`/var/run/access-issuer/...`,
`/var/run/github-roster/...`, `/var/run/slack-roster/...`), the controllers'
ServiceAccount subjects the policy lists (`<release>-github-roster`), and the
telemetry service names (`access-issuer`, `github-roster`, `slack-roster`), so
a dashboard that selects on them still does.

**Steps, for an installation.**

1. Be on the configuration-file form first ([below](#migrating-from-environment-variables)):
   the environment variables went in v1.52.4, and the binaries refuse them.
2. In your values, rename `githubRoster` to `controllerGithub` and `slackRoster`
   to `controllerSlack`, and delete `githubRoster.image` and `slackRoster.image`.
   Move nothing else: `config` stays where it is.
3. If the values set `image.repository`, change it to
   `ghcr.io/truvity/sluis/sluis` (or your mirror's name for it);
   if they do not, there is nothing to do. Pin or mirror one image instead of
   three.
4. Add `nameOverride: access-issuer` and `fullnameOverride: <the old full name>`
   (the Deployment's name without a suffix). `config.release` and each
   controller's `config.release` stay the old full name, as the old chart
   required. **Write them out if the values left them unset**: the default was
   `access-issuer` and is now `sluis`, so an installation that relied on
   it must now say `release: access-issuer`, or the render is refused and says
   what to write. Without the two overrides the installation is renamed
   (above).
5. Point the chart reference at `oci://ghcr.io/truvity/charts/sluis` at
   this version: a Helm release, an Argo CD `Application`'s `chart:`, a
   `HelmRelease`. Run `helm template` first: a misspelt key or a `release` that
   is not the full name is refused at render, with the value to write.
6. Roll out. The pods restart onto the one image; the old images and the old
   chart are no longer published, so a pin on one of them stops resolving at the
   next pull.
7. Anything outside the chart that ran `access-issuer`, `github-roster` or
   `slack-roster` by name (a `docker run`, a Compose file, a systemd unit) runs
   `sluis serve`, `sluis controller github` or `sluis
   controller slack` with the same `--config` file.

## Migrating from environment variables

Everything the subcommands read from the environment is a key of a document, and the
chart's flat values that fed them are now the component's `config`. An old
variable that is still set is **refused at start** with the key that replaces
it; nothing is ignored. The platform-supplied `NAMESPACE` (now read from the
pod's mounted service-account namespace) and `POD_NAME` (now the pod's
hostname, which is the pod's name) are not configuration and the chart no
longer sets them.

### The service

| Old variable | Now, in the `serve` document (the policy document where said) |
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
| `POLICY_DIR` | `policy.file`, the one rendered [policy document](#the-policy-document) |
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

The v1.62 and earlier `controller-github` and `controller-slack` documents' keys, which are
the `controllers.github` and `controllers.slack` sections of the service document since v1.63
(the keys other than `consoleURL`, `tokenFile`, `appsDir`, `credentialsDir`, `recordsDir`,
`interval` and `console` are the service document's own):

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

Retired for every role in v1.62.0, with the configuration layer and the
`secrets` source: a function that still sets one stops at start, naming the v1.62
Pulumi library to deploy with, because the binary and the library move together
([AWS Lambda](../integrations/aws-lambda.md#version-coupling)).

| Old variable | Now |
|---|---|
| `SLUIS_CONFIG_FILE` | `SLUIS_CONFIG`, which names the service document: `/opt/sluis/sluis.yaml` in the configuration layer |
| `SLUIS_SECRET_FILES` | the document's `secrets` source (`ssm`, root `/sluis/<instance>`) and the names its keys give: `signingKey.kms.stateSecret`, `recovery.passwordSecret` |
| `SLUIS_ROLE` | nothing: there is one function, which serves the issuer and the console and runs the controllers' passes (deploy with the v1.63 Pulumi library) |
| any variable set to `ssm:<path>` | the same: a secret is named in the document and read by its `secrets` source |

### Retired keys

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

### The chart's values

| Old value | Now |
|---|---|
| `issuerURL` | `config.issuerURL` |
| `listeners.port`, `listeners.healthPort` | `config.listen.address`, `config.probes.address` |
| `logLevel` | `config.log.level` |
| `groupsScoping`, `cluster` | `config.groupsScoping`, `config.cluster` |
| `lifetimes.*` | `config.lifetimes.*` |
| `directory.store` | `config.store` |
| `directory.freshness.*`, `directory.sessionLifetime`, `directory.login` | `config.freshness.*`, `config.lifetimes.session`, `config.login.directory` |
| `recovery.enabled`, `.serviceAccountName`, `.audience` | `config.recovery.enabled`, `.serviceAccount`, `.audience`, with `config.inCluster: true` |
| `exchange.audience` | `config.exchange.audience` |
| `valkey.address`, `.tls`, `.cluster` | `config.valkey.address`, `.tls`, `.cluster` |
| `valkey.passwordSecret.name` / `.key` | `config.valkey.passwordSecret: valkey/password` and a `secrets` entry `{name: valkey/password, secretName, key}` |
| `oauthClient.secret.name` / `.keys.*` | `config.oauthClient.provider` and `secrets` entries `providers/google/<provider>/client-id` and `client-secret` (`config.oauthClient.{secretName,idKey,secretKey}` name the Secret the console shows) |
| `github.owners` | `policy.exchange.github.owners` |
| `githubRunnerApps.tiers` | `policy.apps.github.runnerTiers` |
| `console.origin`, `console.client` | `config.console.origin`, `config.console.client` (`console.mount` stays: it is the route's) |
| `signingKey.rotation.*` | `config.signingKey.pollInterval`, `.activationDelay`, `.overlap` |
| `audit.writer`, `.query`, `.audience`, `.forwardedForTrustedHops` | `config.audit.writer`, `.queryURL`, `.audience`, `.forwardedForTrustedHops` (`audit.token.*` stays: it is the chart's) |
| `controllerGithub.interval`, `controllerGithub.actsIn` | `config.controllers.github.interval`, `policy.controllers.github.enabledOrgs` (v1.62's `controllerGithub.config.interval`) |
| `controllerSlack.interval`, `controllerSlack.actsIn` | `config.controllers.slack.interval`, `policy.controllers.slack.enabledWorkspaces` (v1.62's `controllerSlack.config.interval`) |
| `controllerGithub.*`, `controllerSlack.*` (v1.62) | removed in v1.63: [`config.controllers`](#controllers-the-github-and-slack-controllers) |
| `telemetry.otlpEndpoint` | removed: `OTEL_*` is set on the pods by the platform |

The old values are removed, not aliased: an old key is refused at render
(`values.schema.json` names it), so a values file that was not migrated fails
`helm template` and not a rollout. The paths the config names are where the
chart mounts what it renders, and the chart refuses a config that names another:
the defaults in `values.yaml` carry them, so a values file states only what it
changes.

## Roles

Two roles, held by membership of two declared internal groups, and the same two
scoped to one directory.

| Role | Group | May |
|---|---|---|
| viewer | `all:access-roster:viewer` | every read: `ListWorkspaces`, `GetSettings`, `GetPolicy`, `WhoAmI`, `Explain`, `ListDirectoryGroups`, `GetDirectoryGroup`, `SearchPeople`, `ListHolders`, `GetGitHubStatus`, `ListGitHubApps`, `GetGitHubApp`, `GetSlackStatus`, `ListSlackApps`, `ListSlackChannels`, `ListSlackSharedChannels`, `ResolveDirectoryGroups`, `ListServedDomains` (a scoped viewer sees only what its directory owns) for anybody else (one's own reach, like `Explain` of oneself, needs no role), and listing one's own sessions — the whole console, read-only |
| operator | `all:access-roster:operator` | everything: Connect, Reconnect, UploadKey, `SetServedDomains`, `SetSyncedGroups`, Probe, Refresh, Disconnect, the GitHub connects and disconnects, `ListGitHubAppTokens` (a request names who asked), `ConfirmGitHubRemovals`, `ImportGitHubLinks`, `BeginSlackWorkspaceConnect`, `RequestSlackPass`, `DisconnectSlackWorkspace`, `ConfirmSlackRemovals`, `CreateSlackApp`, `InstallSlackApp`, the Slack channel and Slack Connect create, update and delete, `ChangeSlackWorkspaceOwner` and `ChangeGitHubOrganisationOwner` (installation-wide operator only), listing and revoking anyone's sessions |

**Per-directory operators.** Beside the two installation-wide groups,
`<directory-workspace-id>:access-roster:viewer` and `:operator` give the same
roles over what that directory owns: the Slack workspaces and GitHub
organisations connected with it as owner, their Apps and the channels in them.
The owner is recorded at connect (the installation-wide operator may name any
connected directory or none; an operator of one directory owns what it
connects; an operator of several chooses among theirs) and only the
installation-wide operator changes it (*Change owner*). A workspace or
organisation with no owner is operated by the installation-wide operator alone.

Behind a gateway that forwards a token, the forwarded identity's email is
resolved through the directory like any other; the groups in the token
itself are not consulted, because the directory is the source they came
from.

## The repository

sluis ships from one repository: the `sluis` chart with
its one image — `serve` runs the controllers; `controller github` and `controller slack` are deprecated subcommands, removed in the next release — `sluisctl`, the GitHub Action, the Go module and the TypeScript
package, all stamped with one tag. Shared Go packages —
the backends, the policy engine, the verifiers, the exchange — are
importable behind interfaces.

## Valkey: a recommendation

Any Valkey or Redis-protocol server reachable from the namespace works.
The service stores one key set per workspace (the snapshot, its timestamp, a
short negative cache) and a lock per workspace; memory use is the size of
the directories, tens of megabytes at most. Persistence is not required —
a cold cache costs one fetch per workspace.

If the cluster runs the [valkey-operator](https://github.com/hyperspike/valkey-operator),
a single-node cluster in the service's namespace is enough:

```yaml
apiVersion: hyperspike.io/v1
kind: Valkey
metadata:
  name: directory-roster-cache
  namespace: directory-roster
spec:
  nodes: 1
  replicas: 0
  tls: false
```

Point the chart at its Service (`config.valkey.address: directory-roster-cache.directory-roster.svc:6379`)
and, if the operator issues a password Secret, name it: `config.valkey.passwordSecret: valkey/password` and a `secrets` entry
`{name: valkey/password, secretName: <that Secret>, key: <its key>}`.

## The gateway: a recommendation

The console listener must only be reachable through an authenticating
gateway that forwards the caller's identity and groups as headers. The
service ships an `HTTPRoute` (console slice) attaching to a Gateway the
deployment names; the authentication layer (an OIDC-aware proxy or the
gateway's own OIDC filter) is the deployment's. Set
`networkPolicy.gatewayNamespace` so nothing else can reach the port.

## Delivering a declared Secret with external-secrets: an example

Not a dependency of this service — one way a deployment can put a
service-account key into the namespace for a declared workspace.

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: example-sa-key
  namespace: directory-roster
spec:
  refreshInterval: 1h
  secretStoreRef:
    kind: ClusterSecretStore
    name: your-store
  target:
    name: example-sa-key
    creationPolicy: Owner
  data:
    - secretKey: key.json
      remoteRef:
        key: /path/in/your/store/example-sa-key-json
```

## Signing with AWS KMS

`signingKey.kms` keeps the issuer's signing key in KMS, so the estate's master
key is never in a pod, a Secret or a backup. One key per estate, created as
`ECC_NIST_P384`, usage `SIGN_VERIFY`, alias like `sluis-signing`.

The role the service runs as (Pod Identity, IRSA) needs exactly this on each
key listed in `signingKey.kms.keys` AND in every `signingKey.kms.additional[].keys`
(the ES384 key and the RS256 key):

```json
{
  "Effect": "Allow",
  "Action": ["kms:Sign", "kms:GetPublicKey"],
  "Resource": ["<the ARN of each key in signingKey.kms.keys>"]
}
```

For an alias, grant on the key it points at (an alias does not match a
key policy `Resource`; use a `kms:ResourceAliases` condition if the grant must
follow the alias). Without `kms:GetPublicKey` the service refuses to start and
the log line names the permission and the key; without `kms:Sign` it starts
and fails every token, counted in `access_issuer.kms_signatures{result="error"}` (or `throttled`).
`access_issuer.kms_signatures` counts every `kms:Sign` by the signing key's
`kid` and `result` (`ok`, `throttled` or `error`). Every signature is also in CloudTrail.

Every token is one `kms:Sign` call, so the account's KMS request quota for
asymmetric `Sign` (a regional, adjustable quota shared by everything in the
account that signs) is the ceiling on token throughput; `result="throttled"`
on the metric says it is being hit. Request a raise before it is.

Rotation is appending a new key to `keys`; see the table above. List order is
age: only the last key is ever newly adopted, and a key that has retired stays
retired while it is still listed (remove it from the list when convenient). At start only the last key is newly adopted; earlier ones are published only if the installation already knows them.
