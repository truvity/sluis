# The console

## One origin

The issuer sits at the **root** of the domain and the console takes a path beside it. That is protocol, not taste:
the issuer URL is the `iss` claim in every token, and discovery lives at `/.well-known/openid-configuration` at an
origin root. An issuer with a path component relocates its discovery URL to a place Kubernetes and AWS handle badly.

What the shape buys is that the console is **same-origin**: its session pages call the issuer with the browser's own
cookie, and the question of how a console reaches sessions in another origin stops existing.

The prefix is stripped in-process rather than by the gateway, which surfaces the one thing that is easy to miss: the
console's handlers never see the prefix, but every link they hand a **browser** has to carry it, because `/login`
resolves against the origin, where the issuer's page is, not the console's. The mount is read from the address the
console is published at rather than configured a second time.

The **admin-consent callback stays at the origin root**. It is the one flow that runs before anybody can be signed
in, and its redirect URI is registered with every corporate tenant, so moving it under the console's path would mean
re-registering it in each of them.

Renaming an issuer invalidates every token in circulation, which is why the decision was made while exactly one
client trusted it.

## It signs people in as a client of the issuer

Somebody with no session is sent to `/authorize` with the console's own declared client, signs in once at the
issuer's login page, and comes back with the issuer's session cookie set, which the console then reads, the way it
reads anybody's. One door, and the console holds nothing special. There is no proxy in front of the console running
an OpenID flow against a service in the same process, and no login of the console's own: the second door an
installation with a gateway turns off.

The code the flow hands back is **never redeemed**. What the console needed was the session the flow established, not
a token: it reads the directory and the policy in this same process. Redeeming would mean holding something it has no
use for and either a client secret to keep or a verifier to carry across the redirect. The code expires unused and
is stripped from the URL, so it reaches no bookmark and no referrer. How to wire it: [the console app](../how-to/connect/console-app.md).

## What it shows

The console **reads**. It shows every person, every directory group, every internal group, every rule that grants
one, every open session, and every GitHub organisation and Slack workspace with what its controller would change:
the whole chain from a directory to a client, and why each link exists.

A person's page states that chain as one line per internal group held, not just the group's own name: the directory
group or matcher at the root, a [mapping wildcard](../reference/taxonomy.md#mapping-wildcards) named when one is the
reason (`*:k8s:admin`, not just `devel:k8s:admin`), and, once a
[declared vocabulary](../reference/policy.md#vocabulary) puts groups in an
[implies ladder](../reference/taxonomy.md#inheritance), every hop back to the group that was actually granted, one
direct parent at a time rather than a root the reader has to trust:
`stage:k8s:viewer ← implied by stage:k8s:operator ← implied by stage:k8s:admin ← wildcard *:k8s:admin ← directory
group sre@example.com`. The whole chain is already in the one `Explain` answer the page reads (`policy.Held` carries
the granting key and the direct parent for every group it names), so the console walks it client-side.

## What it changes

Bootstrap, removals and confirmations, never policy:

- **Connect** a directory by admin consent; a GitHub organisation by its owner creating and installing an App, the
  link App people authorize, and a **runner App** per organisation per tier for self-hosted runners; a **Slack
  workspace** by pasting a throwaway app configuration token and sending a workspace owner through Slack's install,
  and a catalogue Slack App the same way. Each genuinely needs a browser and no credential of theirs can be obtained
  as code; what they produce (a refresh token, an App's private key, a bot token) is what this process writes for
  itself.
- **Keep records**, and only these: a console channel (`_channel.<workspace>.<name>.json`) and a Slack Connect
  channel (`_shared.<name>.json`). They are membership definitions for Slack channels fed by directory groups and
  individual addresses, never by internal groups; they are validated against the policy and the connected
  directories, audited, and never grant access to infrastructure.
- **Archive** a console channel in Slack, opt-in, when forgetting its record. Never a Slack Connect channel.
- **Request a pass** (Refresh): a marker in the records, at most one a minute per Slack workspace or GitHub
  organisation.
- **Revoke a session.** A removal, and the lever between sign-out and expiry. Disconnecting a directory, an
  organisation or an App is the same kind of removal: it revokes at the other side, then forgets.
- **Confirm** a set of removals the controller held because it concerned more than half an organisation, and
  **import** GitHub links approved elsewhere. Both let the controller act on something it could not decide alone;
  neither adds anybody to a group.

It cannot change who is in an internal group. Operator therefore means *may connect*, *may revoke*, *may confirm*,
*may keep Slack channel records* and *may request a pass*, each over the installation, or over the one directory that
owns the organisation or Slack workspace concerned; everything else is a viewer.

## The Sessions page

A person's page lists their sessions grouped under the sign-in that opened them, one group per browser. A sign-in that
has ended, because the person signed out, stays listed while it still holds sessions, marked as a signed-out browser:
those are the agent connections the sign-out kept. Each row shows the client, its class (`interactive` or `agent`) and
its deadline, the computed end of the chain, beside the sliding expiry. *End this browser* ends the sign-in and every
session under it, spared agent sessions included; a row's own revoke ends only that session.

On your own page there are three buttons for everything at once. *Sign out all browsers and apps* ends every browser
sign-in and interactive session and keeps your agent connections; *Disconnect all agents* ends every agent connection
and keeps you signed in; *Sign out everything*, the primary button, ends both and is the one to press if you think
your account is compromised. Somebody else's page offers only *Sign out everything*, which is what an operator's revoke
does whatever it asks.

A client's page lists its sessions across people. Beside the per-person revoke it offers, to an operator only, *End for
everybody*, which ends that client's sessions for every identity (`every_identity` on `RevokeSessions`) and is audited
as `roster.session.revoked` with scope `client_every_identity`. It is the lever for one client when removing it
from the policy would only stop its chains.

## Navigation

Navigation is the model, in four clusters under *Overview*: **IDENTITY** (Directories, Directory groups, People,
Rules); **ACCESS** (Internal groups, Clients, Sessions); **SYSTEMS** (GitHub, Slack, one entry each, with tabs of
their own); **ADMIN** (Audit, Settings).

**GitHub** is a page on the internal side, beside clients, because a GitHub team consumes internal groups the way a
client does. Four tabs, in the order the work happens. *Overview* says what needs attention next: a card per
organisation, and the people who have not linked, with their addresses to copy. *Organisations* opens each one: what
enabling it would do in one sentence, removals first, every person with a row that reads **OK**, **waiting for them**
or **needs you** with the controller's exact state in the tooltip, and each team with a page. *Apps* holds the link
App, every organisation's controller App and every catalogue App, one row each with a page of its own; *Runners*
holds the runner Apps. A disabled organisation is still derived every pass, so its page is the dry run an operator
reads before enabling it. Nothing on it writes to GitHub; the report is read from the controller's published status
document, and a report that is missing or unreadable hides none of the bindings.

**Slack** has *Workspaces* (the connection, owning directory, team, install state and last pass of each), *Channels*
(every managed channel across workspaces in one table: the policy's, read-only and *defined in git*, with the
internal groups that feed them; the console's, editable, with their directory groups and mode), *Slack Connect*
(host, sides and per-side state; create and edit), *Discovered* (every visible channel nothing manages, each with
Manage) and *Apps* (the catalogue). The Channels, Slack Connect and Discovered tabs share one filter bar, and every
selection is in the address query, so a filtered view is a link. Channel states are ok, pending, waiting, held,
invalid and not reported. **A channel has a page**: where it comes from (every source a link), the state of every
person in it and why, each side of a Slack Connect channel, and its history, which is the audit trail narrowed to the
channel's target.

The reverse edges are drawn from the same reports and records, with no call of their own: a directory group's page
lists the Slack channels it feeds, per workspace, and the GitHub teams it feeds through the internal groups; a
person's page lists their channels per workspace with the state and reason (*waiting for them: no Slack account
yet*) and shows them on GitHub, with a button to link your own account.

Every page reads in the same direction, from the identity side toward the access side, and the two group pages carry
the same sections mirrored. The visual vocabulary has one meaning per form, which is what keeps a data-dense page
readable: a name is a link, monospace when it is an identifier; a chip is a state and nothing else is; facts are a
label over a value; two-column data is a list and tabular data is a table.

## The issuer's own HTML

Four things run before there is anyone to authorize, so none of them can be a console page. Everything else a person
sees is the console.

| | |
|---|---|
| `/login` | the sign-in chooser: one button per provider kind, under the name of the application being signed in to and the host it returns to, all of it from the declared policy and the validated request, none of it from the query string |
| `/logout` | the sign-out a person follows, needing no `id_token_hint` |
| `/signed-out` | where a sign-out lands when the client declares no page of its own |
| a refusal | what `/authorize` and `/end_session` show when they cannot send the person onward |

**The refusals are pages because of where they are reached.** Both endpoints redirect a browser back to the relying
party when they can, which is the specification's answer. What is left is the case where there is nowhere safe to
send somebody (an unregistered `redirect_uri`, a broken `id_token_hint`), so they stay here, and a page that names
the service is better than the library's unstyled `http.Error`.

**These pages have to be re-read when behaviour changes**, because nothing fails when they go stale. The signed-out
page once told people their other consoles kept running "until those expire" after sign-out had started revoking
them, and what caught it was a conformance screenshot, not a test.
