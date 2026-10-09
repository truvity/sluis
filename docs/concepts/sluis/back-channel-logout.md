# Telling the relying party: back-channel logout

A sign-out is immediate at the issuer and invisible at the relying party. A console holding a valid access token keeps
serving until it next refreshes, and until then a person who has signed out is still being served pages. `ttl_cap`
BOUNDS that window, per client. A client may also ask to be told, and then the window closes.

A client that declares `backchannel_logout_uri` is POSTed a signed logout token, server to server, the moment a
sign-out ends a session it holds. One that declares none is never contacted, which is what makes serving this safe: it
changes nothing for a client that has not asked.

**Why this one, of the three optional mechanisms.** Session Management and Front-Channel Logout both work by loading
something from the issuer's origin inside the application's page: a polled iframe, or one hidden iframe per client at
sign-out. Browsers block third-party cookies by default now, so both fail quietly in exactly the case they exist for.
This is an HTTP POST between two servers and does not care what a browser allows.

It is also the only one of the three that could ever reach a PROXY, which is what holds the session for a console
running no OpenID flow of its own. *Could*, not *does*: oauth2-proxy encrypts each session with a secret that lives
only in the user's cookie, so nothing server-side can find the session a logout token names. Until that changes
upstream, the proxy's refresh interval is the whole of the dial for a proxied console, a setting the deployment chooses.
Kargo has no such endpoint either. Those relying parties answer until their own session expires or refreshes, which
is the honest boundary of revocation at an issuer and the reason a relying party's session lifetime, and `ttl_cap`,
are decisions rather than details. See [choosing native or gateway OIDC](../../guides/sluis/connect/choosing-native-or-gateway-oidc.md).

Two details the specification is strict about, both pinned by tests: `typ` is `logout+jwt`, and there is no `nonce`.
Both exist so that a logout token cannot be mistaken for an ID token by a relying party that checks too little, which
would turn *you are signed out* into *you are signed in as somebody*.

## Who is told, and by which name

A session in the index is a refresh token, and a client that asked for `openid` alone holds none, yet it signed
somebody in and has to be told when that ends. So the sign-in remembers which clients were issued an ID token under
it, and at sign-out every one of them is told, with or without a refresh token. The token's `sid` is the one the
relying party's ID token carried, which is the per-client session and not the browser sign-in it hangs off: a
relying party matches the two by that value. A client whose ID token carried no `sid` is told by `sub` alone, which
the specification allows.

The order is deliberate in both directions. The sessions are read BEFORE the revocation, because afterwards nothing
records which clients held them. The tokens go out AFTER it, because a client told its session ended and then finding
it alive is worse than one told a moment late. Delivery failures are logged and never raised: the sign-out has
already happened, and a relying party that cannot be reached must not turn a completed sign-out into a failed one.

**What a person's own sign-out spares.** Agent-class sessions are not revoked by a person's own sign-out, so their
clients are not told: no logout token, not even the subject-only one sent to a client that holds a spared session. An
`end_session` that names an agent client ends that client's sessions and tells it. Another person signing in in the
same browser ends everything and tells every client ([sessions](sessions.md#sign-out-keeps-agent-connections-and-says-so)).

## Revoking somebody else's session

A revoked or suspended person is stopped separately, by the next refresh being refused, with the directory's liveness
signal behind it. To end somebody *else's* session is **revocation**, through `RevokeSessions`, which authorizes the
caller first.

The by-id path ends one session and nothing else, so revoking every row of a browser's sessions would leave its SSO
session standing: the list empties, the person believes they signed out everywhere, and the next `/authorize`
completes silently with no password. The Sessions page therefore offers **signing the browser out** beside the rows,
which ends the SSO session and every session under it. The rows keep their narrow meaning, because ending one
session that is not the one you are using is a real thing to want, and the two acts should not be one button.
