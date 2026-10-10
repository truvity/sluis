# How do sessions and sign-out work?

Three things are called a session. A proxy holds the browser session for one console. The issuer holds the SSO session, so a second console needs no second login. It also holds one refresh token per identity and client.

## The SSO session and sign-out

The SSO session is an HttpOnly cookie at the issuer's host, backed by a record in the shared store. A live record completes `/authorize` silently and honours `prompt=login` and `max_age`. Each per-client session points at its parent sign-in, so one operation on the parent ends them all.

The cookie `sluis_sso` holds 32 random bytes, base64url, with a `__Host-` prefix when cookies are secure. The store keeps only its hash. The sign-in id that listings show is a name and authenticates nobody.

Sign-out ends the sign-in and every session under it, through `/logout` or `end_session`. The order is fixed: end the sign-in, revoke its sessions, write the audit record, tell the clients last. The work survives a browser disconnect for 30 seconds, and each back-channel logout takes at most 5. See [back-channel logout](back-channel-logout.md).

- A request ends only what its cookie proves. An `id_token_hint` ends nothing.
- When the store is unreadable, sign-out answers 503, ends nothing and keeps the cookie.
- A new interactive sign-in ends the previous one.
- An authorization code is bound to its sign-in. If the sign-in ended first, the token endpoint revokes the session and answers `invalid_grant`.

## Refresh token reuse

A refresh token is single-use: a refresh spends it and returns a successor. A spent token presented again within 30 seconds returns the same successor, because two replicas of one proxy race the same token. After that, the presentation ends the session the token was spent in.

The spent token's pointer is sealed with AES-256-GCM under a key derived from the token itself. A spent token presented under another client is refused and ends nothing.

The grant answers `invalid_grant` and the client's `backchannel_logout_uri` receives a logout token. The browser sign-in stays, and issued access tokens live to their expiry. The audit record carries scope `refresh_token_reuse`.

## The absolute session limit

`config.lifetimes.refresh` bounds inactivity. `config.lifetimes.absolute` (24h by default) bounds the sign-in itself. A per-client session ends at `min(now+refresh, auth_time+absolute)`, recomputed on every rotation. `auth_time` comes from the SSO session, never from the refresh.

Three checks enforce it:

- A refresh past the limit answers `invalid_grant` and revokes the session.
- A token's `exp` is capped at `auth_time+absolute`.
- A silent `/authorize` past the limit ends the SSO session.

A session with no `auth_time`, such as a machine's token exchange, has no limit. A read-only resource's `absolute_cap`, up to seven days, is deprecated and warned about at start.

## Agent-class sessions

An MCP host holds a refresh token in its own store and would otherwise force a daily sign-in. A client marked `session: agent` has its chain held to `lifetimes.agent`. By default that is 14 days idle, 30 days from `auth_time` and 30-minute tokens. See [the client keys](../../reference/sluis/policy-clients.md#agent-class-sessions).

The class is fixed when the authorization completes. A policy change can shorten a chain and never lengthen it. Only the policy chooses the class, never a client document.

An agent authorization never completes silently. The person sees a consent page naming the client, its return host, the computed deadline and who is signed in. `prompt=none` answers `consent_required`.

### Sign-out keeps agent connections, and says so

| What happens | Agent sessions |
|---|---|
| The person signs out | Kept, with no back-channel logout. |
| The browser sign-in passes its limit | Kept. |
| Another person signs in in the same browser | Ended with the rest. |

A person chooses one of three scopes. *Sign out all browsers and apps* keeps agents. *Disconnect all agents* keeps browsers. *Sign out everything* ends both and is the default. Operator revokes and directory removal ignore the class. See [the Sessions page](console.md#the-sessions-page).

## What the console asks of the SSO session

The console reads the SSO session on every request, through the function silent `/authorize` uses, so the two cannot drift.

- Past the limit, or for a person the directory no longer admits, the request is refused and the sign-in ends.
- An unreachable directory is answered from the hold window (`lifetimes.hold`, 4 hours by default). See [the directory model](directory-model.md).
- A recovery sign-in is held to the absolute limit only. The method recorded at sign-in identifies it, never its subject.

## Who may open which console

A client's `requires` lists internal groups, and any one admits. It is checked at token exchange, when a browser sign-in completes, and on every refresh. A refusal at sign-in is a page, and the person stays signed in.

## Decided in

- [ADR 0001: sessions and an absolute limit](../../decisions/0001-sessions-and-an-absolute-limit.md)
- [ADR 0033: a longer absolute limit for read-only resources](../../decisions/0033-a-longer-absolute-limit-for-read-only-resources.md)
- [ADR 0040: agent-class sessions](../../decisions/0040-agent-class-sessions.md)
