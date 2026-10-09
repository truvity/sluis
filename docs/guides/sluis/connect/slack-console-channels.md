# Manage Slack channels on the console

A console channel is an ordinary Slack channel kept as a record on the console and fed by directory groups and
individual addresses, rather than bound in git to internal groups. This page is how to create, edit, move and delete
one. How the controller treats the channel (adoption, modes, breakers) is in
[How a Slack pass decides](../../../concepts/sluis/slack-pass.md).

Two kinds of channel are fed by two kinds of group, and never mixed on one
channel:

| Kind | Where it is declared | Fed by |
|---|---|---|
| **Policy channel** | `slack.workspaces[k].channels` in git | **internal** groups. For channels the infrastructure owns, such as alert channels |
| **Console channel** | a record on the console, `_channel.<workspace>.<name>.json` | **directory** groups (IdP groups, by address), and individual addresses |

A console channel is **ordinary**: one workspace. A channel shared between
workspaces is the third kind, a **Slack Connect channel** (see
[Slack Connect channels](slack-connect-channels.md)). Both kinds of record are created and
edited on the console, audited, and backed up with the workspaces' own records
(`Secret <release>-slack-records`). The controller reconciles a console channel
with **the same rules as a policy channel**: created when missing, otherwise
adopted by name (or by the record's `channel_id`),
`extend` or `strict`, the directory vouches before anybody is removed, the
breakers hold a large removal set, and a hold is recorded once. Visibility is
never converted.

**The groups.** A source is a **directory group address** such as
`team@example.com`, of a directory sluis has connected. An ordinary
channel takes groups of **its workspace's owning directory** only (the
directory recorded as the workspace's owner when it was connected). Members are
resolved **through nested groups**: a member of a group that is itself a group
in a connected directory is expanded, each group once, so a cycle ends, to a
bounded depth. A chain the console cannot expand whole is reported as cut short
and the channel is refused for that pass, because nobody may be added or
removed on a read that is not whole. A member of a directory the console does
not read is not live and is not invited.

**Individual addresses.** Beside `sources`, a record may list `members`: people
by address, for the few who belong without being in any group. At least one of
`sources` and `members` is set; both may be. An ordinary channel takes users of
**its workspace's owning directory** only, by domain, and the directory must
actually know the user and the account must be active when the record is
written. Addresses are lowercased and none repeats. The console refuses, with
the reason: an address that is a **group** (enter it under *Directory groups*),
a group address entered as a person (enter it under *Individual addresses*), a
repeat, an address of another directory (*<address> is not a user of the
directory that owns this workspace (<owner id>) — individual addresses come
from the directories this channel draws from*), an address the directory does
not know, and an address that is suspended (only an active user can be listed).
The controller asks again at every pass and refuses the record
when an address is of another directory than the owner's. The people wanted are
the **union** of the groups' members and the individuals, mapped to people
exactly as group members are: the `people` aliases, the per-workspace address
choice, `users.lookupByEmail`, so somebody in a group and listed individually is
one person, invited once. An individual who is suspended or deleted in the
directory is a **leaver** like a group member: never added, reported on an
`extend` channel, removed by a `strict` one on the directory's say. One with no
Slack account yet is held, *no Slack account yet*, as a group member is. On the
console, the channel page and the Channels rows show both ("2 groups, 3
people"), and a person's page lists the channels that name them *individually*.

**Removals ask about the directory groups.** Where a policy channel asks the
directory whether somebody still holds an internal group, a strict console
channel asks whether they are still in one of its directory groups, or in a
group those nest. Somebody the directory still finds there is never removed,
however the member list came out; somebody it cannot vouch for waits (`retrying`).

**What is refused** (by the console when the record is written, and again by
the controller at every pass, because a directory can be disconnected or an
owner changed afterwards; the refusal is a held channel on the workspace's
report, with the reason, acted on by nobody):

- a source that is not a group of a connected directory, or, for an ordinary
  channel, of another directory than the workspace's owner;
- a channel the **policy already defines**, the same name in the workspace or
  the same channel id adopted by a binding: *this channel is defined in git;
  remove it there to manage it here*. If one gets through (the policy entry
  came later), both are held as *defined in both git and the console*;
- `strict` on a public channel, or an `ignore` list without `strict`;
- a workspace with no owning directory yet;
- a channel that is already managed the other way (an ordinary record and a
  Slack Connect record of the same host and name or id).

**What an edit may change.** An edit may change `mode`, `ignore`, the directory
groups and the individual addresses. It cannot change the workspace, the name,
the channel id or the visibility; the console refuses such a request and says to
create a new record and delete this one. Two people saving at once do not
overwrite each other: the write is made under the ConfigMap's version, and the
console says to reload when it cannot land. Saving a record wakes the controller: it takes
effect within a couple of minutes (see
[A pass runs promptly after an install](slack-workspace.md#a-pass-runs-promptly-after-an-install)).

**Discovery.** Every pass the report lists, per workspace, every channel the
bot can see that **neither a policy binding nor a record manages**: public
channels, and private ones the bot is in. `#general` and archived channels are
left out. The list is by name and capped at 500 per workspace, with a count of
the rest. On the Slack area's **Discovered** tab (`#/slack/discovered`) they are listed
with **Manage**, which opens the form prefilled with the workspace, the name,
the channel id and the visibility as seen; you choose the directory groups and
the mode. The tab also lists Slack Connect channels, and its filters (`kind`,
`visibility`, `sort`) are described under
[Slack Connect channels](slack-connect-channels.md#adopting-a-channel-that-is-already-shared).

**Archiving.** Deleting a record leaves the channel in Slack. When the
workspace's controller reports that it acts (it is in `policy.controllers.slack.enabledWorkspaces`), the
delete dialog has an opt-in, *Also archive #name in Slack* (off by default);
for a dry-run workspace the dialog says to archive in Slack by hand instead.
Before anything is changed the console checks, in order: the latest report says
the workspace acts (no report, an unreadable one, or a dry run refuses); the
channel's Slack id is known (the record's `channel_id`, else the report's);
and Slack itself, asked with `conversations.info` and the workspace's bot token,
says the channel is not shared. A Slack Connect channel (Slack's `is_ext_shared`,
or any other sharing flag it sets) is refused, because archiving closes it for
every organisation in it, whoever hosts it; so is a channel the bot cannot see
(a private one it is not in), and a Slack that does not answer. A refusal is
`FailedPrecondition` (or `Unavailable` when Slack did not answer), changes
nothing and leaves the record in place; you archive the channel in Slack by hand.
Only then is the record deleted and the bot's `conversations.archive` called,
audited as `roster.slack_channel.archived`. If that call still fails (the App
lacks the scope, or the workspace forbids a bot archiving), the record is
already deleted, the failed attempt is audited with outcome failure, and the
note says to archive the channel in Slack by hand.

**Who may.** Create, edit and delete: the operator over the workspace's owning
directory, or the installation-wide operator. A viewer sees the records and what
was discovered. Every change is audited as
`roster.slack_console_channel.created`, `.updated` or `.deleted`, with the
directory groups as targets of type `directory_group` and the individual addresses as
targets of type `directory_user` (audit catalogue 1.5.0; the data counts them as
`members`, and an edit's `changes` says `members: 2 -> 3 people (1 added, 0 removed)`).

#### Moving a policy channel to the console

To move a channel from git to the console: **remove it from the policy, then
Manage it from Discovered.** The console never manages a channel the policy
defines, and there is no take-over.

1. **Remove it from the policy in git** (the `channels` entry under its
   workspace). It becomes **unmanaged**: nothing is added to or removed from it
   while it is, and no removals happen.
2. After the next pass it appears on the Slack area's **Discovered** tab,
   because the bot is in it.
3. **Manage** it. The form is prefilled with the workspace, the name, the id and
   *private*; choose the mode (the same one keeps its behaviour), set its
   `ignore` list if it had one, and pick the directory groups as the sources.

Do these in this order. The form refuses a channel the policy still defines
(*this channel is defined in git; remove it there to manage it here*), and the
server refuses it the same way. If a record and a policy entry end up defining
the same channel anyway (the entry was added later, or the record is older),
**no mixing**: both are reported **held** (*defined in both git and the
console*) and nothing on the channel changes until one of the definitions is
removed. A record that still carries the retired `supersedes_policy: true`
loads with the field ignored and is never written with it again; once git no
longer defines the channel it is a plain console channel. If the new record's
group holds fewer people than the old internal group did, a strict channel will
remove the difference only once the directory vouches for each, and never past
the breaker: the first pass after the move is subject to it like any other.
