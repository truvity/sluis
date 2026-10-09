# The domain stores

How the console's and the controllers' records sit on State and Secrets, and why each choice was made. The logical
keys are in [keys](../../reference/sluis/keys.md); the contract of the ports is in [ports](../../reference/sluis/ports.md); the overview
is [ports and adapters](ports.md).

`internal/portstore` implements, on State and Secrets, the interface each
domain store already had, so business code did not change; `internal/app` picks
the implementation in one place (`openStores`): **any `ports.adapter` but
`legacy` keeps the domain records on the ports, `legacy` keeps the ConfigMaps
and Secrets of `internal/kube` unchanged** (a demonstration keeps its fixed
stores either way). The controllers read the same records the same way
(`controller.RecordSource` on the Slack side, `controller.AppSource` on the
GitHub side, both implemented by `portstore`) instead of the mounted
directories, and are woken by a poll of the records' revisions, which is the
digest of the mounted files made over keys and revisions (no value is read or
opened for it).

| Domain (interface) | Keys | In Secrets (`credentials/<kind>/<id>/<ref>`) |
|---|---|---|
| directory workspaces (`hub.Store`, `hub.CredentialStore`) | `ws.dir.<id>`: the record, naming the credential | the credential |
| GitHub organisations, the link App, confirmations, pass requests (`server.GitHubConnections`, `GitHubLinkApp`, `GitHubConfirmations`) | `gh.org.<org>`, `app.gh.link`, `gate.github.<org>.confirm`, `.pass` | the App key, the link App's client secret |
| a person's GitHub link (`server.GitHubLinks`, `controller.LinkStore`) | `gh.link.<account>`, `gate.github-claim.<account>` | the token pair |
| runner and catalogue Apps (`server.GitHubRunnerApps`, `GitHubCatalogueApps`, `SlackCatalogueApps`) | `app.gh.runner.<tier>.<org>`, `app.gh.cat.<id>`, `app.slack.cat.<id>` | the App key, or the client secret and bot token |
| Slack workspaces (`server.SlackWorkspaces`) | `ws.slack.<workspace>`, `gate.slack.<workspace>.…` | client secret and bot token |
| Slack Connect and console channel records (`server.SlackSharedRecords`, `SlackChannelRecords`) | `rec.slack.shared.<name>`, `rec.slack.channel.<workspace>.<name>` | — |
| the console's session key | `rec.console.session-key` | the key |
| `users.info` (`apply.MemberCache`) and the Slack Connect hand-off (`controller.Handoff`) | `cache.slack.user.<workspace>.<id>`, `share.<host>.<channel>` | — |
| the OAuth client (`settings.Store`) | none: see below | — |

- **Secrets, not sealing.** A credential is written to Secrets under
  `credentials/<kind>/<id>/<ref>` and the item in State names it by its ref; State never
  holds one. (The envelope-and-KMS sealing that was here is retired, together
  with the `sluis:binding` encryption context and `ports.sealer`.) The ref is
  fresh on every write, so a credential is never replaced in place: a writer
  that loses the compare-and-swap of the item has written a secret nobody
  names, which is removed, and cannot have replaced the one the winner's item
  names (a spent single-use refresh token never overwrites the new pair). The
  credential is written before the item that names it, and the one it replaces is
  removed after (best effort: a leftover is unreachable). The copy of the record
  inside the credential (which a restore read) and the Slack records' recovery
  mirror are not needed, and `ReconcileRecords` is a no-op there. A directory
  workspace's record is rewritten by every probe and carries its credential's
  name along untouched; a credential saved before its record is an item that is
  not listed. A read opens only what it needs: listing the organisations,
  workspaces or Apps never reads Secrets, and a link read with its tokens (the
  controller's check) costs one `Get` per self-link. Key characters a secret
  path does not allow (the `~XX` of a name) are written as `u-` and the
  segment's bytes in hex. Starting on an adapter set with no Secrets (the legacy
  one's, and DynamoDB's until a secrets adapter is chosen) **stops the start**,
  naming the secrets adapter, instead of failing on the first credential an
  operator connects. A record and its credential are no longer one item: the
  order above is what stands in for it.
- **Every update is a compare-and-swap** (`editRaw`): read, change, `Update(rev)`
  or `Create`, retried against what a concurrent writer left, 16 times. The
  stores that took `decide` callbacks (`Apply`) keep their contract: `decide` runs
  again on every retry, and a conflict that outlasts them is
  `ErrSharedConflict` or `ErrChannelConflict`.
- **A person's link is one item and one compare-and-swap.** `gh.link.<account>`
  holds the link, with its token pair named in Secrets. The link's own `Revision` (which a check
  uses so that a person who linked again meanwhile is never overwritten) is
  checked **under the key's revision**, so of two writers that read one link
  exactly one writes it. The refresh is the controller's two phase write, kept:
  (1) the `RefreshingSince` marker is written with `Update` at the revision read;
  (2) GitHub is asked for the new pair; (3) the pair is written, clearing the
  marker. A plain read-exchange-write would let two replicas exchange the same
  single-use refresh token, the loser's exchange being refused as a revoked
  authorization; the marker write is the claim, and only the replica whose swap
  lands may exchange. The replica that loses re-reads: when the winner has
  finished (the marker cleared, a pair that does not need renewing) it checks
  with the winner's pair, otherwise it leaves the link to the next pass. A crash
  between (1) and (3) leaves the marker, as before, and the next pass asks
  whether the old token still works. The test runs two controllers over one
  State, with a barrier that makes both read the same
  revision before either writes, and counts the exchanges at the fake GitHub:
  exactly one. With the compare removed it fails with two.
- **A claim spans keys, so it is steps with a marker.** `Claim` writes
  `gate.github-claim.<account>`, then the claimed link, then narrows each other
  account that held one of its addresses (each its own swap, re-reading the
  account first), then deletes the marker. A reader that finds the marker
  finishes the narrowing (`List` does), so a crash leaves an address proven by two
  accounts for a moment, never by none. `Adopt` and `Invalidate` are per-link
  swaps that re-check the account when they write; `Adopt`'s check across
  candidates is made against the links read at its start, so two `Adopt`s at once
  could both take one address (the kube store did it in one write); it is run by
  one actor.
- **The Slack Connect hand-off** is `share.<host>.<channel>`. After Slack accepts
  an invitation the host's tick writes the guest's side as `pending` (the write is
  the notification: it asks the guest's runner to tick through the Trigger, which
  crosses processes on a shared State; a lost notification is answered at the guest's next
  sweep), the guest's tick accepts the invitation and marks its side `accepted`,
  and the host reads it. The record lives 14 days while a guest is pending (as
  long as the invitation) and 7 days once every guest has accepted. The
  process-local hint stays when no hand-off is configured, and is used if the
  write fails. The tests run the host and the guest as two controllers over one State.
- **The `users.info` cache** answers `Observe`'s who-is-this lookup of a channel's
  other members, 24 hours, shared by every runner; the account of an address, who
  the token is, the channels and their members are read from Slack every pass,
  because a decision rests on them. A member who left is reported as a leaver up
  to a day late; nothing is removed on a cached answer, and a deactivated account
  is never cached. `slack_roster.user_cache{workspace,result=hit|miss}` counts it.
- **The shared inputs cache stays in memory.** What the Slack controller shares
  across a sweep (who holds each bound group, each directory group's members, what
  each directory serves, the decoded records) is a graph of the controller's own
  types, rebuilt from the console in one round of calls; serialising it under
  `cache.<digest>.<name>` is not cheap and buys one round of calls per runner per
  five minutes. A second replica reads the console as the first does. It is the
  first thing to move if the console's load shows it.
- **The OAuth client is an input, not a record.** `settings.Store` only reads the
  client the deployment declared (a mounted Secret, which the service never
  writes; the console's old write path and the memberships have been dead since
  they moved to policy), so it stays what it is: read from the declared Secret
  where a cluster is reachable, otherwise empty.
- **What is not atomic any more.** A console channel's check against every other
  channel's records (a channel id declared twice) is made on the listing read at
  the start of the write, not under one version of all of them as the ConfigMap
  gave: two creates at once of different names for one Slack channel could both
  pass. The names a record is kept under are what each swap protects.
- **Conformance.** `porttest` is unchanged: nothing generic was needed. The
  domain tests (`internal/portstore`, the two controllers and `internal/app`)
  run each store over **memory State and Secrets** (`portstoretest`), and open two
  "processes" onto one State where the point is a second replica.

