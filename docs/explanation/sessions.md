# Sessions and sign-out

Three things get called a session and each has one owner. A proxy holds the **browser session** for one console, a
ticket cookie with the state in a shared store. This process holds the **SSO session** with the browser, so a second
console needs no second login, and one **refresh token per identity and client**, which is what kubelogin, `sluisctl`
and every proxy actually hold.

**The SSO session is the keystone.** A cookie at the issuer's host, HttpOnly, backed by a record in the shared
store: identity, `auth_time`, how they authenticated. `/authorize` completes **silently** when it is live, so signing
in at one console and opening a second is a redirect with no prompt; it honours `prompt=login` and `max_age`.
`end_session` clears it. Each per-client session points at the SSO session that parents it, so *sign out everywhere*
is one operation on the parent.

**The cookie is a secret; the sign-in id is a name.** The cookie (`access_issuer_sso`, `__Host-` prefixed when cookies
are secure) holds a fresh 32-byte random value, base64url. The store keeps only its hash (a labelled SHA-256) as a
pointer, `issuer:sso-cookie:<hash>`, to the sign-in's id, and the sign-in record carries the same hash. The id is
what the console and the `sso` field of a listed session show, and it authenticates nothing: seeing one does not let
anyone be that person. Before this, the cookie *was* the id, so an id seen in a listing was a credential. A cookie set
by an older version signs nobody in: after that upgrade every browser signs in once more. For one release, a sign-out
from a browser still holding such a cookie ends that old sign-in and revokes its sessions, as before; that fallback is
removed in the next release, and it never signs anyone in.

A new interactive sign-in (step-up, `prompt=login`, `max_age`, another account) ends the sign-in the browser held
before, the sign-in only and not its per-client sessions. A sign-in that fails clears the cookie.

When the store cannot be read at sign-out, `/logout` and `/end_session` answer 503 and end nothing: an HTML "Sign-out
did not complete" page with a retry link, or JSON `temporarily_unavailable`. The cookie is kept, so the person can
retry; clearing it and reporting success would have left the sign-in alive.

Per-client sessions are first-class too, not opaque tokens in a store: a per-identity index of client, how it was
obtained, issued, expires, last refreshed, so they can be **listed** per identity and per client and **revoked** per
identity, per client, or one at a time. The index lives in the **shared** store, because an index per process listed
what one replica happened to record and revoked only there: for a control whose whole job is to end access, the worst
failure available. A refresh token is hashed into its key, so an index that can be read is not an index that can be
replayed.

Sign-out ends the sign-in AND every session opened under it, through whichever door was used: `/logout`, which a
person follows, and `end_session`, which a proxy chains to. Ending the sign-in alone would stop only the next silent
`/authorize`: a console already open would keep refreshing and serving pages after a sign-out that reported success,
so the refresh tokens are revoked too.

What a sign-out reaches is scoped by what the request can PROVE, which is the cookie it carries. An `id_token_hint` is
a hint in the specification rather than a credential (the library accepts an expired one by design), so it chooses
the signed-out page and nothing else. A request that proves nothing ends nothing.

To tell the relying party that its session ended, see [back-channel logout](back-channel-logout.md).

## Refresh token reuse

A refresh token is single-use: a refresh spends it and returns a successor. A client that presents a spent token
twice is either broken or has had the token stolen, and the issuer cannot tell which presentation is the thief's
(RFC 9700 section 4.14.2). Two replicas of one proxy also race the same token, so a replay inside a 30-second grace is
answered with the same successor, as before. After the grace, the presentation ends the session the token was spent
in.

The spent token's pointer stays so that this is detectable. It holds when the token was spent, the session id and the
successor, sealed (AES-256-GCM) under a key derived from the spent token itself, so the store never holds a live
refresh token in plain and a mark opens only for the token that made it. It lives until the family's absolute deadline,
`max(auth_time + absolute - now, 30s)`, so a reuse is detected for as long as the family could live and no longer. A
session with no `auth_time`, or a deployment with no absolute limit, keeps it until the session ends (at most
`config.lifetimes.refresh`, 12h by default).

The session is ended only after the library has authenticated the client and matched it to the session's: a spent token
presented under another client is refused and ends nothing. A forged mark, or one that does not open with the presented
token, is treated as an unknown token. The grant is refused with `invalid_grant`, the session leaves every listing, and
a client that declares a `backchannel_logout_uri` is sent a logout token. The browser sign-in stays, so a false positive
costs one client's session and not every one. JWT access tokens already issued and credentials already obtained by token
exchange stay valid until they expire. The revocation is audited once, as `roster.session.revoked` by the system with
the scope `refresh_token_reuse` and the sign-in's id in `sso` when there is one, so an operator can end the sign-in too.

A mark dated more than 2 seconds ahead of the replica reading it means the replicas' clocks disagree, which moves the
grace window. The issuer logs a WARN, keeps the grace and counts it in `access_issuer.spent_mark_ahead`
([telemetry](../reference/telemetry.md)).

## The absolute session limit

Everything above bounds *inactivity*: a session dies once nothing refreshes it for `config.lifetimes.refresh`.
Nothing bounded the sign-in ITSELF: a client that refreshed often enough stayed signed in indefinitely, because a
rotation only asked "was this used recently", never "how long ago did this person actually authenticate".
`config.lifetimes.absolute` (default 24h) is that second question, and a per-client session's end is
`min(now+refresh, auth_time+absolute)`, decided when it opens and recomputed on every rotation, so a sliding
refresher plateaus at the limit rather than climbing past it. `auth_time` is carried down from the SSO session (or the
sign-in itself, for one opened directly), never from the refresh: a session opened against an hour-old SSO session
inherits that hour, not a fresh 24.

Three things enforce it, because a session end recorded in an index is worth nothing if the things that use it do not
check:

- **A refresh at or after the limit** is refused (`invalid_grant`) and the session is revoked, with its own audit
  reason, distinct from an ordinary inactivity timeout or a lost entitlement, both of which the session index also
  produces, but silently.
- **An access or ID token's `exp`** is capped at `auth_time+absolute` too, even when the ordinary token lifetime
  would reach further: a session that has just hit the limit must not go on answering `userinfo`, or any resource
  trusting the token's own `exp`, for whatever was left of its last token.
- **Silent `/authorize`** refuses an SSO session whose `auth_time` is past the limit, ending it first (the same
  cascade `/logout` runs, back-channel logout included) rather than completing against it. A browser left open must
  not go on renewing its sign-in one client at a time forever.

A read-only resource may carry a longer limit of its own, up to seven days
([ADR 0033](../decisions/0033-a-longer-absolute-limit-for-read-only-resources.md)). Each of the three checks then
uses the limit of the resource the session was opened for: the shortest `absolute_cap` among a chain's resources, the
global limit for the client's own audience or any resource without a cap. A silent `/authorize` past the global limit
ends the browser sign-in for a request that is not extended, but spares the extended chains the browser still holds,
which end by sign-out, revocation or their own limit.

A session with no `auth_time` has nothing to measure the limit against, and none applies: it lives out its ordinary
refresh window. That is true of a workload or a machine trading a proof through token exchange, which authenticates
nobody, and of a per-client session recorded before the field existed. Both are unaffected on purpose: there is
nothing to cap either against.

The console's own session (`config.lifetimes.session`, a fixed-duration cookie rather than a sliding one) is capped
too, by the shorter of the two, because it is issued once at sign-in and its issue time already IS its `auth_time`.

## Who may open which console

A client's `requires` names the internal groups any one of which admits somebody to it. It is checked in three
places: **token exchange**, where the audience is the decision; **a browser sign-in**, as the request is completed;
and **a refresh**, as the session is renewed.

The middle check cannot happen at `/authorize`: the request arrives before anyone has proved who they are, so there
is nobody to judge. It happens when the sign-in completes, which is the first moment both facts exist. Without it,
`requires` on a browser client is documentation rather than a gate: anybody this issuer would authenticate receives a
token for any declared client, and what stops them is whatever the application checks for itself.

A refusal there is a **page**. The relying party is not the one that needs telling, and a redirect carrying an error
produces a console rendering its own version of a refusal it does not understand. The person is signed *in*: the
browser session stands, and the next console they are entitled to costs them no password.

The third check is why the second is not enough on its own. A grant removed after a token was issued would otherwise
keep working for as long as the refresh token lives; re-checking at renewal ends it at the next refresh instead,
which for a proxied console is its `ttl_cap`.

## What a refresh costs

Considered for an installation where MCP clients refresh every few minutes, the
issuer's State traffic per grant is a cost to keep small. Measured in State
operations, a `refresh_token` grant makes 4 writes and 4 reads, one of the reads
eventually consistent ([`RevisionPeeker`](../reference/ports.md#peeking-a-revision-optional)),
and asks the directory once; an `authorization_code` grant makes 9 writes and 4
reads. The saving comes from not rewriting what has not changed: one directory
resolution per request, the access-token record written once, the spent refresh
token marked in its own pointer, and the index sets and the last-known groups
touched only when needed ([storage layout](../reference/storage-layout.md)).

There is one trade-off. The last-known groups behind the hold window are rewritten
only when they change, when the stored record is not this process's own write, or
when it is older than the hold window divided by eight, so a hold can end up to
an eighth of the hold window early, never late.
