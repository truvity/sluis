# People and agents

sluis issues tokens to two kinds of caller that look alike on the wire and differ in what they are. A **person** sits at
a browser: they can be asked to sign in again, and when they sign out they mean it. An **agent** is software that holds
its own refresh token and works in the background (an MCP host, a bot, a connector): nobody is at a browser when its
token needs refreshing, and a console sign-out somewhere else was never meant to stop it. Treating both the same way
fails one of them, so a client declares which it is. The decision and its reasoning are
[0040](../../decisions/0040-agent-class-sessions.md); this page is what it means to run.

## Two client classes

The class belongs to the **client**, not to the resource it reaches, and only the installation's policy sets it.

| | interactive (the default) | agent (`session: agent`) |
|---|---|---|
| who holds the chain | a person at a browser, or a CLI they run | software with its own credential store |
| chain ends at | `lifetimes.absolute` (24h) from sign-in; idle at `lifetimes.refresh` | 30 days from sign-in; idle at 14 days (`lifetimes.agent`, up to 90 days) |
| access and ID token | the installation's token lifetime and the usual caps | never longer than `lifetimes.agent.access` (30 minutes by default, 1 hour at most) |
| completing an authorization | silent if the person is signed in | never silent: the person accepts on a consent page |
| a person's own browser sign-out | ends it | keeps it running |

The effective deadline of an agent chain is the sign-in time plus the shorter of the class limit and the resource's
`absolute_cap`; the console shows the computed deadline, never a nominal 30 days. The class is recorded on the session
when it opens, so a later policy change can shorten a chain but never lengthen it or change its class. How to declare it
is in [policy clients](../../reference/policy-clients.md#agent-class-sessions).

A client document cannot declare itself an agent: the issuer reads the class from the installation's policy, never from
a fetched document. `client_documents.session: agent` gives the class to every document client of the admitted origins,
so use it only for origins whose documents their vendor controls.

## Why an agent's authorization is never silent

A 30-day grant obtained without the person seeing it is the phishing case at its worst: someone could start an
authorization for their own client and send the victim the link. So after the person authenticates they are shown a
page naming the client, its origin, where it returns to, the class and the deadline, and the connection is made only
when they accept it in their own browser. The accept is a POST bound to that request and that browser, enforced where
every sign-in converges, and the page refuses to be framed. `prompt=none` for an agent client answers
`consent_required`. The cost is about one click per 30 days per connection.

## Three sign-out scopes

A person ending their own sessions chooses how much:

| Action in the console | Ends | Keeps |
|---|---|---|
| *Sign out all browsers and apps* | every browser sign-in and every interactive session | agent sessions |
| *Disconnect all agents* | every agent session | the browsers and their interactive sessions |
| *Sign out everything* | every session of every class and every browser sign-in | nothing |

*Sign out everything* is the lever for a suspected compromise. An unknown or missing scope means *everything*: a scope
only ever ends less by being understood. The scope is a feature of a person's **own** action. Everything an operator
does, and every automatic end, ends every class:

- an operator's revoke of one person, of one client for one person, of one browser, or of one client for everybody;
- removal from the directory, and an authoritative "not live" answer at a refresh;
- refresh-token reuse.

The plain sign-out (`/logout`, `end_session`) is a fourth, narrower thing: it ends the person's interactive sessions and
keeps agent sessions, and the signed-out page says so. `end_session` that names an agent client ends that client's
sessions too. A sign-in that passes its own absolute limit keeps every live chain, and another person signing in in the
same browser keeps nothing.

## How long an agent outlives a removal

Every refresh re-checks the directory, so a person removed from it loses their agents at the next refresh. The bound is
the access-token cap plus the directory's refresh interval (15 minutes by default) when the directory is healthy, and
the cap plus its freshness window (30 minutes by default) when snapshot refreshes fail while the probe looks healthy.
After that the last-known groups are held for `lifetimes.hold` (4 hours) in an outage. Revoking the person ends every
chain at once and forgets the held groups; see [revoking sessions](../operations/revoke-sessions.md).

## What this does not do

- No sender-constrained tokens (DPoP) yet: a refresh token stolen from an agent's credential store is useful until the
  chain's deadline. Rotation with reuse detection ends the family when both thief and host present it.
- Removing a client or an origin from the policy only **stops** its chains, because the client is then unknown at
  refresh; it does not end them. Revoke the client's sessions first.

The session machinery underneath (sign-ins, the SSO cookie, back-channel logout, reuse detection) is in
[sessions and sign-out](../../explanation/sessions.md).
