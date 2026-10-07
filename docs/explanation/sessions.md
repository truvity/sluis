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
removed in the next release, and it never signs anyone in. That sign-out is audited as `roster.session.revoked` with
an anonymous actor, because an id proves nothing about who presented it. During a rolling upgrade, or after a rollback,
an older replica still accepts a sign-in id as the cookie, so the protection holds once no older replica serves
traffic.

A new interactive sign-in (step-up, `prompt=login`, `max_age`, another account) ends the sign-in the browser held
before, once the new one is handed to the browser; a refused recovery leaves the earlier sign-in and its cookie in
place. When the same person signs in again, only the previous sign-in record ends. When a different person signs in
in the same browser, the previous person's sign-in ends and so do its per-client sessions, with back-channel logout;
`roster.session.revoked` records it, by the new person, with the previous person as subject. A sign-in that fails
clears the cookie. The console's "revoke one browser" also ends the sessions filed under a sign-in that has already
ended.

When the same person signs in again, the sessions and clients of the sign-in that ends are carried over to the new one
first, so a later sign-out ends them too and tells their clients. The step-up itself revokes nothing and announces
nothing, and the same person's sessions in other browsers are not touched. Carrying is best effort: a session that cannot be
moved is logged and left where it was, and the rest are still carried.

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
so the refresh tokens are revoked too. The order matters: the sign-in ends first, then its sessions are listed and
revoked, the audit record is written, and the clients are told last, so nothing can be opened under a sign-in whose end
has begun. The work is not cancelled when the browser disconnects (it is bounded at 30 seconds), so a closed tab does
not leave sessions unrevoked, and each back-channel logout is limited to 5 seconds per client.

What a sign-out reaches is scoped by what the request can PROVE, which is the cookie it carries. An `id_token_hint` is
a hint in the specification rather than a credential (the library accepts an expired one by design), so it chooses
the signed-out page and nothing else. A request that proves nothing ends nothing.

A code is bound to the sign-in it was completed under. The token endpoint files the session first and then reads the
sign-in. When the sign-in has ended (signed out in another tab, replaced by a step-up or by another person, or past
its absolute limit), it revokes the session it just filed and answers `invalid_grant`; otherwise the session was filed
before the sign-out listed its sessions, and the sign-out revokes it. Either way nothing outlives a sign-in that no later
sign-out could reach. The client starts again, and the browser signs in again or completes silently under the new
sign-in. For a client that asked for `openid` alone, the client is recorded among the sign-in's clients before the
sign-in is read, so the code is refused or the client is told at sign-out. What remains is a sign-out whose two adjacent
store calls (reading the clients, ending the sign-in) straddle both. A code that opens no session (an `openid`-only
client, or a request whose every scope the client may not ask for) is held to its sign-in the same way, before any
token is minted.

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

A read-only resource's longer limit is deprecated in favour of the agent class below: it is still honoured and warned
about at start, and a later minor release will refuse it.

A session with no `auth_time` has nothing to measure the limit against, and none applies: it lives out its ordinary
refresh window. That is true of a workload or a machine trading a proof through token exchange, which authenticates
nobody, and of a per-client session recorded before the field existed. Both are unaffected on purpose: there is
nothing to cap either against.

The console's own session (`config.lifetimes.session`, a fixed-duration cookie rather than a sliding one) is capped
too, by the shorter of the two, because it is issued once at sign-in and its issue time already IS its `auth_time`.

## Agent-class sessions

The limits above suit a person at a browser. They do not suit software that holds a refresh token in its own credential
store and works in the background, such as an MCP host: it would make the person sign in again every day.
The policy can therefore mark a client `session: agent` ([ADR 0040](../decisions/0040-agent-class-sessions.md);
[how to declare it](../reference/policy-clients.md#agent-class-sessions)). The split is by who holds the chain, not by
what the token reaches, so it covers write-capable resources too.

An agent-class chain is held to `lifetimes.agent` in place of `lifetimes.refresh` and `lifetimes.absolute`: idle for
at most 14 days, 30 days from `auth_time` at most, and every access and ID token capped at 30 minutes. The deadline is
`auth_time` plus the shorter of the class's absolute limit and the resource's `absolute_cap`, which for an agent chain
is only ever a ceiling. The client's, `client_documents`' and the resource's `ttl_cap` still shorten the tokens.

The class is decided once, when the authorization completes, from the installation's policy, and is recorded on the
session with its deadline. It never changes afterwards. A later policy or configuration change can shorten a chain at
its next refresh but cannot lengthen it past the recorded deadline, and a client moved from `agent` to `interactive`
keeps its agent chains until they end. A chain recorded before the class existed, or rewritten by an older replica
during a rollout, is interactive. A client document cannot choose its class; only the installation's policy can.

A token exchange of a sign-in (`sign_in_exchange`) refuses a token whose session is agent-class, whatever the client's
row says now.

### The consent page

An agent authorization never completes silently, because a month-long grant obtained without the person seeing it is
the phishing case at its worst. After the person signs in, or from the browser's existing sign-in, they are shown a
page once per authorization of an agent client. It names the client, the origin of a document client, the host the
client returns to, that this is a background connection, the computed deadline (never a nominal 30 days) and who is
signed in. The connection is made only when they press *Allow*. `prompt=none` for an agent client is answered
`consent_required`, and the chooser is shown for one even when a single directory is configured.

The page is enforced by the server, not merely shown. The accept is a POST that carries a token bound to this
authorization request, this person, their sign-in and this browser, kept in a cookie of its own (`__Host-` prefixed
when cookies are secure, HttpOnly, SameSite=Lax). It is checked again where every sign-in completes, and the click
re-checks the sign-in. A start link sent to somebody else, an accept posted from another browser, or the page in a
frame therefore completes nothing; the page is served with `frame-ancestors 'none'` and `X-Frame-Options: DENY`. The
browser sign-in itself is still held to `lifetimes.absolute`, so the cost of the grant is about one click per 30 days
per connection.

### Sign-out keeps agent connections, and says so

Three things end a sign-in, and they treat agent sessions differently:

| What happens | Agent sessions |
|---|---|
| The person signs out (`/logout`, or `/end_session`) | kept: not revoked, no Back-Channel Logout sent to their clients, still filed under the ended sign-in |
| The browser sign-in passes its own absolute limit | kept, like every other live chain |
| Another person signs in in the same browser | ended, with everything else the first person opened |

The signed-out page says that agent connections were kept and links to *sign out everywhere*. When `/end_session`
names an agent client, by `client_id` or by the audience of its `id_token_hint`, that client's own sessions under the
sign-in end too. A hint proves nothing, so the name is used only to end more, never less.

Everything that revokes ignores the class: *sign out everywhere*, a per-client revoke, a per-browser revoke, removal
from the directory and refresh-token reuse end an agent chain as they end any other. The console groups sessions under
their sign-in, ended sign-ins included, so that revoking one browser still reaches the agent sessions its sign-out
spared ([the console](console.md#the-sessions-page)).

## What the console asks of the SSO session

The directory console is mounted on the issuer's origin and reads the SSO session directly rather than redeeming a
code. It used to accept any live sign-in for the rest of that sign-in's own lifetime, so a sign-in past the absolute
limit, or one whose person the directory had since suspended, still opened the most privileged surface while every
other client was refusing it. On every request it now asks the same function the silent `/authorize` asks
(`standingSignIn`), so the two cannot drift:

- **Past the absolute limit** (`auth_time` plus the installation's limit) the request is refused and the sign-in
  ended, with the cascade a silent `/authorize` runs: clients signed in under it without a refresh token are sent a
  back-channel logout token, chains still inside their own limit are spared, and the browser's cookie is cleared.
  Nothing is audited.
- **A person the directory no longer admits** (suspended, not found, or not vouched for with nothing held for them) is
  refused, the sign-in is ended and the cookie cleared, with the same warning in the log (`browser session is no
  longer admitted`).
- **A directory that cannot be reached** is answered from the issuer's hold window, as for a silent sign-in
  ([directory model](directory-model.md)): an admitted person keeps the console for the window (4 hours by default),
  and the sign-in ends past it, or for someone nothing is held for.
- **A recovery sign-in** has no directory to ask and is held to the absolute limit only. It is recognised by the
  method recorded when the sign-in began, never by the shape of its subject: a sign-in made through an identity
  provider is a person whatever its subject looks like, is asked of the directory, and is never the recovery account.
  Tokens and refreshes likewise evaluate a subject as a ServiceAccount only for a recovery sign-in, so a recovery
  session from before the upgrade, which recorded no method, must sign in again.
- **Losing a console role** does not end the sign-in. The console refuses those calls by role, as before, as a
  silent sign-in to a client the person is not entitled to also keeps the sign-in.

A console request makes 4 State reads and no writes, one more than before: the eventually consistent revision read of
the last-known groups that every refresh already pays. It still makes one directory resolution and one snapshot read,
because the issuer's check and the console's authorizer share one answer per request.

The issuer's session service (`ListSessions` and `RevokeSessions`, which the account page and the console's Sessions
page call at the issuer's host) judges a browser's cookie by the same function. A cookie whose sign-in is past the
absolute limit, or whose person the directory does not admit, proves nobody, and the call is refused as
`unauthenticated`; an unreachable directory is answered from the hold window, and a bearer token, where one is sent,
is still checked on its own. The service refuses the cookie and does not end the sign-in: the issuer's pages and the
console end it on the browser's next visit. A cookie call makes 7 State reads for a person listing their own
sessions, no writes, and one directory resolution.

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
and asks the directory once; an `authorization_code` grant makes 9 writes and 5
reads. The saving comes from not rewriting what has not changed: one directory
resolution per request, the access-token record written once, the spent refresh
token marked in its own pointer, and the index sets and the last-known groups
touched only when needed ([storage layout](../reference/storage-layout.md)).

There is one trade-off. The last-known groups behind the hold window are rewritten
only when they change, when the stored record is not this process's own write, or
when it is older than the hold window divided by eight, so a hold can end up to
an eighth of the hold window early, never late.
