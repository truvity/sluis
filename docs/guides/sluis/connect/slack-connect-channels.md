# Slack Connect channels

Create, edit and delete Slack Connect channels between the installation's workspaces on the **Slack Connect** tab (`#/slack/connect`).

## Before you start

- The host and every `with` workspace are in `slack.workspaces`, and the host's policy does not bind that name or id.

- To change the host or name, create a new record and delete the old.

- The channel is always `extend`: nobody is removed. Set `config.store: kubernetes`.

## 1. Write a record

| Field | Meaning |
|---|---|
| `name` | Lowercase letters, digits, `-`, `_`, at most 80. Unique, immutable |
| `host` | The workspace that creates and owns the channel. Immutable, never in `with` |
| `with` | The other workspaces, in order. At least one, none repeated. Order decides where a person without a host-domain address joins |
| `from` (`sources` when stored) | Directory groups by address, of any connected directory. Never an internal group |
| `members` | Individual addresses, active users of any connected directory. Set at least one of `from` and `members` |
| `channel_id` | Optional: an existing shared channel to adopt. Set by **Manage**, identical on every side, immutable |
| `private` | One visibility for all sides, or one per side naming the host and every `with` workspace |

A refused record is `invalid` with the reason. A person joins on the side whose owning directory serves their domain, the host first; someone on no side is held. A record whose `from` names internal groups is `invalid`: edit or delete it.

## 2. Let the controller reconcile

1. The host's controller creates the channel and invites the guest's bot (`roster.slack_shared.invited`).
2. The guest's controller accepts (`roster.slack_shared.accepted`). Until then the record shows *waiting for acceptance*.
3. Each side adds its own people. A leaver is reported (`roster.slack_leaver.reported`); remove them in Slack.

A workspace acts only if `policy.controllers.slack.enabledWorkspaces` names it. Host-directory operators and the installation-wide operator may create, edit and delete; a guest's operator may not. An edit takes effect within about two minutes.

## 3. Adopt a channel that is already shared

A channel is *managed* when a record matches it by channel id, else by host and name. On **Discovered** (`#/slack/discovered`) every connected workspace is a side. A side whose bot cannot see the channel shows **unknown**; add it by hand.

Filters are query parameters, for example `#/slack/discovered?workspace=<key>&kind=shared&q=ops`:

| Tab | Parameters |
|---|---|
| Discovered | `workspace`, `kind` (`ordinary`, `shared`), `visibility` (`public`, `private`, `unknown`), `q`, `sort` (`members`) |
| Slack Connect | `host`, `side`, `state` (`active`, `waiting`, `pending`, `held`, `invalid`, `not_reported`), `q` |

Press **Manage** and pick the directory groups. The controller adopts by `channel_id`: a public side is joined, and a private side is held until you invite the bot. A *visibility mismatch* hint means **Edit** the record.

For a managed channel only, the controller probes each workspace that did not list it with `conversations.info`, once per pass. Errors never fail the pass.

## 4. Delete

Deleting removes the record only. The channel and its people stay in every workspace. The console never archives a Slack Connect channel.

## Verify

The record is `_shared.<name>.json` in `ConfigMap <release>-slack-workspaces`, mirrored in `Secret <release>-slack-records`. Audit actions are `roster.slack_shared_channel.created`, `.updated` and `.deleted`.

Decided in [ADR 0021](../../../decisions/0021-slack-connect-channels-are-console-records.md) and [ADR 0023](../../../decisions/0023-guest-side-probe-only-for-managed-slack-connect-channels.md).
