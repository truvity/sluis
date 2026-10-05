# HTTP endpoints

What `sluis serve` answers on `listen.address` (default `:8080`): the issuer's own paths at the origin root and the
console under `console.mount` (default `/console`). Why a path and not a host, and why the console's route is separate:
[Configuration: the model](../explanation/configuration.md#two-routes). Each endpoint's request and response shape is in
[contracts](contracts.md).

Six things make up what the issuer serves. Three are grants, which `grant_types_supported` names exactly:
`authorization_code`, `refresh_token` and token exchange. The other three, userinfo, `end_session` and revocation, are
endpoints, and discovery advertises each in its own field.

| Path | Standard | Purpose |
|---|---|---|
| `/.well-known/openid-configuration`, `/keys` | OIDC discovery, JWKS | what relying parties read |
| `/authorize`, `/token`, `/userinfo`, `/end_session` | OIDC | login, tokens, RP-initiated logout |
| `/logout` | ours | the same sign-out for a person rather than a relying party, on GET and on POST. It needs no `id_token_hint`. The console's sign-out button points here |
| `/token` with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` | RFC 8693 | CI and workload exchange; the requested `audience` is a client, gated by its `requires` |
| `/token`, the same exchange with `requested_token_type=urn:access-roster:params:oauth:token-type:github-installation-token` and `audience=github-app:<id>` | RFC 8693 | a GitHub App installation token of a catalogue App, under its grants ([contract](contracts.md#installation-tokens-at-token)) |
| `/revoke` | RFC 7009 | revokes a refresh token; what Revoke and "sign out everywhere" call underneath |
| `/login`, `/logout`, `/signed-out` | ours | the whole of the issuer's HTML: the sign-in chooser — which names the application being signed in to from the client's declared `display_name` and `description`, and the host its redirect returns to, or says a program on this computer is asking when that redirect is loopback ([policy](policy-clients.md#what-the-sign-in-page-calls-a-client)) — the sign-out a person follows, and where a sign-out lands when the client declares no page of its own. |
| `/account` | ours | a redirect into the console's page for the signed-in person (its sessions and *sign out everywhere*). The address stays because it was linked to and bookmarked |
| `/.access/grants` | ours | the clients the caller's groups admit it to, and which group admits each — read by `sluisctl kubeconfig` and `aws-config` so a laptop writes a context per cluster and a profile per role without keeping a list that drifts from the policy. A bearer, and it discloses nothing the caller could not work out from its own token |
| `/.access/simulate` | ours | **not built**: what would this identity get, for somebody other than the caller. The console's Rules page has a simulator that answers it, and `/.access/grants` answers it for the caller's own identity |
| `SessionService` (ConnectRPC): `ListSessions{identity? \| client? \| contains?}` → `{sessions, sign_ins}`, `RevokeSessions{identity, client?, session_id?, sso?}` | ours | sessions per identity and per client, with client, how obtained, issued, expires, last refreshed; revoke per identity, per client, or one. Listing and revoking others is operator; listing and revoking your own is any signed-in identity; listing **every** session (neither identity nor client named) is operator-only, paged by `page_size`/`page_token`, and audited. `contains` reads `identity` and `client_id` as substrings and is operator-only; `RevokeSessions` has no such field. Authorized by the browser's SSO cookie on a same-origin call — which is what the console is — or by a bearer. The `console.origin` CORS gate is for a console served from another origin and stays empty on one origin, the shipped shape |
| back-channel logout | OIDC Back-Channel Logout 1.0 | opt-in per client with `backchannel_logout_uri`: a signed `logout+jwt` POSTed to every such client that signed the person in, by the `sid` it saw, when the sign-in ends |
| not served | RFC 8628 device flow, client credentials, RFC 7523 JWT bearer, RFC 7662 introspection, implicit and hybrid flows, session-management iframe, RFC 7591 dynamic client registration | the headless cases (a CI job, a workload) are token exchange; tokens are JWTs verified offline against the key set. Instead of RFC 7591, an origin named in `client_documents` is served by Client ID Metadata Documents: no endpoint, nothing stored |
