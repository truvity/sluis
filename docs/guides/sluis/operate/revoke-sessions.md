# Revoke sessions

**Purpose.** End somebody's access now: one session, one browser, one person, or one client for everybody. Which lever
to pull depends on what you suspect and who is acting; the classes behind the choice are explained in
[people and agents](../../../concepts/sluis/people-and-agents.md).

**Who.** A person can revoke their own sessions from their page in the console (the account address redirects there).
An operator (`all:access-roster:operator`, see [console roles](../../../reference/sluis/console-roles.md)) can list and revoke
anyone's. A viewer can list only their own.

## Choose the lever

| You want to end | Do this | Ends | Agent sessions |
|---|---|---|---|
| one session | *Revoke* on the session's row (person page or client page) | that session | ended if it is one |
| one browser | *Sign out* on any session of that browser | the browser's sign-in and every session under it, including ones an earlier sign-out spared | ended |
| everything a person holds, you suspect a compromise | their page: *Sign out everything* | all sessions of all classes, and all browser sign-ins | ended |
| a person's browsers and apps only | their own page: *Sign out all browsers and apps* | interactive sessions and browser sign-ins | kept |
| a person's agents only | their own page: *Disconnect all agents* | agent sessions | ended |
| one client for one person | *Revoke* on the client page's session row | that person's sessions on the client | ended |
| one client for everybody | the client's page: *End for everybody* (asks to confirm) | every session of the client, whoever holds it; operator only | ended |
| a person who has left | remove them from the directory | the next refresh of each chain is refused and the session deleted | ended |

On somebody else's page only *Sign out everything* is offered: an operator's revoke ends every class whatever scope was
asked, and the two narrower scopes are a person's own choice.

## Steps

1. Open the person's page (or the client's) and list the sessions. Each row shows the client, its class, the computed
   deadline, when it was last refreshed and the browser sign-in it is filed under.
2. Pull the lever from the table. Revocation is idempotent: ending zero sessions is a success, and the answer says
   how many ended.
3. Check the audit trail. Every revoke writes `roster.session.revoked` with its scope (`everywhere`,
   `every_browser_and_app`, `every_agent`, `refresh_token_reuse`, ...) and, for the two scoped actions, the class that
   ended and the class that was kept; a sign-out writes `roster.session.ended` with the agent clients it spared (see
   [audit actions](../../../reference/sluis/audit-actions.md)).
4. Tell the affected relying parties if they matter: a client that declares a back-channel logout address is sent a
   logout token for each session that ended.

## What revoking does not reach

- **Access tokens already issued** are JWTs and stay valid until they expire: at most the token lifetime, and for an
  agent no more than `lifetimes.agent.access` (30 minutes by default).
- **Credentials already obtained by token exchange** (cluster, cloud and registry credentials) stay valid until
  they expire. Where that is too long, shorten the exchange's lifetime in the policy; revoking the session only stops
  new ones.
- **A client removed from the policy** is stopped, not ended: its chains are still listed, and would resume within
  their idle window if the row came back. Revoke the client's sessions before removing it.

## If the store cannot be read

Sign-out answers 503 and ends nothing (an HTML page with a retry link, or `temporarily_unavailable` as JSON), and keeps
the cookie so that the person can retry. A revoke that fails says so; do not assume it ended. Run it again once the
store is back, and see [check health](check-health.md).

## Afterwards

Remove the cause: take the person out of the group that granted the access, rotate the credential that leaked
([rotate keys and credentials](rotate-keys-and-credentials.md)), or remove the client row. A revoked
person can sign in again at once if they are still entitled.
