# 0025 — Integrations are adapters, work actors that return facts or carry out authorised effects, hold nothing at rest, and mint only for declared targets

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #997](https://github.com/truvity/sluis/issues/997).

## Context

[0002](/decisions/0002-families-of-actors/) says that the work actor of an integration is an adapter, named `adapter-<vendor>@<boundary>`, that it holds no credential at rest, and that it returns facts or carries out effects that core has authorised. It does not say what an adapter may return, how core authorises an effect before it runs, what each kind of integration is called, how a tenant's connection reaches an adapter, or for which targets a credential may be minted at all. Every integration is written against this contract.

The forces are the ones that made `policy` a separate actor ([0007](/decisions/0007-policy-and-sealed-grants/)).

- An adapter speaks a third party's protocol and uses a third party's credential. If it also decides who gets what, the decision hides in vendor code that no policy file shows.
- Vendor error text, vendor identifiers and vendor structs in core would tie core to each vendor.
- An adapter that served several companies from credentials of its own would let one company's compromise reach another's. An adapter that keeps nothing at rest yields only the credential it is using at that moment ([principle 16](/principles/)).
- A minter is a place where sluis holds standing power at a vendor, so each one needs a reason. The only entitlement sluis can state with confidence is one it derives from its own groups and proofs. A vendor that can trust sluis as an identity provider needs no minter at all. The credential that creates tokens at a vendor is the sharpest one in its integration.

## Decision

### The rule

- **An integration returns facts, or carries out an effect that core has authorised, and never decides.** Whether a fact entitles anyone to anything is the question of `policy`. A refusal by an adapter means "the vendor said no" or "this is outside what core authorised", never "this person may not".
- **An integration is implemented by an adapter.** An adapter serves one vendor, implements one or more of the contracts below for it, and opens each tenant's connection credential at key@ for each call ([Adapters and connections](/decisions/0025-integrations/#adapters-and-connections)).
- **Core calls adapters, and an adapter calls no work actor other than `policy`.** `signin` calls its upstream adapter, `exchange` its minters and notifiers, and `console` the adapters whose flows it starts or whose kinds it routes writes to. `entry` only routes a push notification to an adapter. A provisioner reads its desired state as data through its data port. The one call an adapter makes is to `policy`, for the grant of work that no caller started and, for a directory adapter, for the members of the targets that its directory feeds. Writing what `policy` answered is executing, not deciding ([0026](/decisions/0026-reconcilers/#the-desired-state)).
- **Three invariants are tested:** core packages never import vendor packages; the actor registry has no call from an adapter to a work actor other than `policy`; policy matchers have no vendor-specific fields.

### The contracts

Every contract is a ConnectRPC service in the package `sluis.integration.v2alpha1` ([Contracts and errors](/reference/contracts/)).

| Contract | Kind | Methods | Returns | State |
|---|---|---|---|---|
| `Upstream` | sign-in | `Begin`, `Complete` | An authorization URL; then an assertion of identity | built |
| `Integration` | base, served by every adapter | `Describe`, `Probe` | Its kinds and version, the attributes it can return with their types, and its health | planned |
| `FactsSource` | facts | `Lookup`, `Changes` | Facts about named subjects; the subjects whose facts changed since a cursor | planned |
| `CredentialMinter` | effect | `Mint` | A short-lived credential for a sealed grant | planned |
| `Provisioner` | effect | `Reconcile` | The outcome of making an external system equal a desired state | planned |
| `Notifier` | effect | `Send` | The outcome of sending one message | planned |

A contract is written with its first consumer, as an addition to the package; adding a service or a method is compatible. The workload verifier is not an adapter but a verifier driver of `exchange`, which holds public material only ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).

### Adapters and connections

- **An adapter serves one vendor.** The adapters of the actor registry are `adapter-upstream-oidc` (the `Upstream` contract, and the directories of Google Workspace and Entra), `adapter-github` (a GitHub organisation's directory, minting and teams) and `adapter-slack` (chat channels and notifications). A provider for which sluis mints is served by the adapter of that provider. An adapter is built with the first consumer of its contract.
- **One adapter runs for a vendor in a boundary, and connections tell the tenants apart.** `adapter-github@prod` serves every tenant of `prod` that has connected a GitHub organisation. Two tenants' connections to the same vendor are two items of one kind under two tenants, not two instances.
- **A connection is a tenant's own, durable and sealed.** A tenant's directory, chat workspace, code host or account at a provider that sluis mints for is a durable kind of that tenant with its credential sealed, made through the console or the API. The adapter that completes the connect flow writes the connection, under ids that start with its own name ([0004](/decisions/0004-the-data-model/)).
- **Which kinds an adapter uses.** `Upstream` and `UpstreamSecret` are in use; the others are declared in the kind registry and used when their adapter is built.

  | Adapter | App definition, installation-wide (config, secret) | Connection of a tenant (durable, sealed credential) | Flow in progress (temp) |
  |---|---|---|---|
  | `adapter-upstream-oidc` | `Upstream`, `UpstreamSecret` | `EntraConnection`, `DirectoryConnection` | `EntraConsentState` |
  | `adapter-github` | none | `GitHubApp`, `GitHubInstallation`, `DirectoryConnection` | `GitHubManifestState` |
  | `adapter-slack` | `SlackApp`, `SlackAppSecret` | `SlackConnection` | `SlackInstallState` |
  | A provider's adapter, for minting | `MinterBounds` | `MinterConnection`, `MinterTarget` | none |

- **`Integration` and `IntegrationSecret` define an integration made by hand or by infrastructure code:** its vendor, its capability and the hosts its adapter may reach. They serve the whole installation, hold no connection of a tenant, and are read by the adapter named in their id.
- **An adapter opens a connection's credential per call.** It reads the connection through its data port, asks key@ to `Open` the sealed credential with a grant sealed for the connection's tenant ([0006](/decisions/0006-keys-and-sealing/)), makes the call to the vendor, and keeps nothing. A pass that no caller started, such as a scheduled re-sync or a reconcile pass, has no grant to bring, so the adapter asks `policy` for one for that pass, one tenant at a time. The grant is short-lived like any other. Deleting a connection ends the credential, and a tenant that leaves takes its connections with it ([0008](/decisions/0008-boundaries-and-tenants/)).
- **A credential that serves the whole installation stays in a secret source.** The client secret of an `Upstream`, a `SlackAppSecret` and an `IntegrationSecret` are read by the adapter named in the registry from the secret source, for one call at a time, so the adapter holds no credential file of its own ([0005](/decisions/0005-the-store/), REQ-ISO-106).
- **Infrastructure code may create a connection through the API.** What it writes lands in a durable kind of the tenant, not in config ([principle 17](/principles/)). The company that runs the installation is a tenant, so its own connections are durable too.

### What an integration may return

- **A fact is a subject reference, named attributes, the time observed (`as_of`), how long it may be used (`max_age`) and whether the source is authoritative.** A fact past its `max_age` is stale, and `policy` is told so; staleness is never repaired by guessing.
- **References are opaque.** A subject or a group is a `{namespace, id}` pair, and core never parses the id. Attributes carry namespaced names (`github.repository`, `k8s.namespace`), declared with their types in `Describe`, so a policy file is validated at load without core knowing the vendor.
- **A directory's facts are temp kinds of the tenant** (`DirectoryUser`, `DirectoryGroup` and `DirectoryMembership`), written by the adapter and sealed because they hold names and addresses. Their time to live is the fact's maximum age, 24 hours by default and 7 days at most, and a re-sync rebuilds them. On boards every write is a commit of the cluster. A full re-sync of 100,000 users was measured on 2026-11-03 at 260 s and 4,100 commits, and it raised the p99 of a sign-in write from 37 to 209 ms. Clearing a directory first stalls writes for seconds, so a re-sync replaces items and does not clear first ([0037](/decisions/0037-capacity-is-measured-before-it-is-promised/)).
- **The sign-in assertion is a fact about one authentication.** `Upstream.Complete` returns the issuer, the name and value of the upstream's subject claim ([0019](/decisions/0019-person-and-upstream-link/)), and `acr`, `amr` and `auth_time` exactly as the upstream sent them, plus an organisation claim where one is configured. An absent value is unknown, never a default. Groups and roles that the upstream sends are not read.
- **A refusal carries a reason from a closed list:** `UNAVAILABLE`, `UPSTREAM_REJECTED`, `INVALID`, `NOT_FOUND`, `RATE_LIMITED`, `UNSUPPORTED`, `STALE`, each with one Connect code ([Contracts and errors](/reference/contracts/)). No vendor error text crosses the contract.
- **"I could not ask" is `UNAVAILABLE`, never an empty answer.** A directory that cannot be read must not look like a directory with no members.

### The upstream contract

This is the built contract. Its caller is `signin`, which keeps the state of a sign-in and decides; the adapter keeps none.

- **`Begin`** takes the name of the `Upstream`, sluis's callback URL, the `state`, the `nonce` and the S256 `code_challenge` that `signin` made, and optionally `prompt`, `max_age` and `acr_values`, and returns the authorization URL. `prompt` is `none` for a silent renewal or `login` for a fresh authentication, passed on as the client gave it. A login hint and a locale are planned additions.
- **`signin` asks the upstream for what the client asked of the authentication, and holds the upstream to it.** A request for no interaction is a silent request to the upstream. `prompt=login` and `max_age` (zero included, which is `login`) are passed on in `Begin`. At the callback, `max_age` is checked against the `auth_time` of the assertion. `prompt=login` and `max_age=0` must show an `auth_time` within ten seconds of the request on either side, an allowance for the clocks. An upstream that answered from its own single sign-on session is not believed, nor is one whose clock puts the authentication in the future, and an upstream that sends no `auth_time` cannot show it and is refused. The audit record says `fresh` for this refusal and `max_age` for an authentication older than `max_age`.
- **The four errors of OpenID Connect Core section 3.1.2.6** (`login_required`, `interaction_required`, `consent_required` and `account_selection_required`) go back to the client when it asked for `prompt=none`, each as its own audit reason, and are `access_denied` otherwise. A valid request for `prompt=none` is answered with a redirect and never with a page, but its callback can still land on the fixed error page: the sign-in cookie is `SameSite=Lax` and holds one sign-in, so a silent renewal in a cross-site frame, or two at once in one browser, can arrive without the cookie it needs. `consent` and `select_account` are answered at once with `consent_required` and `account_selection_required`, because sluis shows no consent and no account chooser. `none` combined with another value, a repeated value and an unknown value are `invalid_request`.
- **An `id_token_hint` holds the sign-in to its person.** The hint must be shaped like an ID token of this issuer for the client (a JSON header, and claims with `iss`, `aud` and `sub`). Its signature and expiry are not checked, because it is only ever used to refuse: `signin` keeps its `sub` in the pending sign-in and signs the person in only if the person's own subject, made as a token's is, equals it. Another person, or one new to sluis, gets `login_required` and nothing is made for them. An expired hint is accepted (OpenID Connect Core section 3.1.2.1). Ignoring the hint would answer for whoever signs in. Any use that selects a session or a person from a hint, RP-initiated logout ([#779](https://github.com/truvity/sluis/issues/779)) for one, must verify its signature against the key set first.
- **The pending sign-in is a cookie, and nothing is stored before a person is known.** `signin` seals the `PendingSignIn` message (the client's request, the state, nonce and PKCE values sent upstream, and the tenant) into the cookie `__Host-sluis_signin`, under the boundary's `cookie` data key ([0006](/decisions/0006-keys-and-sealing/)). The cookie is valid for 10 minutes, is bound to the request that began the sign-in, and is at most 4 KiB ([REQ-SESS-047](/requirements/sess/#REQ-SESS-047) to [REQ-SESS-050](/requirements/sess/#REQ-SESS-050)). A grant for a tenant's keys exists only once a person is decided, so a sealed item could not be written earlier, and an unauthenticated request must not create stored state ([0036](/decisions/0036-volumetric-abuse/)). `PendingSignIn` is a message of the `signin` contract, not a kind of the registry.
- **`Complete`** takes the callback's `code` and `iss`, the `code_verifier`, the `nonce` and the callback URL. `adapter-upstream-oidc` exchanges the code at the upstream's token endpoint with the client credential, checks the ID token strictly (signature against the upstream's key set, an allow-list of algorithms, `iss`, `aud`, `azp`, `nonce` and the time claims), and returns the verified assertion. The client credential never leaves the adapter, so `signin` holds no upstream credential.
- **The credential is the operator's.** It is the `UpstreamSecret` of a secret source. A customer organisation's own provider is configured in the broker and not in sluis ([0032](/decisions/0032-customer-identity-installations/)).
- **The callback ignores response parameters it does not know,** as RFC 6749 section 4.1.2 asks of a client. The closed lists of the protocol endpoints cover requests made to sluis only ([REQ-AUTHN-012](/requirements/authn/#REQ-AUTHN-012), [REQ-AUTHN-078](/requirements/authn/#REQ-AUTHN-078)).
- **A missing subject claim, a bad token or a missing `iss` is refused with `INVALID`.** There is no lenient fallback, and no fallback to another claim or to the e-mail address.

### How core authorises an effect

- **A mint is authorised by a grant that `policy` sealed.** The grant carries the decision, the digest of the policy and the hash of the scope ([0007](/decisions/0007-policy-and-sealed-grants/)). The minter opens it at key@, which fails for a grant of another boundary or tenant, refuses a scope the grant does not cover and cannot lengthen the lifetime. `exchange` has the limits check admit the operation before it calls the minter ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)).
- **A reconciliation is authorised by a desired state that `policy` answered for and a directory adapter wrote.** The provisioner reads it by its digest through its data port; it never asks core a question and cannot choose what the state should be ([0026](/decisions/0026-reconcilers/#the-desired-state)).
- **A notification is authorised by core composing it.** The caller names a template, its parameters and a destination that configuration allows; the notifier sends that and nothing else.
- **Business limits live in core.** Caller caps, mint rates and which group may hold which role are policy and limits, checked before an adapter is called. An adapter enforces only technical limits: the vendor's API rate and maximum lifetime.
- **Every effect is reported as an outcome:** done with its expiry, refused with a reason from the closed list, or failed. A credential or a secret is never in an audit record, a log line or a span.

### Credential minters

- **A minter serves a declared target.** A target is a `MinterTarget`: a cloud role, a prototype token or a DNS zone of a tenant's account at a provider, with the lifetime asked for. It is durable and Tenant-adjustable, written by the adapter that mints it, and checked on write against `MinterBounds`, the config kind that says what the installation allows a minter to mint (the target types and the longest lifetime of each). The account itself is the tenant's `MinterConnection`, with the credential that creates tokens sealed. A request for a target that is not declared, or that lies outside the bounds, is refused with `NOT_FOUND`.
- **sluis governs the membership that entitles a caller to mint.** Only `policy` decides who may obtain a credential for a target, from the internal groups it derives from directory facts and verified proofs ([0019](/decisions/0019-person-and-upstream-link/), [0020](/decisions/0020-grant-vocabulary-and-token-claims/)). The grant `policy` seals names the group, the target and the scope, and the minter opens only that. A target whose entitlement is held outside sluis, such as a personal token that a person supplies, is not a target: the request is refused with `UNSUPPORTED`.
- **A minter never passes on, widens or records another party's credential.** What it returns is never wider than the one grant and never longer than the least of the grant, the policy cap and the vendor's maximum. It is handed to the caller and kept nowhere: no kind holds a minted credential, and no audit record, log line or span carries one ([0004](/decisions/0004-the-data-model/)). A mint is a one-shot action, so the minter leaves a `SpentGrant` ([0007](/decisions/0007-policy-and-sealed-grants/)).
- **A minter counts its caps from its own record of its mints.** The caps on live tokens per caller and per target and on mints per caller per hour are counted from a record that holds only the token identifier, a digest of the caller, the target and the expiry of each mint, never from the vendor's list (REQ-EXCH-022, REQ-EXCH-026). An unreadable record fails the mint with `UNAVAILABLE` (REQ-EXCH-023), and a periodic pass compares the record with the vendor's list and counts each correction (REQ-EXCH-024, REQ-EXCH-025).
- **The minting credential has a floor in code.** Each minter refuses a target that grants administration of tokens, billing, account settings, memberships or identity providers at its vendor. Configuration can add names to the refusal list and cannot remove any. The per-vendor lists are on the integration pages.
- **No minter where the vendor can federate.** AWS roles and Kubernetes API servers trust sluis as an OpenID Connect issuer and take its tokens ([0017](/decisions/0017-identity-and-sign-in/)); sluis does not mint an AWS key or a service-account token for them. A minter exists only for a vendor that cannot accept an external issuer for the purpose.
- **The credential of an installation-level minter stays in a secret source.** A minter whose account is not a sluis tenant, such as a messaging system's operator, is bounded by `MinterBounds` and keeps its credential as the secret kind `MinterSecret`. It is the sharpest edge of its adapter.
- **Per-vendor behaviour stays on the integration pages,** tested against a fake vendor. This record fixes only when a minter may exist and what it may return.

### Splitting by credential

- **The split follows the credential, not the process.** Sign-in and the directory of one workspace are two credentials, so two connections (a sign-in client and a `DirectoryConnection`); two tenants' connections to the same vendor are two connections. Each fails, rotates and leaves on its own, and an adapter cannot reach a connection that its grant does not name. One adapter process serves them all, because it holds none of them at rest.
- **An adapter declares the hosts it may reach and the key purposes it needs,** and the actor registry grants no more. Outbound access is limited to the declared hosts: a per-uid egress rule on boards, a network policy on Kubernetes ([0003](/decisions/0003-units-transports-and-fusion/)).
- **Each adapter audits what it does** through audit's emitter ([0002](/decisions/0002-families-of-actors/)): a facts pass with its digest and counts, a sign-in with its outcome, an effect with its outcome.

### Push, then pull

- **A push is a notification, then a pull.** A small notification reaches `entry`, which routes it to the adapter. The adapter reads the full state from the vendor's API, and a periodic full pass runs as well. A pass that changes an external system takes a lease ([0026](/decisions/0026-reconcilers/#scope)).

### Built and planned

Built: the `Upstream` contract and `adapter-upstream-oidc`, serving each `Upstream` the operator declares, and `signin`'s side of it described above ([0019](/decisions/0019-person-and-upstream-link/)). The kinds of the adapters are declared in the kind registry.

Planned: `Integration`, `FactsSource` and the three effect contracts, with `adapter-github`, `adapter-slack`, the directories of Google Workspace and Entra, and the minters. See [the roadmap](/roadmap/).

## Consequences

- A flaw in an adapter reaches the credential it is using at that moment and the facts it can return. It cannot grant, because it holds no policy. It cannot mint beyond a grant, because it opens only grants that `policy` sealed. It holds no connection at rest.
- Policy and core stay free of vendors: a new vendor is a new adapter with a `Describe` document, and no core package changes.
- A minter is added with a reason that can be read off the registry: a bound in config, a connection of a tenant, a target within the bound, a group that policy binds. Reviewers see in one place what the credential can do.
- A user who wants a token for something sluis does not govern gets a refusal, not a surprise. A vendor with no federation and no bounds that can be declared cannot be served until it has both.
- The AWS and Kubernetes rows of the parity list are relying-party recipes, not minters, and are tested as such.
- Every call to a vendor on behalf of a tenant costs an open at key@, and a pass over many tenants costs many. The cost is batched and measured with the other hops ([0037](/decisions/0037-capacity-is-measured-before-it-is-promised/)).
- The cost is a typed contract per kind to keep compatible. Small machines rely on the fusion rule of [0003](/decisions/0003-units-transports-and-fusion/).
- An unreachable directory is `UNAVAILABLE`, so an outage is felt as refusals after the freshness window, not as everyone silently losing access ([0017](/decisions/0017-identity-and-sign-in/)).
- The requirement records stay with their areas: the upstream's checks are REQ-FED-001 to REQ-FED-016, and the minters' are REQ-EXCH-013 to REQ-EXCH-015.

## Roads not taken

- **One generic `Do(request)` call for every integration.** One interface, but no typed contract to test, no schema to validate a policy against, and every caller parses vendor-shaped bytes.
- **Integrations that return a decision, such as "allow".** A second policy engine inside vendor code, invisible to the policy file and its reviewers.
- **One process per vendor tenant and credential, each keeping its credential.** A company's connector would fail and leave alone, but every tenant adds a unit, which small boards cannot carry, and the credential would sit at rest in each process. A connection opened per call has the same separation without the units.
- **An adapter that calls core for what it needs.** It adds an edge from vendor code to an actor that holds keys or state; published data that the adapter reads through its data port gives the same without the edge.
- **Vendor errors passed through.** Core would parse vendor text, and the text could leak a tenant's data into a caller's response.
- **A stream of pushed facts instead of notify-then-pull.** A forged or reordered push would change core's state directly; a pull reads the vendor's own answer.
- **The `FactsSource` and effect services written before a consumer exists.** Nothing is built without a consumer.
- **A tenant's connection as config committed by the operator.** A tenant never appears in config, and a customer's change would wait for review ([0004](/decisions/0004-the-data-model/)).
- **Mint for any target a caller names.** It makes sluis a proxy for credentials it cannot govern, and an undeclared target has no bound to limit the grant.
- **Accept a caller's own credential and exchange it.** sluis would hold or forward someone else's standing power, which is the secrets-engine role it has declined ([0009](/decisions/0009-export/)).
- **A minter for AWS and Kubernetes as well, for uniformity.** Both accept sluis as an issuer, so a minter would add a standing credential for nothing.
- **Keep what a minter returns, sealed, with rotation and a sweep.** sluis would become a store of vendor credentials, with their lifecycle to carry; the caller keeps what it is given and asks again.
- **Leave the floor to configuration.** The sharpest credential in the integration would then depend on the care of each installation.
- **Mint without bounds, from the minter's own full rights.** The grant could then not be narrower than the vendor account itself.

## Follow-ups

- Directory sync, the first `FactsSource`: [#805](https://github.com/truvity/sluis/issues/805). Its re-sync cost on boards is in [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/).
- Downstream credentials, the first `CredentialMinter`: [#1011](https://github.com/truvity/sluis/issues/1011); the first minters are GitHub installation tokens [#811](https://github.com/truvity/sluis/issues/811), Cloudflare API tokens [#816](https://github.com/truvity/sluis/issues/816) and R2 credentials [#817](https://github.com/truvity/sluis/issues/817).
- Reconcilers and the `Provisioner` contract: the build is [#1220](https://github.com/truvity/sluis/issues/1220), and the first provisioners are [#808](https://github.com/truvity/sluis/issues/808), [#812](https://github.com/truvity/sluis/issues/812) and [#813](https://github.com/truvity/sluis/issues/813).
- The generic OpenID Connect upstream: [#1106](https://github.com/truvity/sluis/issues/1106).
