# 0040 — Agent-class sessions: a longer chain by client class, not by resource

**Status:** Proposed; amends [0001](0001-sessions-and-an-absolute-limit.md) (agent-class chains are
not held to the installation's absolute limit) and deprecates the lengthening half of
[0033](0033-a-longer-absolute-limit-for-read-only-resources.md)
**Date:** 2026-10-07

## Context

[0001](0001-sessions-and-an-absolute-limit.md) ends every refresh chain at `lifetimes.absolute` (24h) from
`auth_time`, and a person's sign-out ends every chain the browser opened (`endSignIn` in
`internal/issuer/signin.go` revokes `Query{Identity, SSO}`). Both are right for a person at a browser. They are wrong
for agent clients such as MCP hosts and other bot-like software that holds a refresh token in its own credential
store and works in the background: the host makes the person sign in again every day, and signing out of a console
silently breaks a tool running elsewhere.

[0033](0033-a-longer-absolute-limit-for-read-only-resources.md) answered this per resource: a `read_only` resource
may carry an `absolute_cap` up to `policy.MaxAbsoluteCap` (7 days). That helps only where the resource can honestly
say it changes nothing, and it rejected a per-client limit on the ground that safety is a property of what the token
reaches. The owner has since decided otherwise: whether a person must prove presence daily is a property of **who
holds the chain**, a person at a browser or a background host, and agent hosts need write-capable resources too.

Fixed before the alternatives were compared (the owner's decisions):

1. An agent-class chain lives **30 days absolute**. The idle limit is **14 days**.
2. The split is by **OIDC client class**, not by resource; long chains apply to write-capable resources too.
   Interactive clients keep the installation's `lifetimes`.
3. A person's browser sign-out ends interactive sessions only. An agent session still ends on a per-client revoke,
   on *sign out everywhere* (`Issuer.Revoke(identity)` and `RevokeSessions` with no client), on removal from the
   directory and on refresh-token reuse detection.
4. The revocation-latency bound is the one stated in decision 5 below, and an authoritative "not live" answer at
   refresh ends the session.
5. An agent-class authorization never completes silently (decision 6).

## Decision

**1. Policy shape.** A declared client may say `session: agent`; absent is `interactive`. `client_documents` may say
`session: agent` for **every** document client, beside its `requires` and `ttl_cap`. A client document cannot
declare its own class: the issuer never reads a class from the fetched document, only from the installation's
policy. An installation that needs one document client interactive and another agent declares one of them as a
client row, and an origin admitted with `session: agent` must serve only documents its vendor controls (never ones
its users publish), because every document on it gets the class.

Refused at load: `session` on an `exchange` client (an exchange opens no chain with an `auth_time`), and
`session: agent` together with `sign_in_exchange: true` (that client is a person's CLI, and a month-long chain there
would make every cluster and cloud credential traded from it a month long in effect). Warned at load: `session: agent`
on a client with `signed_out` or `backchannel_logout_uri`, which describe a browser-facing application.

The class's lifetimes live in the service configuration next to the others, because they are an installation's
dial like `lifetimes.absolute`, and because the policy's `lifetimes` table is keyed by group name, where `agent`
would collide with a group:

```yaml
lifetimes:
  absolute: 24h          # interactive, unchanged
  agent:
    refresh: 336h        # idle limit, 14 days
    absolute: 720h       # 30 days from auth_time
    access: 30m          # mandatory access- and ID-token cap for the class
```

Refused at load, in the style of `Resource.CheckAbsoluteCap`: `agent.absolute` above **90 days** (2160h) or not
positive; `agent.refresh` above `agent.absolute`; `agent.access` above **1h** or not positive. Shortening is always
allowed. The 90-day ceiling exists so that a typo cannot turn the class into "never signs in again". The idle limit
stays at 14 days: it ends an abandoned credential store (an uninstalled host, a laptop in a drawer), and it is half
the absolute limit, so a host used once a fortnight lives its full 30 days and one left longer does not.

**2. Effective lifetime of a chain.** Its deadline is `auth_time + min(class.absolute, the resource's absolute_cap)`,
and its end `min(now + class.refresh, deadline)`. For an agent chain a resource's `absolute_cap` is only ever a
ceiling. The access token's `exp` is `min(lifetimes.token, the client's ttl_cap, client_documents.ttl_cap, the
resource's ttl_cap, lifetimes.agent.access)`, never past the deadline, and an agent client's ID token is capped by
`lifetimes.agent.access` too. Today `client_documents.ttl_cap` reaches a document client's ID token but not its
access token (`Storage.issue` looks the client up among declared clients only); this change set closes that.
Everything that shows a person how long a chain lasts shows the computed deadline, never a nominal 30 days.

**3. The class is recorded on the session.** `Session` (`internal/issuer/session.go`) gains `class` and `deadline`,
written by `Sessions.Record` at authorization from the client's class at that moment and never re-derived on refresh.
A later policy or configuration change can **shorten** a chain (the current `lifetimes.agent` and resource caps
still bound it at the next refresh, as 0033 does for a withdrawn cap) but never **lengthen** it past its recorded
deadline or change its class. A client moved from `agent` to `interactive` keeps its agent chains until they end or
are revoked; one moved the other way gets agent chains at its next authorization. A record with no class, or with
class `agent` and a zero deadline, is interactive, which covers every chain recorded before the upgrade.

During a rolling upgrade or a rollback an older replica reads a record into a struct without these fields and writes
it back without them (`writeRotated`, `refile`), and it applies `pastLimit` with the installation's limit. Either way
the chain becomes interactive, and one already older than `lifetimes.absolute` ends at that refresh. That is the safe
direction and is accepted.

**Every lifetime that follows the class.** Today each one is `Sessions.lifetime`, the installation's refresh window,
and each must become the recorded class's:

- the session record's store TTL in `put`, `writeRotated` and `refile`/`Refile`;
- the token pointer's TTL in `Record` and in `rotate`;
- index membership: `indexLifetime`, the horizon `indexed` compares with, and the Add in `index`;
- `endedByAbsoluteLimit`, which infers "cut at the limit" from a record that is still stored but no longer live,
  and is only right while the record's TTL equals the class's refresh window from its last write;
- `spentLifetime` (decision 9).

**Invariant: index membership never ends before the record does.** An agent session is indexed until its deadline.
On an engine whose set expiry is the whole set's, an Add never shortens it, so every Add uses the longest horizon
any class needs. Interactive ids then stay in the sets longer, and the listing's self-repair drops them as today.
Required test: an agent session idle for longer than `2 × lifetimes.refresh` is still ended by
`Issuer.Revoke(identity)`, by a per-client revoke and by a per-browser revoke, and is still listed in the console.

**4. The read-only exception is deprecated.** With agent class in place, 0033's lengthening (an `absolute_cap` above
`lifetimes.absolute` on a `read_only` resource) has no remaining user: the connectors it was written for are agent
clients. Two mechanisms for one need is one too many, and the class answers it for write paths as well. Migration:
mark the connector clients `session: agent`, then remove `absolute_cap` from those resources (or lower it to at most
`lifetimes.absolute`). The release that ships agent class accepts the old rows with a load-time warning naming
`session: agent`; a later minor release refuses `absolute_cap` above `lifetimes.absolute` and `read_only` with it,
marked **Breaking:** under [0007](0007-breaking-changes-inside-1x.md). `absolute_cap` as a shortening cap stays.

**5. Bound on revocation latency.** After a person is removed from the directory, an agent chain ends within
**the access-token cap plus `freshness.refreshInterval`** (15 minutes by default). The access-token cap is
`lifetimes.agent.access` (at most 1h, 30 minutes by default) or the resource's `ttl_cap` if that is shorter. The hub
answers from a snapshot that sees the removal within one refresh interval. Every refresh after that is refused,
because the refresh re-checks the directory (`Storage.entitled` in the refresh path of
`internal/issuer/storage.go`). The exposure is then the access token already issued. The cap is mandatory, so the
bound holds for an agent client with no resource as well. While the directory cannot be reached, `Resolver.Resolve`
(`internal/issuer/resolve.go`) answers with the last-known groups for `lifetimes.hold` (4h) from the last
authoritative answer, and that window adds to the bound. This is the same for every class.
`Issuer.Revoke(identity)` forgets the held groups and ends every chain at once, and is the lever when that is too
long.

**An authoritative "not live" answer ends the session.** Today a refresh whose person the directory authoritatively
reports suspended or not found meets `*Refused`, which the refresh path maps to `server_error`: nothing is deleted
and nothing is audited, so the chain lingers until it idles out. From this change, for every class, such a refresh
deletes the session and its token pointer, writes `roster.session.refresh_refused`, and answers `invalid_grant`.
`Refused` carries whether the answer was authoritative. A non-authoritative refusal (the directory cannot vouch and
nothing is held) still answers `server_error` and ends nothing, because it is an outage and not a removal. A person
who loses a group but stays live is refused with `invalid_grant` and audited, as today.

**6. An agent authorization never completes silently.** For an agent-class client, `signIn.silent` and the
single-provider redirect in `signIn.chooser` are bypassed. An interstitial names the client, its origin (for a
document client), the redirect host, the class and the computed deadline, and asks for one click. With a live
browser sign-in the click completes against it; without one, the provider buttons are the click. `prompt=none`
returns `consent_required`. The browser sign-in's own limit stays `lifetimes.absolute` for agent clients:
`checkSignIn` does not extend it per request as it does for an extended resource. This is what turns a phishing
document or a forged authorization link into something a person has to read and accept. The cost is about one click
per 30 days per connection.

**7. Sign-out: three modes, and revocation never spares.** `endSignIn`'s `sparingLive` bool becomes a three-value
mode:

| Mode | Where | Spares |
|---|---|---|
| own sign-out | `/logout`, `end_session` | live sessions whose recorded class is `agent` |
| own limit | the browser sign-in past its absolute limit (silent `/authorize`, the console) | every live chain, as `sparingLive` does today |
| replaced | another person signs in in the same browser (`endPrevious`) | nothing |

Spared sessions are not revoked, are not announced by Back-Channel Logout (`announceLogout`), and stay filed under
the ended sign-in's id. The subject-only logout token for clients signed in with `openid` alone is still not sent to
a client that holds a spared session. When `end_session` names an agent-class client (by `client_id` or the
`id_token_hint`'s audience), that client's own sessions under the sign-in are not spared. A hint proves nothing, so
the name is used only to end more, never less. Each mode has its own test.

**Invariant: nothing that revokes consults the class.** `Issuer.Revoke(identity)`, `RevokeSessions` by identity, by
client, by browser or by id, refresh-token reuse, and an authoritative "not live" at refresh end every class alike.
`signInProof` refuses a token whose session has class `agent`. The load-time refusal of `sign_in_exchange` is not
enough on its own, because a recorded class outlives a policy change.

**Incident levers.** To end a person, use `Issuer.Revoke(identity)`. To end one client for one person, use the
per-client revoke. To end one client for everybody, there is a new operator-only console action revoking
`Query{ClientID}`; the query and the `issuer:sessions-for:` index already exist. Removing a client or an origin from
the policy only **stops** its chains, since the client is then unknown at refresh; it does not end them. They stay
listed, and would resume within their idle window if the row came back.

**8. Audit and console.** `roster.session.ended` gains `spared`, the client ids of the agent sessions a sign-out left
running. `roster.person.signed_in` gains the session's `class` and `deadline`. The catalogue moves from 1.9.0 to
1.10.0 with its released fixture, following [change the audit catalogue](../how-to/change-the-audit-catalogue.md).
Recording is best effort, so the audit writer goes first: an audit installation must hold catalogue 1.10.0 before
the issuer that emits it is deployed. A Lambda audit writer needs the new catalogue release asset, schemas included.

The console lists sessions grouped under their sign-in id, ended sign-ins included, so that "revoke one browser"
still reaches the agent sessions a sign-out spared. Each row shows the class and the deadline beside the sliding
expiry (an additive `session_class` and `deadline` on `accessissuerv1.Session`), and the per-client revoke stays.
Optionally, a session records the source address and User-Agent at open and at the last refresh, for the console. The
signed-out page must say that agent connections were kept, and must link to the account page's *sign out
everywhere*.

**9. Spent marks and storage.** A mark is kept until `min(deadline, now + class.refresh)` instead of the deadline
alone. A token older than the idle window can no longer be told apart as a reuse, but it is refused all the same.
Assume a host that refreshes each time its 30-minute access token expires, and a mark of about 200 bytes. An agent
chain then holds at most 14 days × 48 = **672 marks** (about 130 KB), where deadline-only retention would hold 1,440.
An interactive chain refreshing hourly holds at most 24. A Valkey that backs State must run with `noeviction`, so
eviction can never remove a session, a pointer, a mark or an index key. A per-session rotation counter, with an alert
on a chain that rotates much faster than its access-token cap implies, shows a looping host or a misused token.

**10. Cheap `invalid_grant`.** A host whose chain has ended keeps presenting the dead refresh token, often in a loop.
Each presentation costs two to four State reads (`Sessions.present`, then `endedByAbsoluteLimit`).

- **Fingerprint.** A refusal as an invalid refresh token logs a WARN with the request's client id (unauthenticated,
  so through `logsafe`) and a fingerprint: the first 8 hex of an HMAC-SHA-256 of the token. The key is derived from
  the installation's state secret with HKDF and a label of its own (never the raw secret), or drawn at random per
  process where no state secret is configured. The token is never logged.
- **Negative cache.** An in-process LRU with a fixed (non-sliding) 5-minute TTL is keyed by the **full** HMAC; the
  8-hex prefix would collide and refuse a valid token. A repeat then costs 0 State calls. The WARN is written once
  per cache entry, and a counter counts the hits.
- **What may be cached.** Only consistent reads that found no pointer **and** no legacy rotated record, a record that
  is absent, or a record whose `ExpiresAt` is earlier than `now − (refreshGrace + markAheadTolerance)`. The entry is
  written only after the refusing path has done its work, so the absolute-limit refusal still audits once.
- **Invariant: never cached.** A spent mark inside its 30-second grace window or in the 2-second tolerance band past
  it; a spent mark whose session is live (a reuse that must reach `endReuse`); any failed read. A dead token never
  comes back, so nothing needs invalidating.

## Consequences

- Agent hosts stop asking for a daily sign-in, and a console sign-out stops breaking them. In exchange, a person's
  sign-out no longer means "everything I opened has ended". That is why the signed-out page changes and the console
  shows the spared sessions.
- **Threat model.** A refresh token stolen from an MCP host's credential store is useful until the chain's deadline
  (at most 30 days, and at most 14 days idle).
  - **Mitigations:** the directory re-check on every refresh, with deletion on an authoritative "not live", and
    rotation with reuse detection: a token used by both thief and host ends the family, whichever presents first.
    Also per-client, per-person and per-client-everywhere revocation, *sign out everywhere*, the audit trail, the
    access-token cap, and the interstitial.
  - **Residual:** no DPoP or other sender-constrained tokens yet, because MCP clients do not widely support them
    (revisit when they do). A thief who takes the token from a host that never presents it again (uninstalled,
    lost, wiped) keeps the chain alive by refreshing, since reuse detection fires only when both present. Malware
    living on the host can take each new token as it is minted.
  - **Who grants the class:** only policy authors mark a client `agent`, through the reviewed policy, and for
    document clients only the installation's `client_documents` block does, never the document.
- Storage holds up to 672 marks per active agent chain (decision 9), and index sets hold interactive ids longer.
- An older binary refuses the new policy and configuration keys, so a rollback past this change needs `session` and
  `lifetimes.agent` removed first; recorded agent chains then become interactive (decision 3).
- 0001's class B (a relying party that keeps its own session) is unaffected: this widens what comes back to the
  issuer, not what never does.

## Alternatives considered

- **Raise `lifetimes.absolute` for everyone.** Rejected for the reason 0033 gave: it extends every console, every
  CLI and the browser sign-in to the agents' needs.
- **Mark write-capable resources `read_only` to use 0033.** Rejected: `read_only` is a claim the issuer cannot
  verify, and writing it where it is false spends the one signal that made 0033 safe. It would also stop at 7 days.
- **Keep the decision per resource** (0033's shape, extended to write paths). Rejected by the owner's decision 2:
  the same resource is reached by a person at a console and by a background host, and only the second needs to
  outlast a day.
- **Let a client document declare itself agent.** Rejected: the document is written by the client, and the class is
  a grant of time the installation makes, not one a client takes.
- **Complete agent authorizations silently, like any other.** Rejected by the owner's decision 5: a 30-day grant
  obtained without the person seeing it is the phishing case at its worst.
- **DPoP now.** Rejected for now: requiring it would lock out the MCP hosts this exists for. Recording the class on
  the session leaves room to require sender-constrained tokens for the class later.
- **Throttle repeated `invalid_grant` at an API gateway.** Rejected: it throttles by source rather than by token, so
  it would slow every host behind a hosted assistant's shared egress, and it exists on one platform only, while the
  process is the same on both ([0037](0037-one-process-everywhere.md)). A cache in the issuer works on both.
