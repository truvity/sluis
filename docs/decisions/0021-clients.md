# 0021 — Clients: installation clients are config, tenant clients are durable, and a self-described client is admitted only from allow-listed hosts

**Status:** Accepted; partly superseded by 0051
**Date:** 2026-11-21

Proposal: [issue #789](https://github.com/truvity/sluis/issues/789).

## Context

A client is authority: it decides which redirects receive codes, which grants a token carries and how long a session lasts. So the question is who writes a client.

The installation's own clients are config that a reviewer sees. A tenant's clients cannot be: a customer that arrives at run time cannot wait for a pull request, and a tenant never appears in config ([0004](/decisions/0004-the-data-model/)). The pattern is the tenant-adjustable profile: config declares a template with bounds, and a tenant admin, or a product for its tenant, makes an instance inside them.

The other forces:

- A document that anyone can publish must not widen anything.
- sluis takes the core of a standard before its extensions.
- A token has one audience, a registered client id; a resource indicator selects a client and is never copied into `aud` ([0017](/decisions/0017-identity-and-sign-in/)).
- The MCP authorization specification names the pieces an agent client relies on. Its current revision, `2026-07-28`, makes Client ID Metadata Documents a SHOULD for authorization servers and dynamic registration a deprecated MAY. It requires resource servers to publish RFC 9728 metadata and clients to send `resource` on every request.

## Decision

### The client declaration

- **Installation clients are config.** They are the config kind `InstallationClient`, declared, reviewed and delivered read-only as a signed document; infrastructure code writes it. Built.
- **Tenant clients are durable.** A client that a tenant admin, or a product for its tenant, registers is the durable kind `TenantClient`, written by `exchange` after policy has decided ([0007](/decisions/0007-policy-and-sealed-grants/)). It is an instance under a template: the config kind `ClientTemplate` says which slots an instance may fill (redirect hosts, profiles such as FAPI 2.0, rates) and the bound on each, and the store refuses an out-of-bound write ([0005](/decisions/0005-the-store/)). An instance holds no authority the template did not carry: no new grant name, no claim, no logic. A tenant may lower the rate limits of its own clients. Planned: both kinds are in the registry and have no writer yet.
- **A client has exactly one of three kinds:** `public`, `confidential`, `exchange`. A `public` client has a `profile`: `browser` (exact `https` redirects, with a host of ASCII letters, digits, `-` and `.`, so a Unicode name is written in its `xn--` form) or `native` (the loopback redirects of RFC 8252 with any port, or a claimed `https` redirect). The kind fixes the defaults that belong together (PKCE, the allowed grants, the client authentication method), and the loader refuses a declaration that mixes them, a `profile` on a client that is not public, any profile word but these two, a `confidential` client without a `secret`, a `public` client with one, and a `requires` that is empty or missing. A public client's profile defaults to `browser`. Built.
- **There is no `client_credentials` grant.** A machine uses token exchange with a workload proof ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).
- **The built fields** are `kind`, `redirects`, `secret`, `requires`, `ttl_cap`, `profile`, and:
  - `signing_alg`, empty (ES384) or `RS256`, for a relying party that cannot take the default ([0006](/decisions/0006-keys-and-sealing/)). It is the algorithm of the client's **ID tokens**; access tokens stay ES384. An exchange client has none, and `RS256` needs the key plan to list the RS256 purpose.
  - `pkce`, empty (`required`) or `nonce`. A `confidential` client that names its secret may say `nonce`: an authorization request may then carry no PKCE challenge if it carries a `nonce`, and the client must check that `nonce` in the ID token before it uses any token (RFC 9700 section 2.1.1). sluis cannot check this, so it is a setting of signed config that `sluis config explain` prints and that every request using it makes an audit fact name. `config check` refuses it for any other kind. A request that sends a `code_challenge` is checked as a PKCE request whatever the setting says, and a `code_verifier` for a code issued without a challenge is refused (RFC 9700 section 4.8.2). The shared vectors are `kits/conformance/token/client-vectors.json`.
  - `groups` and `groups_delimiter`, which shape the groups of the client's tokens ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)). They are the client's declaration; no tenant client holds them.
- **The fields still to come** are `display_name` and `description` (shown on the sign-in and consent pages), `post_logout_redirects`, `backchannel_logout_uri` and `harness` for the conformance harness client. `frontchannel_logout_uri` and `initiate_login_uri` are refused. Planned.
- **The login domain of a client is a reference** to an entry of the config kind `FrontHost`, which holds the domain and its relying-party ID. A domain needs DNS and a certificate, so it is fixed and shared; a tenant's clients use the entry that the `Tenant` item names, its issuer host. The kind is built; the reference is planned.
- **The class of a session comes from policy, not from the client** ([0018](/decisions/0018-sign-in-sessions/)).
- **A confidential client authenticates at the token endpoint and at revocation with `client_secret_basic` (each part form-URL-decoded) or `client_secret_post`, and either is accepted for any confidential client.** The certification plan of the OpenID Foundation runs both against one static client. Every failure is the same `invalid_client`, whether the client is unknown, public, an exchange client or confidential. The secret of an installation client is the one that infrastructure code provides as `InstallationClientSecret`, read from the secret source. `private_key_jwt` is planned with the FAPI 2.0 profile ([0038](/decisions/0038-fapi-2-profile/)).
- **Changing the kind of a client does not end its sessions.** A chain records the client, not how it authenticated, so after a change from `confidential` to `public` the refresh tokens work with the client id alone. To end the chains, give the client a new id and remove the old declaration; its next request is `invalid_client`.
- **A spent code is the evidence of its owner only.** A code presented again ends the session chain it issued when the client that presents it is the client it was issued to; another client is refused and ends nothing.
- **A generated secret is 32 random bytes.** `exchange` stores its hash as the durable kind `ClientSecret`, one item per version, and `signin` verifies it at the token endpoint. The current and the previous version both authenticate during an overlap that the operator sets at rotation, up to a maximum that config declares, compared in constant time. The value is shown once, to the operator or the product that generated or rotated it. A version is authoritative durable data, backed up as its hash and rolled forward after a restore ([0010](/decisions/0010-backup-and-recovery/)). Planned; an installation client's secret comes from the secret source.
- **A secret that a consumer reads lives in KV.** A client marked for a consumer has a KV entry `{client_id, client_secret}` under a managed prefix that only `exchange` writes, and the consumer reads it under policy ([0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)). There is no sealed export copy; a secret that no consumer reads keeps only its hash. Planned.

### Resources

Planned.

- **A resource is declared with the installation's clients:** `resources` maps an absolute `https` URI, without a fragment, to `client` (the registered client whose id is the token's `aud`), `requires`, `ttl_cap` and `display_name`.
- **A request names at most one resource** with the `resource` parameter, in the authorization and the token request. Several, a relative URI or a fragment are refused, and so is an undeclared resource (`invalid_target`). The URI is matched literally.
- **Two gates apply:** the caller must satisfy the client's `requires` and the resource's, and the shorter cap wins. A refusal for an entitlement shows the generic message and puts the reason in the log only.
- **The session chain records the resource, and every refresh checks it again** ([0018](/decisions/0018-sign-in-sessions/)). A long-running caller is an agent-class session, not a resource with a longer session.

### Self-described clients

A client may present an `https` URL as its `client_id`; sluis then fetches its metadata document. Planned.

- **It is off until a host is named.** The config kind `ClientDocumentGate` has `hosts` (host names, no scheme, path or wildcard), `requires` (mandatory with `hosts`: the one gate for every such client), and optionally `ttl_cap` and `class`. Discovery advertises `client_id_metadata_document_supported` only while a host is named. A declared client with the same id always wins.
- **The host is decided before any connection.** `signin` makes the fetch with the egress policy that checks addresses after name resolution and refuses loopback, link-local and private ones. It follows no redirect, accepts at most 64 KiB, allows 10 seconds per attempt with one retry, and is limited to 30 fetches a minute per host. A failure is remembered for a minute.
- **A document client is always public.** The document's `client_id` must equal its URL, it must have `redirect_uris`, its `token_endpoint_auth_method` must be `none`, and a secret in it is refused.
- **The document grants nothing.** Its `kind`, `requires`, `ttl_cap` and any session class are ignored; the gate, the cap and the class come from `ClientDocumentGate`.
- **The fetched document is the temp kind `ClientDocument`,** written by `signin`: valid 10 minutes, and the last good copy may be used for one hour past expiry, for transport failures only. A refused answer (invalid document, redirect, 4xx, too large) is never served stale.
- **The consent and sign-in pages show the client's cleaned display name and its host,** and the audit record names the URL.
- **Dynamic client registration is not offered.**

### MCP authorization

Planned.

- **sluis implements one revision of the MCP authorization specification at a time and pins it:** `2026-07-28`. Moving the pin is a change to this record and to the conformance vectors in one pull request.
- **sluis is the authorization server only.** It publishes RFC 8414 and OpenID Connect discovery, advertises `authorization_response_iss_parameter_supported` and sends `iss` on every redirect, requires PKCE S256, accepts metadata-document clients and `resource`, and issues agent-class sessions ([0018](/decisions/0018-sign-in-sessions/)) for them.
- **A resource server publishes its own RFC 9728 metadata,** naming sluis as the authorization server. The declared resource carries what is needed to render it (the URI, the issuer, the supported scopes), so the Go and TypeScript libraries and the sidecar write the same document from one declaration.
- **A resource server checks that `aud` is the registered client of its resource,** not its URL. A stock MCP server that compares `aud` with its own URL is fronted by the sidecar.

### The resource-proxy sidecar

Planned.

- **It is a release image beside the server and not a sluis actor:** no state, no key, no entry in the actor registry. It is configured with the issuer, the resource URI and the upstream.
- **It verifies the token** (signature against the issuer's key set, `iss`, `exp`, and `aud` equal to the resource's client), **serves the RFC 9728 metadata without authentication, strips the caller's `Authorization` header before it forwards,** and writes one structured log line per request ([0016](/decisions/0016-telemetry/)). It reads no group.
- **An optional outbound forwarder** adds the workload's own token, obtained by token exchange with the pod's workload proof. Inconsistent outbound settings are refused at start.

## Consequences

- An MCP client that sends a resource and a metadata-document client id works against sluis with no registration step. The cost is the fetcher: an outbound request to a URL that a stranger names, bounded by the host list, the egress policy and the limits above.
- A self-described client is only as trusted as the group its gate admits. An operator who allows a host admits everyone who can publish there.
- A tenant's client is made at run time and never waits for a review, yet cannot step outside its template. The cost is a template to write and keep, and a bound for every slot a tenant may fill.
- A generated secret that a consumer reads has two homes, a hash to verify and a KV entry to read.
- Because `aud` is a client id and not a URL, an MCP server written against the plain specification needs the library or the sidecar. That is the price of one audience rule.

## Roads not taken

- **A client authentication method registered per client.** Stricter, but the certification plan runs both methods against the same client.
- **A boolean `skip_pkce`.** It says what is dropped, not what takes its place, so a review would not see that the `nonce` then holds a code to its client; and it cannot grow.
- **A request parameter, or a client document, that turns the `nonce` option on.** The caller would decide whether the caller is bound.
- **Generate and hash the secret of an installation client.** Its secret has a consumer in the secret source that infrastructure code writes; sluis generates secrets only for clients registered at run time.
- **Dynamic client registration.** An endpoint that mints trust for anyone; the specification treats it as deprecated.
- **The resource URL as `aud`.** It would break the one-audience rule and tie a token to a URL that the client typed.
- **A document that sets the session class, the gate or the lifetime.** Anyone who publishes a document could give their client an agent session of a month.
- **Redirects or private addresses in the fetch.** Both are the classic route from a client URL to an internal service.
- **sluis hosting the metadata of every resource.** It would make sluis a registry of other products' endpoints.
- **Following the newest MCP revision automatically.** A library or a server would change behaviour between two releases with no test that says so.
- **The sidecar as an audit emitter.** It would need an audit identity and a transport; a log line serves the one consumer that exists.
- **A tenant's clients as config committed by the operator.** Every customer client would wait for a review.
- **A template that lets an instance add a grant name or a claim.** The instance would carry authority that no reviewer saw.
- **The login domain as a field of each client.** A tenant client could then name a domain that config never provisioned.
- **A sealed export copy of each generated secret.** Another copy and another credential in the exporter, while the consumer can read the original from KV under policy.

## Follow-ups

- Build: client model [#789](https://github.com/truvity/sluis/issues/789), generated secrets and rotation [#790](https://github.com/truvity/sluis/issues/790), resources [#791](https://github.com/truvity/sluis/issues/791), metadata documents [#792](https://github.com/truvity/sluis/issues/792), MCP authorization [#822](https://github.com/truvity/sluis/issues/822), the sidecar [#823](https://github.com/truvity/sluis/issues/823). The order is on the [roadmap](/roadmap/).
- The registry still lists the sealed export kind `ClientSecretExport`; it is removed with the managed prefix of `exchange` ([#798](https://github.com/truvity/sluis/issues/798)).
- Open: resources as policy entities. No issue yet; one is opened when a consumer needs it.
