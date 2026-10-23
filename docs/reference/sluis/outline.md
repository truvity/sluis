# Outline reference

The policy table, the rows, the breakers, the console and the audit actions of the Outline controller.
Setup: [Connect and enable an Outline workspace](../../guides/sluis/enable-outline-workspace.md).

## The policy table

| Key | Means |
|---|---|
| `outline.workspaces.<key>` | One Outline workspace, by a key we choose: lowercase letters, digits and `-`, at most 40 |
| `client` | The sluis client the workspace signs in through. An account that reaches none of its required groups is suspended. Unset: an account must hold a group the workspace binds |
| `roles.admin` | Internal groups whose holders are workspace admins. Every other managed account is a member |
| `ignore` | Addresses never demoted, removed or suspended. The API key's owner belongs here. Joined across files |
| `collections.<name>` | One collection, by its name in Outline. Created private when missing |
| `read`, `read_write`, `admin` | Internal groups granted that permission (`admin` is "Manage"). A group at two levels takes the higher |
| `mode` | `extend` (default) only adds; `strict` also removes the group grants and direct members the policy does not name |
| `directory` | The connected directory, by workspace id or primary domain, that every bound group must come from |
| `adopt` | The id of the collection meant, for a renamed one or two of one name |
| `controllers.outline.enabledWorkspaces` | The workspaces the controller changes. The rest are dry runs |

Refused at load: a group or client nothing declares, a collection with no group, a collection declared twice (names compare without case), a malformed key, name, address or id, and `url`, `token`, `api_key` or `owner` on a workspace.

## Rows

| State | Means |
|---|---|
| `ok` | True already |
| `will-add`, `will-remove`, `will-change` | A member, a grant or a role is to change |
| `will-suspend`, `will-activate` | A leaver, or an account the directory has back |
| `will-create`, `will-adopt` | A managed group or a bound collection |
| `held` | Waits for a person; the reason says why |
| `retrying` | The directory could not vouch, or Outline refused or rate-limited the change |
| `reported` | Said, never acted on |
| `ignored` | On `ignore`, or the key's own user |

A workspace whose key's user is not an admin is held as a whole.

## Breakers

| Scope | Trips when a pass would |
|---|---|
| `group:<name>` | Remove over half of the group's members |
| `collection:<name>` | Remove over half of the collection's grants and direct members |
| `admins` | Demote more than one admin, or the last |
| `suspensions` | Suspend more than three accounts |

An operator confirms the fingerprint the latest report shows. A confirmation lapses after 24 hours.

## Findings

| Kind | Reported when |
|---|---|
| `setting` | Public sharing is on, members may create collections or invite, or the default role is not member |
| `share` | A document in a collection that names a directory is shared with someone that directory does not serve, or a group the collection does not bind. Read hourly (`controllers.outline.shareScan`) |
| `integration` | A Slack post integration sits on such a collection. Outline shows the channel, not the workspace |

## Calls to Outline

`auth.info`, `users.list` (`filter: all`), `users.update_role`, `users.suspend`, `users.activate`, `groups.list`, `groups.memberships`, `groups.create`, `groups.add_user`, `groups.remove_user`, `collections.list` (`includeListOnly`), `collections.memberships`, `collections.group_memberships`, `collections.create`, `collections.add_group`, `collections.remove_group`, `collections.remove_user`, `documents.list`, `documents.memberships`, `documents.group_memberships`, `integrations.list`. Lists page at 100. A 429 ends the tick or its changes; the next pass goes on.

## Where it is kept

| What | Where |
|---|---|
| Connection record | State, kind `outline-workspace` of the `oidc` module |
| API key | `internal/oidc/outline/<workspace>/<ref>` |
| Confirmations, pass requests | State, kind `outline-gate` of the `oidc` module |
| Reports | Blob prefix `oidc/reports/outline/` |

## Console and API

SYSTEMS, Outline (`#/outline`) shows each workspace, its report, the breakers with **Confirm**, **Refresh**, **Connect** and **Disconnect**. `OutlineService`: `GetOutlineStatus`, `ConnectOutlineWorkspace`, `DisconnectOutlineWorkspace`, `RequestOutlinePass`, `ConfirmOutlineBreaker`. See the [contracts](contracts.md).

## Metrics

| Metric | Labels |
|---|---|
| `outline_roster.passes` | `workspace`, `outcome` |
| `outline_roster.changes` | `workspace`, `action`, `outcome` |
| `outline_roster.rows` | `workspace`, `state` |
| `outline_roster.reported` | `workspace`, `kind` |
| `outline_roster.breaker_trips` | `workspace` |

## Audit

| Recorded by | Actions |
|---|---|
| controller | `roster.outline_group.created`; `roster.outline_member.added`, `.removed`; `roster.outline_role.changed`; `roster.outline_collection.created`, `.adopted`, `.granted`, `.revoked`, `.member_removed`; `roster.outline_user.suspended`, `.activated`; `roster.outline_action.held`; `roster.outline_breaker.tripped` |
| console | `roster.outline_workspace.connected`, `.disconnected`; `roster.outline_breaker.confirmed` |

## Decided in

- [ADR 0077](../../decisions/0077-the-outline-controller-is-hosted-by-the-issuer.md): the Outline controller, hosted by the issuer
