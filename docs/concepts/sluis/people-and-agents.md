# How do people and agents differ?

sluis issues tokens to two kinds of caller. A person sits at a browser and can be asked to sign in again. An agent is
software with its own refresh token, such as an MCP host, a bot or a connector. Nobody is at a browser when its token
refreshes, and a console sign-out elsewhere is not meant to stop it. A client declares which kind it is.

## Which class does a client belong to?

The class belongs to the client, not to the resource it reaches, and only the installation's policy sets it.

| | interactive (default) | agent (`session: agent`) |
|---|---|---|
| holder | a person at a browser, or a CLI they run | software with its own credential store |
| chain ends | `lifetimes.absolute` (24h) from sign-in; idle at `lifetimes.refresh` | 30 days from sign-in; idle at 14 days (`lifetimes.agent`, up to 90 days) |
| access and ID token | the installation's token lifetime and the usual caps | at most `lifetimes.agent.access` (30 minutes by default, 1 hour at most) |
| authorization | silent if the person is signed in | never silent: the person accepts on a consent page |
| the person's browser sign-out | ends it | keeps it running |

An agent chain ends at the sign-in time plus the shorter of the class limit and the resource's `absolute_cap`. The
console shows the computed deadline. The class is recorded when the session opens. A later policy change can shorten a
chain but never lengthen it or change its class. To declare a class, see [policy clients](../../reference/sluis/policy-clients.md#agent-class-sessions).

A client document cannot declare itself an agent, because the issuer reads the class from the policy. Setting
`client_documents.session: agent` makes every document client of the admitted origins an agent. Use it only for origins
whose documents their vendor controls.

## Why is an agent's authorization never silent?

A 30-day grant the person never saw is the worst phishing case. Someone starts an authorization for their own client and
sends the victim the link. After the person authenticates, a page names the client, its origin, its return address, the
class and the deadline. The connection is made only when they accept in their own browser.

The accept is a POST bound to that request and that browser. The page refuses to be framed. `prompt=none` for an
agent client answers `consent_required`. The cost is about one click per 30 days per connection.

## What do the three sign-out scopes end?

| Console action | Ends | Keeps |
|---|---|---|
| Sign out all browsers and apps | every browser sign-in and interactive session | agent sessions |
| Disconnect all agents | every agent session | browsers and their interactive sessions |
| Sign out everything | every session of every class and every browser sign-in | nothing |

Sign out everything is the lever for a suspected compromise. An unknown or missing scope means everything.

The scope applies to a person's own action. An operator's revoke ends every class, whether of one person, one client
for one person, one browser or one client for everybody. So do removal from the directory, an authoritative "not live"
answer at a refresh, and refresh-token reuse.

The plain sign-out (`/logout`, `end_session`) ends the person's interactive sessions and keeps agent sessions. An
`end_session` that names an agent client also ends that client's sessions. A sign-in that passes its absolute limit
keeps every live chain.

## How long does an agent outlive a removal?

Every refresh re-checks the directory, so a removed person loses their agents at the next refresh. With a healthy
directory the bound is the access-token cap plus the directory's refresh interval (15 minutes by default). When snapshot
refreshes fail while the probe looks healthy, it is the cap plus the freshness window (30 minutes by default). In an
outage the last-known groups are held for `lifetimes.hold` (4 hours).

Revoking the person ends every chain at once and forgets the held groups: [revoke sessions](../../guides/sluis/operate/revoke-sessions.md).

## What does this not do?

DPoP is not served, so a refresh token stolen from an agent's store works until the chain's deadline. Rotation with reuse
detection ends the family when both thief and host present it.

Removing a client or origin from the policy stops its chains at the next refresh but does not end them. Revoke the
client's sessions first. The session machinery is in [sessions and sign-out](sessions.md).

## Decided in

- [ADR 0040](../../decisions/0040-agent-class-sessions.md): agent-class sessions
