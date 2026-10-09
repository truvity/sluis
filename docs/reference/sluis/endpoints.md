# HTTP endpoints

What `sluis serve` answers on `listen.address` (default `:8080`): the issuer at the origin root and the console under `console.mount` (default `/console`). Request and response shapes: [contracts](contracts.md). Routes: [Configuration: the model](../../concepts/sluis/configuration.md#two-routes).

The issuer serves three grants, named in `grant_types_supported` (`authorization_code`, `refresh_token`, token exchange), and three endpoints that discovery advertises (userinfo, `end_session`, revocation).

| Path | Standard | Purpose |
|---|---|---|
| `/.well-known/openid-configuration`, `/.well-known/oauth-authorization-server`, `/keys` | OIDC discovery, OAuth 2.0 Authorization Server Metadata (RFC 8414), JWKS | what relying parties and MCP clients read |
| `/authorize`, `/token`, `/userinfo`, `/end_session` | OIDC | login, tokens, RP-initiated logout |
| `/logout` | ours | the same sign-out for a person rather than a relying party, on GET and on POST. It needs no `id_token_hint`. The console's sign-out button points here. When the store cannot be read, `/logout` and `/end_session` answer 503 (an HTML retry page, or JSON `temporarily_unavailable`), keep the cookie and end nothing |
| `/token` with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` | RFC 8693 | CI and workload exchange; the requested `audience` is a client, gated by its `requires` |
| `/token`, the same exchange with `requested_token_type=urn:sluis:params:oauth:token-type:github-installation-token` (or the deprecated `urn:access-roster:...` spelling, until v1.76) and `audience=github-app:<id>` | RFC 8693 | a GitHub App installation token of a catalogue App, under its grants ([contract](contracts.md#installation-tokens-at-token)) |
| `/token`, the same exchange with `requested_token_type=urn:sluis:params:oauth:token-type:cloudflare-token` (or the deprecated `urn:access-roster:...` spelling, until v1.76) and `audience=cloudflare:<preset>` (optional `lifetime`, seconds) | RFC 8693 | a Cloudflare API token, or R2 credentials, minted from the preset's prototype for the caller, under the policy's `cloudflare.grants`: `token`, or `access_key_id`, `secret_access_key` and `endpoint`, with `expires_on` ([how-to](../../guides/sluis/cloudflare-tokens.md#4-mint-for-a-person)). Only where the service has a `cloudflare` section |
| `/revoke` | RFC 7009 | revokes a refresh token; what Revoke and "sign out everywhere" call underneath |
| `/login`, `/logout`, `/signed-out` | ours | all of the issuer's HTML: the sign-in chooser, which names the application being signed in to from the client's declared `display_name` and `description`, and the host its redirect returns to, or says a program on this computer is asking when that redirect is loopback ([policy](policy-clients.md#what-the-sign-in-page-calls-a-client)). Also the sign-out a person follows, and where a sign-out lands when the client declares no page. |
| `/account` | ours | a redirect into the console's page for the signed-in person (its sessions and *sign out everywhere*). The address stays because it was linked to and bookmarked |
| `/.access/grants` | ours | the clients the caller's groups admit it to, and which group admits each, and the Cloudflare presets they open. Read by `sluisctl kubeconfig` and `aws-config` to write a context per cluster and a profile per role. A bearer; it discloses nothing the caller's own token does not |
| `/.access/client-secrets/{rotate,show,purge}` | ours | [operator calls on a generated client's secret](#client-secrets), used by `sluisctl clients` |
| `/.access/simulate` | ours | **not built**: what would this identity get, for somebody other than the caller. The console's Rules page has a simulator that answers it, and `/.access/grants` answers it for the caller's own identity |
| `SessionService` (ConnectRPC): `ListSessions{identity? \| client? \| contains?}` → `{sessions, sign_ins}`, `RevokeSessions{identity, client?, session_id?, sso?}` | ours | sessions per identity and per client, with client, how obtained, issued, expires, last refreshed; revoke per identity, per client, or one. Listing and revoking others is operator; listing and revoking your own is any signed-in identity; listing **every** session (neither identity nor client named) is operator-only, paged by `page_size`/`page_token`, and audited. `contains` reads `identity` and `client_id` as substrings and is operator-only; `RevokeSessions` has no such field. Authorized by the browser's SSO cookie on a same-origin call (the console) or by a bearer. `console.origin` is the CORS gate for another origin and stays empty on one origin |
| back-channel logout | OIDC Back-Channel Logout 1.0 | opt-in per client with `backchannel_logout_uri`: a signed `logout+jwt` POSTed to every such client that signed the person in, by the `sid` it saw, when the sign-in ends |
| not served | RFC 8628 device flow, client credentials, RFC 7523 JWT bearer, RFC 7662 introspection, implicit and hybrid flows, session-management iframe, RFC 7591 dynamic client registration | the headless cases (a CI job, a workload) are token exchange; tokens are JWTs verified offline against the key set. Instead of RFC 7591, an origin named in `client_documents` is served by Client ID Metadata Documents: no endpoint, nothing stored |

## Client secrets

`POST /.access/client-secrets/rotate`, `/show` and `/purge` take `{"client": "<id>"}`; `rotate` also takes `overlap_seconds` (absent is 24h, `0` a hard cut, at most 604800). The caller sends a bearer token this issuer issued to `sluis-console` or `sluisctl`, or to `console` or `accessctl` (their older ids, still accepted), with the operators group in `groups`. Answers hold times and flags, never a secret. Without the feature the path answers `404`.

| Status | Means |
|---|---|
| `200` | done; `rotate` answers the rotation time, the overlap and any discarded earlier previous secret, `show` the metadata, `purge` `deleted` |
| `400` | the body is not JSON naming the client, or the overlap is outside 0 to 7 days |
| `401` | no bearer, or one that was not accepted (logged and counted, not audited) |
| `403` | the token is not for `sluis-console`, `sluisctl`, `console` or `accessctl`, or the caller is not an operator |
| `404` | no stored secret for the client |
| `409` | the client's secret is being changed by another call; try again |
| `422` | the client is not generated (`rotate`), or still generated (`purge`: only an orphan is purged) |

A refused verified caller is audited as `roster.client.secret.denied` ([audit actions](audit-actions.md)). Guide: [rotate a client secret](../../guides/sluis/operate/rotate-a-client-secret.md).
