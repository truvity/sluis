# The Slack reconciler

The second reconciler makes each Slack workspace's channels contain the people who hold the groups bound to them. It
runs as a loop in the sluis process (`controllers.slack`), on the same [rails](github-controller.md#reconciler-rails)
as the GitHub controller. Operator side: [Connect a Slack workspace](../../guides/sluis/connect/slack-workspace.md),
[Slack Connect channels](../../guides/sluis/connect/slack-connect-channels.md) and
[Slack Apps](../../guides/sluis/connect/slack-apps-catalogue.md).

It lives in `internal/slackroster`: `reconcile` (the decision, no network), `status` (the report document the console
reads), `connection` (the per-workspace record, credential, and the console's channel, Slack Connect, confirmation
and pass-request records), `apply` (reading a workspace and carrying out a decision through the Slack client),
`controller` (the loop: the pass, the 30-second credential and request watch, the directory questions, the guest-side
probe and the metrics) and `app` (the OAuth connect and install flow). The Slack API client is `internal/slackapp`,
with a fake beside it for tests.

**Nothing is declared that the product already knows.** The owner is recorded in the connection record when the
workspace is connected (the installation-wide operator chooses; an operator of exactly one directory owns what they
connect), the team is recorded at the first install and every later install must match it, and a person is looked up
by the served domains of the owning directory, read from the console every pass. The policy keeps what is policy: the
workspace key, `channels` and `people`. A policy that still carries `team_id`, `domains` or `owner` is refused at
load, naming where the value now comes from.

## Two kinds of channel, never mixed

A *policy channel* is bound in git to internal groups. A *console channel* is a record fed by directory groups of the
workspace's owning directory and by individual addresses (`members`, each an active user of that directory); the
desired membership is the union, resolved through nested groups (cycles end, depth and size are bounded, and a read
cut short refuses the channel for the pass). One channel is managed one way: the console refuses a record for a
channel the policy binds (by name, or by adopted id), and a channel defined both ways is held on both sides, *defined
in both git and the console*, and nothing on it changes until one definition is removed. There is no take-over from
git: to move a channel to the console, remove it from the policy and Manage it from Discovered
([ADR 0020](../../decisions/0020-hold-on-double-definition-instead-of-taking-over.md),
[ADR 0019](../../decisions/0019-two-kinds-of-slack-channel-never-mixed.md)).

## Channels, modes and breakers

A bound channel is an idempotent upsert, created or adopted by name; a mismatch it will not fix (visibility, an
archived channel, a name it cannot see) is held, never forced. `extend` only adds; `strict` also removes, and only on
an answer the directory vouches for, so an unreadable directory removes nobody. Two breakers stop a removal set over
half of a channel or of the workspace until an operator confirms its fingerprint. A person the directory no longer
has is reported, never acted on by that fact alone. The details of a pass, with the held states, are in
[how a Slack pass decides](slack-pass.md); the owner, team and domains of a workspace and who a person is in it are
there too.

## Slack Connect channels

They are an input list of definitions (name, host, the workspaces it is shared `with`, sources: directory groups of
any connected directory, and individual addresses, visibility as one bool or per side), not something the policy
declares: the console manages them as records
([ADR 0021](../../decisions/0021-slack-connect-channels-are-console-records.md)). The record keeps the channel's Slack
id when it was found rather than created. The host creates the channel and invites each guest workspace's bot; a
guest accepts the pending invitation for that channel from the host's team and only that. Then each side invites only
its own people: a person joins from the host when they have a host-domain address, otherwise from the first `with`
workspace, in order, where they have one, otherwise the host holds them. A side waiting for the other is a `waiting`
state, not a hold. A bot that does not list a side is asked once per pass, by id with `conversations.info`, but only
for a channel a record manages, and only the workspaces Slack names as guests or, when Slack names none, the
workspaces the record names as sides. A channel no record manages is never probed
([ADR 0023](../../decisions/0023-guest-side-probe-only-for-managed-slack-connect-channels.md)).

## What it never does

It creates no Slack account, touches no user group, invites or removes no guest, removes nobody from a public
channel, never converts a channel's visibility, never unarchives, never creates a second channel under another name,
and never removes anyone the directory has not vouched for. It acts only in workspaces listed in
`policy.controllers.slack.enabledWorkspaces`; every other workspace is derived and reported.

**A read is whole or it is nothing.** A missing `users:read.email` scope, a rate limit that outlasts every retry, a
failed page: any of them fails the whole workspace's read, and nothing is decided or changed on it. An address never
looked up, or a channel member nobody identified, is an error in the decision too, so a partial read can never look
like a workspace in which nobody has an account.

## What is written

`status.Workspace` is one versioned JSON document per workspace: per channel, per person, `ok`, `will-invite`,
`will-remove`, `held` (with the reason), `retrying`, `reported` or `ignored`; the two breakers; the leavers; and the
last pass's time and outcome. A workspace's connection is two objects for the reason a directory's is: a record the
console shows, and a credential (client id and secret, bot token) that only the controller reads. The token is empty
between creating the app and installing it, which is its own state, and the client secret is kept because a
scope-upgrade reinstall needs it. The object names are in [Kubernetes objects](../../reference/sluis/kubernetes-objects.md).
