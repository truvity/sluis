# Revoke sessions

End somebody's access now: one session, one browser, one person, or one client for everybody. The session classes are in [people and agents](../../../concepts/sluis/people-and-agents.md).

## Before you start

- A person revokes their own sessions from their page in the console. An operator (`all:sluis:operator`, see [console roles](../../../reference/sluis/console-roles.md)) revokes anyone's. A viewer lists only their own.

- Access tokens already issued are JWTs and stay valid until they expire. For an agent that is at most `lifetimes.agent.access` (30 minutes by default).

- Credentials already obtained by token exchange stay valid until they expire. Shorten the exchange lifetime in the policy if that is too long.

- A client removed from the policy is stopped, not ended: its chains resume within their idle window if the row returns. Revoke its sessions before removing it.

## Choose the lever

| To end | Do this | Ends | Agent sessions |
|---|---|---|---|
| one session | *Revoke* on its row (person or client page) | that session | ended if it is one |
| one browser | *Sign out* on any session of that browser | the browser's sign-in and every session under it | ended |
| everything a person holds | their page: *Sign out everything* | all sessions of all classes and browser sign-ins | ended |
| a person's browsers and apps | their own page: *Sign out all browsers and apps* | interactive sessions and browser sign-ins | kept |
| a person's agents | their own page: *Disconnect all agents* | agent sessions | ended |
| one client for one person | *Revoke* on the client page's session row | that person's sessions on the client | ended |
| one client for everybody | the client page: *End for everybody*, operator only | every session of the client | ended |
| a person who has left | remove them from the directory | the next refresh of each chain is refused | ended |

On somebody else's page only *Sign out everything* is offered. An operator's revoke ends every class whatever scope was asked.

## Steps

1. Open the person's or client's page and list the sessions. Each row shows the client, its class, the deadline, the last refresh and the browser sign-in.

2. Pull the lever. Revoking zero sessions succeeds, and the answer says how many ended.

3. Check the audit trail. Each revoke writes `roster.session.revoked` with its scope (`everywhere`, `every_browser_and_app`, `every_agent`, `refresh_token_reuse`, ...). A sign-out writes `roster.session.ended` ([audit actions](../../../reference/sluis/audit-actions.md)).

A client that declares a back-channel logout address receives a logout token for each ended session.

## If the store cannot be read

Sign-out answers 503 and ends nothing, as an HTML page with a retry link or `temporarily_unavailable` as JSON. The cookie stays so the person can retry. A failed revoke says so: run it again when the store is back ([check health](check-health.md)).

## Afterwards

Remove the cause: take the person out of the granting group, [rotate the leaked credential](rotate-keys-and-credentials.md), or remove the client row. A revoked person who is still entitled can sign in again at once.
