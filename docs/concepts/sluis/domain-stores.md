# How do the domain records sit on State and Secrets?

See [keys](../../reference/sluis/keys.md), [ports](../../reference/sluis/ports.md) and [ports and adapters](ports.md).

`internal/portstore` implements each domain store on State and Secrets, and `internal/app` picks the implementation in one place (`openStores`). Any `ports.adapter` but `legacy` keeps the domain records on the ports. `legacy` keeps the ConfigMaps and Secrets of `internal/kube`.

The controllers read the same records through `controller.RecordSource` (Slack) and `controller.AppSource` (GitHub). A poll of the records' keys and revisions wakes them, and opens no value.

| Domain (interface) | Keys | In Secrets (`credentials/<kind>/<id>/<ref>`) |
|---|---|---|
| directory workspaces (`hub.Store`, `hub.CredentialStore`) | `ws.dir.<id>`: the record, naming the credential | the credential |
| GitHub organisations, the link App, confirmations, pass requests (`server.GitHubConnections`, `GitHubLinkApp`, `GitHubConfirmations`) | `gh.org.<org>`, `app.gh.link`, `gate.github.<org>.confirm`, `.pass` | the App key, the link App's client secret |
| a person's GitHub link (`server.GitHubLinks`, `controller.LinkStore`) | `gh.link.<account>`, `gate.github-claim.<account>` | the token pair |
| runner and catalogue Apps (`server.GitHubRunnerApps`, `GitHubCatalogueApps`, `SlackCatalogueApps`) | `app.gh.runner.<tier>.<org>`, `app.gh.cat.<id>`, `app.slack.cat.<id>` | the App key, or the client secret and bot token |
| Slack workspaces (`server.SlackWorkspaces`) | `ws.slack.<workspace>`, `gate.slack.<workspace>.…` | client secret and bot token |
| Slack Connect and console channel records (`server.SlackSharedRecords`, `SlackChannelRecords`) | `rec.slack.shared.<name>`, `rec.slack.channel.<workspace>.<name>` | none |
| the console's session key | `rec.console.session-key` | the key |
| `users.info` (`apply.MemberCache`) and the Slack Connect hand-off (`controller.Handoff`) | `cache.slack.user.<workspace>.<id>`, `share.<host>.<channel>` | none |
| the OAuth client (`settings.Store`) | none: see below | none |

## Credentials live in Secrets

A credential is written to Secrets under `credentials/<kind>/<id>/<ref>`, and the State item names it by its ref. State never holds one. The ref is fresh on every write, so a credential is never replaced in place.

A writer that loses the compare-and-swap of the item removes the secret nobody names. A spent single-use refresh token never overwrites the new pair. The service writes the credential first, then the item that names it, then removes the old one, best effort.

Listing organisations, workspaces or Apps never reads Secrets. Reading a link with its tokens costs one `Get` per self-link. A character a secret path disallows, the `~XX` of a name, is written as `u-` and the segment's bytes in hex.

An adapter set with no Secrets stops the start and names the missing secrets adapter. That covers the legacy set, and DynamoDB until you choose one.

## Every update is a compare-and-swap

`editRaw` reads, changes, then calls `Update(rev)` or `Create`, retrying 16 times against what a concurrent writer left. A `decide` callback (`Apply`) runs again on every retry. A conflict that outlasts them is `ErrSharedConflict` or `ErrChannelConflict`.

## A person's link is one item

`gh.link.<account>` holds the link and names its token pair. The compare on the link's own `Revision` runs under the key's revision, so only one of two writers that read one link writes it.

The refresh is a two-phase write, because GitHub refresh tokens are single use:

1. Write the `RefreshingSince` marker with `Update` at the revision read. The replica whose swap lands owns the exchange.

2. Ask GitHub for the new pair.

3. Write the pair and clear the marker.

The losing replica re-reads. If the winner finished, it checks with the winner's pair, and otherwise leaves the link to the next pass. A crash between steps 1 and 3 leaves the marker, and the next pass tests the old token.

## A claim spans keys

`Claim` runs these steps with a marker:

1. Write `gate.github-claim.<account>`.

2. Write the claimed link.

3. Narrow each other account that held one of its addresses, each its own swap.

4. Delete the marker.

A reader that finds the marker finishes the narrowing, as `List` does. A crash leaves an address proven by two accounts for a moment, never by none.

`Adopt` and `Invalidate` are per-link swaps that re-check the account when they write. Two concurrent `Adopt` calls could take one address, so one actor runs it.

## The Slack Connect hand-off

`share.<host>.<channel>` carries the hand-off. After Slack accepts an invitation, the host's tick writes the guest's side as `pending`. The write asks the guest's runner to tick through the Trigger. The guest's next sweep answers a lost notification.

The guest's tick accepts the invitation and marks its side `accepted`. The record lives 14 days while a guest is pending and 7 days once every guest has accepted. The process-local hint applies when no hand-off is configured or the write fails.

## Caches

The `users.info` cache answers the who-is-this lookup of a channel's other members for 24 hours, shared by every runner. Everything else is read from Slack every pass.

A member who left shows as a leaver up to a day late. The service removes nobody on a cached answer and never caches a deactivated account. The metric `slack_roster.user_cache{workspace,result=hit|miss}` counts it (legacy identifier, renamed in v1.75–v1.76).

The shared inputs of a Slack sweep stay in memory. A second replica reads the console as the first does.

## Limits

- The OAuth client is an input, not a record. `settings.Store` reads it from a mounted Secret the service never writes.

- A console channel's duplicate check runs on the listing read at the start of the write. Two concurrent creates of different names for one Slack channel can both pass.

- The domain tests run each store over memory State and Secrets (`portstoretest`). Where a second replica matters, they open two processes onto one State.

## Decided in

- [ADR 0027: The State port](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md)
- [ADR 0028: Nothing writes ConfigMaps or Secrets](../../decisions/0028-nothing-writes-configmaps-or-secrets.md)
- [ADR 0041: The secret contract](../../decisions/0041-the-secret-contract.md)
