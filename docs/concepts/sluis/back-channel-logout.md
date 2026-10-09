# How does a relying party learn of a sign-out?

A sign-out is immediate at the issuer. A relying party holding a valid access token keeps serving until it next refreshes. `ttl_cap` bounds that window per client.

A client that declares `backchannel_logout_uri` closes the window. When a sign-out ends a session the client holds, sluis POSTs it a signed logout token, server to server. A client that declares none is never contacted.

## Why back-channel only

Session Management and Front-Channel Logout load something from the issuer's origin inside the application's page. Browsers block third-party cookies by default, so both fail quietly. Back-channel logout is an HTTP POST between two servers and does not depend on the browser.

The logout token carries `typ: logout+jwt` and no `nonce`. A relying party that checks too little then cannot mistake it for an ID token. Tests pin both.

## What a proxy can and cannot do

A gateway proxy holds the session for a console that runs no OpenID flow of its own. The proxy (oauth2-proxy) encrypts each session with a secret only the user's cookie holds. Nothing server-side can find the session a logout token names. Kargo has no such endpoint.

For those relying parties the sign-out takes effect when their own session expires or refreshes. Set the proxy's refresh interval and `ttl_cap` accordingly: see [choosing native or gateway OIDC](../../guides/sluis/connect/choosing-native-or-gateway-oidc.md).

## Who is told, and by which name

A client that asked for `openid` alone holds no refresh token, and so no entry in the session index. The sign-in therefore records which clients were issued an ID token under it. At sign-out, sluis tells every one of them.

The logout token's `sid` is the per-client session the relying party's ID token carried. It is not the browser sign-in. A client whose ID token carried no `sid` is told by `sub` alone, which the specification allows.

Sessions are read before the revocation, because afterwards nothing records which clients held them. Tokens go out after it, so a client never learns of a sign-out and then finds the session alive.

Delivery failures are logged and never raised. A relying party that cannot be reached does not turn a completed sign-out into a failed one.

## What a person's own sign-out spares

A person's own sign-out does not revoke agent-class sessions, so sluis tells none of their clients. It sends no logout token, not even the subject-only one.

An `end_session` that names an agent client ends that client's sessions and tells it. Another person signing in in the same browser ends everything and tells every client ([sessions](sessions.md#sign-out-keeps-agent-connections-and-says-so)).

## Revoking somebody else's session

A revoked or suspended person is stopped by the next refresh being refused, with the directory's liveness signal behind it. To end another person's session, use `RevokeSessions`, which authorizes the caller first.

The by-id path ends one session and nothing else. Revoking every row of a browser's sessions leaves its SSO session standing, and the next `/authorize` completes silently.

The Sessions page therefore offers **signing the browser out** beside the rows. It ends the SSO session and every session under it. The rows keep their narrow meaning: ending one session that is not the one you use.

## Decided in

- [ADR 0001: Sessions and an absolute limit](../../decisions/0001-sessions-and-an-absolute-limit.md)
- [ADR 0040: Agent-class sessions](../../decisions/0040-agent-class-sessions.md)
