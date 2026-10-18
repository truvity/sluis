# HTTP endpoints

`sluis serve` answers on `listen.address` (default `:8080`): the issuer at the origin root, the console under `console.mount` (default `/console`). Request and response shapes: [contracts](contracts.md). Routes: [Configuration: the model](../../concepts/sluis/configuration.md#two-routes).

The issuer serves three grants (`authorization_code`, `refresh_token`, token exchange) and three endpoints that discovery advertises (userinfo, `end_session`, revocation).

| Path | Standard | Purpose |
|---|---|---|
| `/.well-known/openid-configuration`, `/.well-known/oauth-authorization-server`, `/keys` | OIDC discovery, OAuth 2.0 Authorization Server Metadata (RFC 8414), JWKS | what relying parties and MCP clients read |
| `/authorize`, `/token`, `/userinfo`, `/end_session` | OIDC | login, tokens, RP-initiated logout |
| `/authorize` parameters | OIDC Core 3.1.2, 6.2, 7.2.1 | `response_mode` is `query` or absent; anything else, `form_post` and `fragment` included, is `invalid_request`, and discovery's `response_modes_supported` is `["query"]`. `request_uri` is `request_uri_not_supported` and `registration` is `registration_not_supported`. Each goes back to the client in the redirect when `redirect_uri` is one it registered, and is a page otherwise. A native client's loopback `redirect_uri` may differ from a registered one in the port only (RFC 8252 7.3). An `id_token_hint` names the person the request is for: a browser signed in as someone else is not answered silently (`prompt=none` is `login_required`), and a sign-in as someone else is refused to the client as `login_required` |
| `/userinfo` | OIDC Core 5.3, RFC 6750 | the bearer goes in the `Authorization` header or in the form body of a `POST`. In the URL query it is refused with `400 invalid_request` (RFC 9700 4.3). No token: `401` with `WWW-Authenticate: Bearer realm="<issuer>"` and no `error`. A token that is unknown, or whose session has ended: `401` with `error="invalid_token"` |
| `/logout` | ours | the same sign-out for a person rather than a relying party. `GET` shows a page with a *Sign out* button and ends nothing; the button `POST`s a token bound to the sign-in cookie, and a `POST` without it answers `400` and ends nothing. A browser with no sign-in goes straight on. It needs no `id_token_hint`. The console's sign-out button points here. `/end_session` asks the same way, naming the application when `client_id` is a declared client, unless its `id_token_hint` names the person this browser is signed in as (an expired one counts, as RP-initiated logout allows): `GET` shows the button with the request's `client_id`, `post_logout_redirect_uri`, `state` and hint kept, and the `POST` it makes does what the request asked. A `post_logout_redirect_uri` the client did not register is refused before the page. A native client's loopback one may differ from a registered one in the port only When the store cannot be read, `/logout` and `/end_session` answer 503 (an HTML retry page, or JSON `temporarily_unavailable`), keep the cookie and end nothing |
| `/token` with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` | RFC 8693 | CI and workload exchange; the requested `audience` is a client, gated by its `requires` |
| `/token`, the same exchange with `requested_token_type=urn:sluis:params:oauth:token-type:github-installation-token` (or the old spelling `urn:access-roster:...`, accepted until v1.76) and `audience=github-app:<id>` | RFC 8693 | a GitHub App installation token of a catalogue App, under its grants ([contract](contracts.md#installation-tokens-at-token)) |
| `/token`, the same exchange with `requested_token_type=urn:sluis:params:oauth:token-type:cloudflare-token` (or the old spelling `urn:access-roster:...`, accepted until v1.76) and `audience=cloudflare:<preset>` (optional `lifetime`, seconds) | RFC 8693 | a Cloudflare API token, or R2 credentials, minted from the preset's prototype for the caller, under the policy's `cloudflare.grants`: `token`, or `access_key_id`, `secret_access_key` and `endpoint`, with `expires_on` ([how-to](../../guides/sluis/cloudflare-tokens.md#4-mint-for-a-person)). Only where the service has a `cloudflare` section |
| `/revoke` | RFC 7009 | revokes a refresh token; what Revoke and "sign out everywhere" call underneath |
| `/login`, `/logout`, `/signed-out` | ours | all of the issuer's HTML: the sign-in chooser, which names the application being signed in to from the client's declared `display_name` and `description`, and the host its redirect returns to, or says a program on this computer is asking when that redirect is loopback ([policy](policy-clients.md#what-the-sign-in-page-calls-a-client)). Also the sign-out a person follows, and where a sign-out lands when the client declares no page. |
| `/account` | ours | a redirect into the console's page for the signed-in person (its sessions and *sign out everywhere*). The address stays because it was linked to and bookmarked |
| `/.access/grants` | ours | the clients the caller's groups admit it to, and which group admits each, and the Cloudflare presets they open. Read by `sluisctl kubeconfig` and `aws-config` to write a context per cluster and a profile per role. A bearer; it discloses nothing the caller's own token does not |
| `/.well-known/sluis-configuration` | ours | the installation's name and where its backup and restore functions are, as URIs whose scheme names the transport. A bearer the issuer signed; anyone else gets `401`, an issuer with no backup module `404`. Read by `sluisctl login`, `backup` and `restore`; see [discovery](backup.md#discovery) |
| `/.access/client-secrets/{rotate,show,purge}` | ours | [operator calls on a generated client's secret](#client-secrets), used by `sluisctl clients` |
| `/.access/simulate` | ours | **not built**: what would this identity get, for somebody other than the caller. The console's Rules page has a simulator that answers it, and `/.access/grants` answers it for the caller's own identity |
| `SessionService` (ConnectRPC): `ListSessions{identity? \| client? \| contains?}` → `{sessions, sign_ins}`, `RevokeSessions{identity, client?, session_id?, sso?}` | ours | sessions per identity and per client, with client, how obtained, issued, expires, last refreshed; revoke per identity, per client, or one. Listing and revoking others is operator; listing and revoking your own is any signed-in identity; listing **every** session (neither identity nor client named) is operator-only, paged by `page_size`/`page_token`, and audited. `contains` reads `identity` and `client_id` as substrings and is operator-only; `RevokeSessions` has no such field. Authorized by the browser's SSO cookie on a same-origin call (the console) or by a bearer. `console.origin` is the CORS gate for another origin and stays empty on one origin |
| back-channel logout | OIDC Back-Channel Logout 1.0 | opt-in per client with `backchannel_logout_uri`: a signed `logout+jwt` POSTed to every such client that signed the person in, by the `sid` it saw, when the sign-in ends |
| not served | RFC 8628 device flow, client credentials, RFC 7523 JWT bearer, RFC 7662 introspection (`/introspect`, `/oauth/introspect` and the device routes answer `404`), implicit and hybrid flows, session-management iframe, RFC 7591 dynamic client registration | the headless cases (a CI job, a workload) are token exchange; tokens are JWTs verified offline against the key set. Instead of RFC 7591, an origin named in `client_documents` is served by Client ID Metadata Documents: no endpoint, nothing stored |

## Hardening

| What | Value |
|---|---|
| Every response of the issuer and the console | `Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; object-src 'none'; form-action 'self'`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`. The issuer's recovery form and agent consent page leave out `form-action`, which a browser would hold the redirect back to the application to; the consent page adds `script-src 'nonce-…'` for its one script. `style-src` allows inline styles for the pages' own `<style>` and the console's component library |
| `Strict-Transport-Security` | `max-age=31536000` on a response of the issuer or the console that arrived over https, and never `preload`. Over https means a TLS connection, an API Gateway event on Lambda, or a request for the host of the configured public URL when that URL is https and no `X-Forwarded-Proto: http` says the front hop was plain; no `X-Forwarded-Proto` value ever turns it on. Never sent for `localhost`, `*.localhost` or a loopback address, nor over http. A header the response already carries is kept. `hsts.maxAge` (`0s` sends none) and `hsts.includeSubDomains` (off by default, because an issuer on an apex or parent domain would pin every sibling subdomain to https) tune it; turn `includeSubDomains` on only when every subdomain answers https |
| `/assets/` | files only; the directory itself is `404`, not a listing |
| Authorization request cookie | `/authorize` sets `__Host-sluis_authreq` (`sluis_authreq` without secure cookies): HttpOnly, SameSite=Lax, 30 minutes, holding a MAC of each request id this browser started, at most eight. `/login`, `/login/<provider>/start`, the silent completion and `/authorize/callback` require it; without it the page says "This sign-in was started in another browser" and `400`, and nothing completes. Open the application again from the browser you mean to sign in with. `prompt=none` is still answered `login_required` at the client's redirect. `HandlerWithSignIn` refuses a session store with no state key |
| Cross-origin access to the protocol endpoints | `Access-Control-Allow-Origin: *` and never `Access-Control-Allow-Credentials`. `SessionService` allows only `console.origin` |
| Request body | at most 2 MiB (`413` when declared larger; read error when it turns out larger). A token request is read for 1 MiB to find its grant. Connect messages are refused over 2 MiB (`resource_exhausted`); the audit services' over 4 MiB |
| Client document fetch | connects only to public addresses: loopback, private, link-local (the metadata service), carrier-grade NAT and reserved ranges are refused where the socket connects, after the name resolves, so DNS rebinding does not help. No proxy from the environment, no redirects. A failed fetch is remembered for 1 minute, and one host is fetched from at most 30 times a minute, whatever the ids (so more than 30 uncached documents of one host in a minute are delayed, not refused for good) |
| Back-channel logout post | connects to the declared address, private ranges allowed (a service in the cluster), loopback and link-local refused; a redirect is a failure and is not followed. Neither check applies with `allowInsecure` and an issuer URL on loopback; `allowInsecure` alone lifts nothing |
| Listener timeouts | headers 10 s, whole request 60 s, response 20 min (a backup is one synchronous call of up to 16 minutes), idle 2 min; header block at most 64 KiB. The audit services use 5 min for the response |

## Refusals

A refusal is fixed words and a request id; the reason is kept under it.

| Surface | A refused caller reads |
|---|---|
| `/token`, `/authorize`, `/end_session`, `/.access/*` | `error_description` is `not authorised (request <id>)`, `invalid request (request <id>)` for a malformed request, or `server error (request <id>)` for a fault of ours. Nothing names a client, audience, group or address |
| Sign-in and sign-out pages | `Not authorised. Reference: <id>` (`Server error.` for a fault), and the way back. No address, domain or error text |
| ConnectRPC: the console's services and `SessionService` | `permission_denied` is `not authorised (request <id>)`, `internal` is `server error (request <id>)`, `unauthenticated` is `sign in first`; other codes are unchanged |
| The directory listener | `401` or `403` with `not authorised (request <id>)` |
| `error` | the specification's: `invalid_client`, `invalid_grant` (a code, refresh token or subject token that does not match), `invalid_target`, `unauthorized_client`, `server_error` |
| `invalid_client` | `401` with `WWW-Authenticate` when the client sent an `Authorization` header, else `400`. An unknown client and a wrong secret are one answer |
| `invalid_target` at `/token` | an unknown audience and one the proof is refused are one answer |
| `X-Request-Id` | on every response. The caller's own is kept when it is 8 to 64 letters, digits, `.`, `_`, `:` or `-`, and replaced otherwise |
| Finding the reason | search the log for `request_id`, or the audit trail for `context.request_id`. A refused exchange or sign-in records `reason_code`, `audience`, `missing_requires`, `held_groups`, `admitted`, `admitted_by` and `directory` ([audit actions](audit-actions.md)) |

## Client secrets

`POST /.access/client-secrets/rotate`, `/show` and `/purge` take `{"client": "<id>"}`; `rotate` also takes `overlap_seconds` (absent is 24h, `0` a hard cut, at most 604800). The caller sends a bearer token this issuer issued to `sluis-console` or `sluisctl`, or to `console` or `accessctl` (their older ids, still accepted), with the operators group in `groups`. Without the feature it answers `404`.

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
