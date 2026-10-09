# The GitHub controller

The GitHub controller is a loop inside the sluis process, enabled by `controllers.github` in the service document
([one process](one-process.md)). It holds GitHub App keys and writes to GitHub, and neither belongs in the login
path, so it has no listener and no console of its own. See [Connect a GitHub organisation](../../guides/sluis/connect/github-organisation.md)
for the operator's side.

What each GitHub team should contain is the policy's `github` table: internal groups per team, in both of GitHub's
roles. Which accounts hold those groups is the console's to answer, and the controller asks it over the console's API
with the process's own ServiceAccount token (no exchange in front of a same-cluster call). A GitHub account is
matched to a person by the work addresses GitHub verified on it, which the person shows by authorizing a link App
(GitHub discloses members' addresses to no organisation outside its Enterprise Cloud plan), so nobody types a GitHub
username, and nobody else keeps a mapping. Two more ways exist, and neither displaces a link the person made: an
account whose public profile shows a work address the directory has is matched, because GitHub lets an account
publish only a verified address; and a pairing approved elsewhere is imported through an operator RPC, after three
checks. A self-link is checked on GitHub every pass, and an account whose link GitHub says is gone leaves the
organisation at once; a profile match or an import holds no token of the App's and is not re-checked.

## The loop

The loop is `internal/rails`' (the Slack controller's too): a pass every interval, and a pass at once when the watch
sees a change. Every 30 seconds the controller hashes its credentials and records and reads the
`_pass.<organisation>.json` markers an operator's **Refresh** leaves there. A changed hash, or a marker newer than
the last one acted on, wakes the loop; the markers are never deleted, and those that exist at start are answered by
the first pass. The pass is always the full one, because the reports are published as one document set. The request
is rate limited to one a minute per organisation and audited.

What a pass derives and changes, what it holds for a person, and what it never touches are in
[how a GitHub pass decides](github-pass.md). Two rules from it carry the design. Removal needs one more question than
addition: absence from a holders list is never evidence (an unreadable workspace contributes nobody), so each removal
is confirmed by asking about that one address, and done only on an answer the directory vouches for. And the
controller stops itself where a person is needed: a removal set over half an organisation waits for an operator to
confirm exactly that set, and an organisation is born disabled, its report the dry run an operator reads before
adding it to `policy.controllers.github.enabledOrgs`.

## Policy digest

Every answer the console gives carries the digest of the policy it was computed under, and the controller changes
nothing on an answer under another policy. During a rollout, replicas run different policies, and a team the new
policy binds looks to an old replica like one nobody holds. Such a pass is tried again within seconds, not after an
interval, a bounded number of times, and then falls back to the interval. A failed pass is reported over the last
report with rows, so the page does not blank while passes fail; a restarted controller takes what it had already
recorded from that report, so a restart is not news in the audit trail.

It writes GitHub and its report, records what it did to the audit installation as itself, and pushes metrics over
OTLP when a collector is named. It holds no session or refresh-token store of its own.

## Reconciler rails

A reconciler is a loop that, every pass and for every target the policy binds, reads what a system holds, asks the
console who should hold it, decides, acts where the target is enabled, and reports. GitHub organisations are the
first; Slack workspaces are the second ([the Slack reconciler](slack-reconciler.md)). Both are built on
`internal/rails`, which holds what they call with the same meaning:

- the pass loop and its backoff: a pass that met a console on another policy is tried again within seconds a bounded
  number of times, then left to the interval;
- the directory half: who holds each group and what is true of one address, every answer gated by the policy digest,
  and the removal rule that somebody is removed only on an answer the directory vouched for (an address not asked, or
  not vouched for, settles nothing this pass);
- the held-once ledger, so a held row is audited when it becomes held and not every pass or after a restart;
- the journal of each target's last good report, so a failed pass reports its failure over what was known;
- the confirmation loop (`Confirm`), asked one address at a time under the removal rule;
- the removal circuit breaker and its fingerprint, which is the unit of operator confirmation: one confirmation
  satisfies every gate the fingerprint covers, and the dry-run switch a target is born behind.

`internal/githubroster` and `internal/slackroster` each own everything shaped like their system: teams, logins,
invitations, seats, channels, deriving and deciding what a target should look like, the change calls, status
documents and audit events. The rails take funcs and small interfaces rather than a generated client, so they import
nothing of either system.

This is deliberately not a reconciler framework
([ADR 0024](../../decisions/0024-reconciler-rails-are-shared-pieces-not-a-framework.md)). The pieces above are the ones
a second reconciler was shown, by being written, to call identically. The rest (the row and action types, the pass
skeleton, the status document, the metrics) differ in kind, or are exported names that must stay identical to what
one system already publishes, and a shared shape for them would be a guess fitted to one system and bent to the
other. A piece moves here when a second reconciler needs it unchanged, not before. Ticks per target under a lease:
[ADR 0029](../../decisions/0029-ticks-per-target-under-a-lease.md).

Where the reports and records live: [Kubernetes objects](../../reference/sluis/kubernetes-objects.md) and [the store](store.md).
