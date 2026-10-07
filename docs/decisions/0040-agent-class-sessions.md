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
   directory (seen at the next refresh) and on refresh-token reuse detection.

## Decision

**1. Policy shape.** A declared client may say `session: agent`; absent is `interactive`. `client_documents` may say
`session: agent` for **every** document client, beside its `requires` and `ttl_cap`. A client document cannot
declare its own class: the issuer never reads a class from the fetched document, only from the installation's
policy, so a hostile document on an allowed origin gets exactly what the block grants and no more. An installation
that needs one document client interactive and another agent declares one of them as a client row. Refused at load:
`session` on an `exchange` client (an exchange opens no chain with an `auth_time`), and `session: agent` together
with `sign_in_exchange: true` (that client is a person's CLI, and a month-long chain there would make every cluster
and cloud credential traded from it a month long in effect).

The class's lifetimes live in the service configuration, next to the others, because they are an installation's
dial like `lifetimes.absolute`, and because the policy's `lifetimes` table is keyed by group name, where `agent`
would collide with a group:

```yaml
lifetimes:
  absolute: 24h          # interactive, unchanged
  agent:
    refresh: 336h        # idle limit, 14 days
    absolute: 720h       # 30 days from auth_time
    access: 30m          # mandatory access-token cap for the class
```

Refused at load, in the style of `Resource.CheckAbsoluteCap`: `agent.absolute` above **90 days** (2160h) or not
positive; `agent.refresh` above `agent.absolute`; `agent.access` above **1h** or not positive. Shortening is always
allowed. The 90-day ceiling exists so that a typo cannot turn the class into "never signs in again".

The idle limit stays at 14 days: it is what ends an abandoned credential store (an uninstalled host, a laptop in a
drawer), and 14 days is half the absolute limit, so a host used once a fortnight lives its full 30 days and one left
for longer does not.

**2. Effective lifetime of a chain.** End = `min(now + class.refresh, auth_time + class.absolute,
auth_time + absolute_cap of the chain's resource)`. For an agent chain a resource's `absolute_cap` is only ever a
ceiling, never a lengthening. The access token's `exp` is `min(lifetimes.token, the client's ttl_cap,
client_documents.ttl_cap, the resource's ttl_cap, lifetimes.agent.access)` and never past the chain's end. Today
`client_documents.ttl_cap` reaches a document client's ID token but not its access token (`Storage.issue` looks the
client up among declared clients only); this change set closes that.

**3. The class is recorded on the session.** `Session` (`internal/issuer/session.go`) gains `class` and the chain's
`deadline` (`auth_time + class.absolute`), both written by `Sessions.Record` at authorization from the client's
class at that moment, and never re-derived on refresh. `absoluteOf`, `pastLimit`, `capEnd`, `spentLifetime` and the
token's `exp` cap read the recorded class. A later policy or configuration change can therefore **shorten** a chain
(the current `lifetimes.agent.absolute` or a resource cap still bound it at the next refresh, as 0033 already does
for a withdrawn cap) but can never **lengthen** it past its recorded deadline or change its class. A client moved from
`agent` to `interactive` keeps its agent chains until they end or are revoked per client; a client moved the other
way gets agent chains at its next authorization. A chain recorded before the upgrade has no class and is
interactive, exactly as a session without `Method` is evaluated as a person.

**4. The read-only exception is deprecated.** With agent class in place, 0033's lengthening (an `absolute_cap` above
`lifetimes.absolute` on a `read_only` resource) has no remaining user: the connectors it was written for are agent
clients. Two mechanisms for one need is one too many, and the class answers it for write paths as well. Migration:
mark the connector clients `session: agent`, then remove `absolute_cap` from those resources (or lower it to at most
`lifetimes.absolute`). The release that ships agent class accepts the old rows with a load-time warning naming
`session: agent`; a later minor release refuses `absolute_cap` above `lifetimes.absolute` and `read_only` with it,
marked **Breaking:** under [0007](0007-breaking-changes-inside-1x.md). `absolute_cap` as a shortening cap stays.

**5. Bound on revocation latency.** An agent chain never outlives a directory removal, as the issuer sees it, by
more than its last access token's lifetime: every refresh re-checks the directory (`Storage.entitled` in the refresh
path of `internal/issuer/storage.go`), so after the removal no new access token is minted, and the exposure is the
token already issued. Because `lifetimes.agent.access` is mandatory and capped at 1h, this holds for an agent client
with no resource or with a resource that sets no `ttl_cap`; with one, the bound is the shorter `ttl_cap`. Two
qualifications apply to every class equally and are accepted here as they are for interactive clients. The issuer
sees a removal when the hub's directory snapshot does (`freshness.refreshInterval`, 15 minutes by default). And
while the directory cannot be reached, `Resolver.Resolve` (`internal/issuer/resolve.go`) answers with the last-known
groups for `lifetimes.hold` (4h) from the last authoritative answer, so a removal made during an outage is not seen
until the directory answers or the hold runs out. `Issuer.Revoke(identity)` forgets the held groups and ends every
chain at once, which is the operator's lever when that window is too long.

**6. Sign-out spares agent chains; revocation never does.** `endSignIn`'s `sparingLive` flag becomes a per-session
predicate. At `/logout` and `end_session` it spares a live session whose recorded class is `agent`; at the absolute
limit of the browser sign-in (silent `/authorize` and the console) it spares every live chain, as today. Spared
sessions are not revoked, are not announced by Back-Channel Logout (`announceLogout`), and stay filed under the
ended sign-in's id, so the console's "revoke one browser" still reaches them. **Invariant: nothing that revokes
consults the class.** `Issuer.Revoke(identity)`, `RevokeSessions` by identity, by client, by browser or by id,
refresh-token reuse, and a refused refresh after a directory removal end or stop every class alike; another person
signing in in the same browser ends the previous person's sessions of every class. Sparing exists only where a
browser sign-in ends by its own sign-out or its own limit.

The audit record of a sign-out, `roster.session.ended`, gains `spared`: how many agent sessions it left running,
beside `ended`. That is a catalogue change, so the version moves from 1.9.0 to 1.10.0 with its released fixture,
following [change the audit catalogue](../how-to/change-the-audit-catalogue.md); a Lambda audit writer needs the new
catalogue release asset, schemas included, before it can keep records of that version.

**7. The console** lists agent sessions with their class and their deadline beside the sliding expiry, and offers
the per-client revoke it already has. `accessissuerv1.Session` gains an additive `session_class` field.

**8. Cheap `invalid_grant`.** A host whose chain has ended keeps presenting the dead refresh token, often in a loop,
and each presentation costs two to four State reads (`Sessions.present`, then `endedByAbsoluteLimit`). On a refresh
refused as an invalid refresh token the issuer logs at WARN the client id the request names and a fingerprint: the
first 8 hex of an HMAC-SHA-256 of the token under the installation's state secret where one is configured, otherwise
under a random key drawn at start (a fingerprint then correlates within one process only). The token itself is never
logged. An in-process negative cache, keyed by the **full** HMAC (never the 8-hex prefix, whose collisions would
refuse a valid token), bounded in size, holds a dead token for 5 minutes, so a repeat costs 0 State calls. It is fed
only by consistent reads that found no pointer, a pointer to a session no longer live, or a spent mark whose session
is gone, and only after the refusing path has done its work (the absolute-limit refusal still audits once).
**Invariant: a spent mark that is still inside its 30-second grace window, or in the 2-second tolerance band past it,
or whose session is live (a reuse that must reach `endReuse`), is never cached**, nor is any read that failed. A
token is dead monotonically, so nothing needs invalidating.

## Consequences

- Agent hosts stop asking for a daily sign-in, and a console sign-out stops breaking them. The cost is that a
  person's sign-out no longer means "everything I opened has ended": the console has to show the agent chains that
  survived, and the sign-out page should say that some were kept.
- **Threat model.** A refresh token stolen from an MCP host's credential store is useful for up to 30 days (at most
  14 days idle). Mitigations: the directory re-check on every refresh, rotation with reuse detection (a stolen token
  used by both the thief and the host ends the family, whichever presents first), per-client revocation, *sign out
  everywhere*, the audit trail, and the access-token cap that bounds each minted token. Residual: no DPoP or other
  sender-constrained tokens yet, because MCP clients do not widely support them (revisit when they do); a thief who
  takes the token from a host that never presents it again (uninstalled, lost, wiped) keeps the chain alive by
  refreshing, since reuse detection fires only when both present; and malware living on the host can take each new
  token as it is minted. Only policy authors mark a client `agent`, through the reviewed policy, and for document
  clients only the installation's `client_documents` block does, per installation rather than per document. A
  phishing document on an allowed origin still gets a 30-day chain if a person consents to it, which is why the
  sign-in page states the class and its duration for an agent client.
- Spent marks are kept until the family's deadline (`spentLifetime`), so an agent chain refreshing every 30 minutes
  accumulates up to about 1440 small marks over its 30 days, against 48 for a 24-hour interactive chain.
- An older binary refuses the new policy and configuration keys, so a rollback past this change needs `session`
  and `lifetimes.agent` removed first; chains recorded with a class are then evaluated as interactive by the older
  binary and end at its 24-hour limit at their next refresh.
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
- **DPoP now.** Rejected for now: requiring it would lock out the MCP hosts this exists for. Recording the class on
  the session leaves room to require sender-constrained tokens for the class later.
- **Throttle repeated `invalid_grant` at an API gateway.** Rejected: it throttles by source rather than by token, so
  it would slow every host behind a hosted assistant's shared egress, and it exists on one platform only, while the
  process is the same on both ([0037](0037-one-process-everywhere.md)). A cache in the issuer works on both.
