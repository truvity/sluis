# The console's contracts

Console services, roles, routes and errors. The proto files under [`proto/`](../../../proto) are the source of truth.

Connect also speaks JSON over HTTP, so `curl` works. The proto package is `sluis.v1`; the legacy `directoryroster.v1` and `accessissuer.v1` stay served until v1.76. Tables write `<package>` for either hub package.

## Services

| Services | Reached by | Path prefix |
|---|---|---|
| `WorkspaceService`, `SettingsService`, `AccessService`, `GitHubService`, `SlackService`, `SlackChannelService`, `SlackSharedChannelService`, `SlackAppService`, `CloudflareService`, the SPA, and the audit `QueryService` forwarded under `/audit/` | the console, same origin under `console.mount`; a workload with its own ServiceAccount token | `/<package>.*/` |
| `sluis.v1.SessionService` | a browser at the issuer's host (SSO cookie); any caller with a token from this issuer | `/sluis.v1.SessionService/`, legacy `/accessissuer.v1.SessionService/` |
| `/login/*`, `/connect/*`, `/.access/*` | the origin root: bootstrap surface and CLI endpoints | none |
| `directory.v1.DirectoryService` | nothing: it has no listener and no chart value enables one | none |

## Authentication

| Caller | Proof | Roles | Failure |
|---|---|---|---|
| Workload | ServiceAccount token as bearer, audience `config.exchange.audience`, verified against the `exchange.clusters` key sets, never by TokenReview | policy `service_account` matchers put it in groups; the console reads `all:sluis:viewer` and `all:sluis:operator` | `401` with `WWW-Authenticate` |
| Person | session cookie (HttpOnly, signed with the session key) from a login route, or minted from a forwarded bearer behind a gateway | viewers group reads, operators group writes; `<directory-workspace-id>:sluis:viewer` and `:operator` scope the same roles to one directory | `unauthenticated`, `permission_denied` |

| Rule | Fact |
|---|---|
| Workload source | `workload`; never recovery's operator |
| No consumers declared | admits nobody |
| Outside a cluster | listener open, logs a warning at start |
| Token file | read on every call; the kubelet rotates it |
| Controllers | GitHub and Slack controllers share the console process and still call as workloads ([decision 0037](../../decisions/0037-one-process-everywhere.md)) |
| Roles | [console roles](console-roles.md) |

## Login and bootstrap routes

| Route | Does |
|---|---|
| `GET /login` | login page, one button per enabled source |
| `GET /login/<backend>/start`, then `/callback` | directory sign-in with `openid email profile`; only the address is taken; an address in no served domain, or absent from the directory, is refused |
| `POST /login/recovery` | recovery sign-in: ServiceAccount token (TokenReview) in a cluster, generated password elsewhere; bearer `Authorization: Bearer <proof>` is the runbook path |
| `GET /connect/<backend>/callback` | consent callback; authority is the signed state (cookie-pinned, ten minutes); failures render a `4xx` page, never `5xx` |
| `POST /logout` | clears the session |

| Recovery form post | Result |
|---|---|
| no page `state` or no `__Host-sluis_recovery` cookie | `400` |
| body over 16 KiB | `413` |
| JSON without `Content-Type: application/json` | not read |
| `curl -d proof=...` or proof in the query string | refused |

## Freshness

Every read returns `snapshot_at` and takes an optional `max_age` (`google.protobuf.Duration`). Model: [freshness](../../concepts/sluis/freshness.md).

| `max_age` | Behaviour |
|---|---|
| omitted | serve the current snapshot |
| a duration | refresh first when the snapshot is older |
| `0s` | fetch now |

| Call kind | Refresh path |
|---|---|
| Bulk: `ListGroups`, `GetGroup` | full workspace read, single-flight |
| Point: `ResolveUser`, `GetAccount`, `ResolveAccounts` | one account and its groups, live |
| Miss on an in-domain address | always live once before `found=false` |
| Failed fetch | stale snapshot with `authoritative=false`, never an error |

## Served domains and authority

| Term | Fact |
|---|---|
| Served | `serve` narrows the discovered domains; an unserved domain returns `in_domain=false` and keeps no accounts |
| Narrowing | `workspaces[].serve` for a declared workspace, `SetServedDomains` for a connected one; only discovered domains may be named |
| Contested | two workspaces both serve the domain |
| Moved domain | the old workspace stops serving it when discovery drops it; the stale entry shows `owned=false` and grants nothing |
| Authoritative | last probe succeeded, snapshot inside the freshness window, no other workspace serves the domain |
| Consumer rule | act on removals only when `authoritative=true`; non-authoritative "suspended", "not found" or "not a member" is a hold |
| `ListWorkspaces` words | `provisional` with reason `first_snapshot_pending`, `snapshot_stale` or `probe_failed`; `conflict` |

## DirectoryService

Schema of the directory answers; no listener serves it.

| Fact | Value |
|---|---|
| Compatibility | extends google-group-sync with additive fields, including `authoritative` |
| Not carried | REST routes `/users/{email}/groups`, `/groups`, `/groups/{email}` |

| RPC | Request | Response | Notes |
|---|---|---|---|
| `Describe` | none | `domains[]`, `backend`, `served[]{name, authoritative, workspace_id, backend, snapshot_at}` | `domains` and `backend` are legacy fields |
| `Probe` | `workspace_id?` | `healthy`, `detail`, `workspaces[]{workspace_id, healthy, detail, probed_at}` | empty id probes all |
| `GetGroup` | `email`, `max_age?` | `group{email, members[], domain}`, `found`, `authoritative`, `snapshot_at` | flat members, nested groups not expanded |
| `ListGroups` | `domain?`, `max_age?` | `groups[]`, `served[]` | empty domain is the union of served domains |
| `GetAccount` | `email`, `max_age?` | `account{email, in_domain, found, live, given_name, family_name, authoritative}`, `snapshot_at` | see the account table |
| `ResolveAccounts` | `emails[]`, `max_age?` | `accounts[]` in request order, `snapshot_at` (oldest) | each address routed on its own |
| `ResolveUser` | `email`, `max_age?` | `groups[]`, `suspended`, `in_domain`, `found`, `authoritative`, `snapshot_at` | login-time call |

| `in_domain` | `found` | `live` | Meaning |
|---|---|---|---|
| true | true | true | live account |
| true | true | false | suspended; gone if authoritative |
| true | false | any | deleted or absent; gone if authoritative |
| false | any | any | no opinion: domain not served here |

## WorkspaceService

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `ListWorkspaces` | viewer | none | `workspaces[]` | id, backend, `sync_groups`, `discovered_groups`, domains with authoritative/conflict/served/owned flags and `reason`, admin, credential type, connected_by/at, health, snapshot_at, declared |
| `BeginConnect` | operator | `backend` | `consent_url` | sets the state cookie; state names the operator |
| `Reconnect` | operator | `workspace_id` | `consent_url` | callback requires the same tenant, then replaces the credential |
| `UploadKey` | operator | `backend`, `key` (bytes), `admin` | `workspace` | domain-wide-delegation service-account key; creates or re-credentials |
| `SetServedDomains` | operator | `workspace_id`, `domains[]` | `workspace` | empty means all, including later ones; undiscovered domain is `InvalidArgument`; declared workspace is `FailedPrecondition`; excluded accounts leave the snapshot at once; the re-read is detached |
| `SetSyncedGroups` | operator | `workspace_id`, `groups[]` | `workspace` | empty keeps every group in served domains; unknown group is `InvalidArgument`; declared workspace is `FailedPrecondition` |
| `Probe` | operator | `workspace_id` | `health`, `domains[]` | credential check now, domains re-read |
| `Refresh` | operator | `workspace_id` | `snapshot_at` | full snapshot now |
| `GET /connect/google/callback?code&state` | route | | | verifies state cookie, exchanges code, discovers tenant and domains, probes, stores, redirects |
| `Disconnect` | operator | `workspace_id` | none | revokes at the backend, deletes the Secret and record; `failed_precondition` for a declared workspace |

## SettingsService

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `GetSettings` | viewer | none | `oauth_client{client_id, configured, source}`, `refresh_interval`, `freshness_window`, `probe_interval`, `cache_backend`, `connectors[]`, `key_connectors[]`, `version`, `setup[]{backend, redirect_uris[], scopes[]}` | never the client secret; intervals are chart values; no `SetOAuthClient` exists; `setup` lists two redirect URIs per backend, and a client missing one fails on that flow; `key_connectors` also accept an uploaded key |

## AccessService

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `WhoAmI` | any signed-in | none | `identity{email, subject, source, role, groups[], given_name, family_name}`, `version` | groups are internal groups from the policy |
| `Explain` | self: any; other: viewer | one proof: `email?`, `github{repository, owner, ref, workflow, environment, visibility}?` or `service_account{cluster, namespace, name}?` | identity, directory answer (`in_domain`, `found`, `suspended`, `authoritative`), `workspace_id`, `directory_groups[]`, `held[]{group, via[]}`, `claims`, `lifetime`, `clients[]{id, kind, requires[], admitted, lifetime}`, `policy_digest` | a consumer acting on `held` refuses a `policy_digest` other than its own |
| `GetPolicy` | viewer | none | `groups[]{name, members[]{address}, rules[]{kind, rule}, claims, lifetime, github_grants[]{app_id, app_name, org, repositories[], permissions{}, last_minted}}`, `clients[]{id, kind, requires[], redirects[], ttl_cap, secret}`, `teams[]`, `orgs[]`, `recovery_enabled`, `recovery_kind`, `login_sources[]` | a client names its Secret, never the secret; `github_grants` is read from the catalogue and makes no GitHub call; absent `last_minted` means not remembered, not unused |
| `ListHolders` | viewer | `group?` or `client?`, `limit?` | `holders[]{email, given_name, family_name, live, authoritative, via[], lifetime}`, `examined`, `truncated`, `policy_digest` | absence is not evidence: an unreadable snapshot contributes no accounts, so confirm with `Explain` before removing access; a group the policy lacks has no holders |
| `SearchPeople` | viewer | `query?`, `workspace_id?`, `domain?`, `account?` (live, suspended), `github?` (linked, not linked), `limit?` | `people[]{email, given_name, family_name, workspace_id, live, github_login}`, `total`, `truncated`, `github_known` | filters apply before the limit; `github_known=false` means links are unreadable, and filtering by `github` then fails |
| `ListServedDomains` | viewer | none | `directories[]{workspace_id, primary_domain, domains[]}` | served domains, lowercased and sorted; a scoped viewer sees its own directories |
| `ResolveDirectoryGroups` | installation-wide viewer | `groups[]` (at most 200), `users[]` (at most 200) | `groups[]{email, found, authoritative, workspace_id, members[]{email, given_name, family_name, known, live}, nested[], truncated}`, `users[]{email, workspace_id, found, live}`, `policy_digest` | nested groups expanded, each once, to at most 8 levels and 500 groups; `truncated` forbids removing on absence; a directory the caller cannot view returns `found=false` |
| `ListDirectoryGroups` | viewer | `domain?` | `groups[]{email, domain, workspace_id, members}` | picker source: the service's snapshots |
| `GetDirectoryGroup` | viewer | `email` | `email`, `domain`, `workspace_id`, `found`, `authoritative`, `snapshot_at`, `members[]{email, given_name, family_name, known, live}`, `feeds[]{group}` | `feeds` are internal groups naming this group; field 2 `layer` is reserved |

## GitHubService

Read-only. Roles apply installation-wide or over the owning directory; link App and links are installation-wide only.

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `GetGitHubStatus` | viewer | none | `organisations[]`, `reports_available`, and the fields below | every organisation the policy binds or the controller reports, bindings beside the report; a viewer of one directory sees only its organisations |
| `ListGitHubApps` | viewer | none | `apps[]`, `connecting_available`, `linking_available`, `catalogue_available`, `runner_tiers[]`, `bound_organisations[]`, `link_url`, `owner_choices[]`, `may_connect_without_owner` | all four App kinds in one shape; an undeclared App shows `declared=false`; write new code against this call |
| `GetGitHubApp` | viewer | `id` | `app` | `not_found` for an unknown id |
| `ListGitHubAppTokens` | operator | `id` | `tokens[]{at, subject, proof, grant, repositories[], permissions, outcome, reason}`, `kept`, `kept_since` | last requests, newest first, never the token; memory of this replica, lost on restart; `failed_precondition` for an App that mints none; `unavailable` is not "nothing asked" |
| `BeginGitHubAppConnect` | operator | `id`, `owner`, `owner_directory` | `url`, `manifest` | create or finish installing any App kind; `owner` applies to the link App only |
| `DisconnectGitHubApp` | operator | `id` | `uninstalled`, `detail`, `app_settings_url`, `invalidated` | forgets record and key even if uninstall fails; `invalidated` counts links, link App only |
| `CheckGitHubApp` | operator | `id` | `app` | asks GitHub again, bypassing the one-minute cache |
| `RotateGitHubAppWebhook` | operator | `id` | `app` | catalogue App only; waits for the new target to accept a signed ping; a failure before that restores the old secret |
| `BeginGitHubConnect` | operator | `org`, `owner_directory` | `url`, `manifest` | sets the state cookie; with `manifest`, POST it as form field `manifest` to `url`, else `url` is the install page; `failed_precondition` for an unbound or already installed organisation, or no Kubernetes state |
| `RequestGitHubPass` | owner operator or installation-wide | `org` | `requested_at` | writes `_pass.<org>.json`, noticed within 30 seconds; `failed_precondition` if the App is not installed; `resource_exhausted` within a minute of the last; audit `roster.github_org.pass_requested` |
| `ChangeGitHubOrganisationOwner` | installation-wide operator | `org`, `owner_directory` | none | empty removes; new owner must be a connected directory; audit `roster.github_org.owner_changed` |
| `DisconnectGitHubOrganisation` | operator | `org` | `uninstalled`, `detail`, `app_settings_url` | forgets record and key even if uninstall fails; the owner deletes the App at `app_settings_url` |
| `BeginGitHubLinkAppConnect` | operator | `owner` | `url`, `manifest` | public App, `emails: read` only, installed nowhere; `failed_precondition` if one exists or no Kubernetes state |
| `DisconnectGitHubLinkApp` | operator | none | `invalidated`, `app_settings_url` | self-links become unverifiable |
| `BeginGitHubRunnerAppConnect` | operator | `org`, `tier` | `url`, `manifest` | `invalid_argument` for a tier not in `githubRunnerApps.tiers`; `failed_precondition` for an unbound organisation or installed App |
| `DisconnectGitHubRunnerApp` | operator | `org`, `tier` | `uninstalled`, `detail`, `app_settings_url` | runners registered with it stop getting jobs |
| `BeginGitHubCatalogueAppConnect` | operator | `id` | `url`, `manifest` | `invalid_argument` for an undeclared id; `failed_precondition` if installed or no Kubernetes state. |
| `DisconnectGitHubCatalogueApp` | operator | `id` | `uninstalled`, `detail`, `app_settings_url` | works for an App the catalogue no longer declares; the App stays on GitHub |
| `CheckGitHubCatalogueApp` | operator | `id` | `app` | asks GitHub again, bypassing the one-minute cache |
| `ConfirmGitHubRemovals` | operator | `org`, `fingerprint` | none | `failed_precondition` if the report shows a different set; lapses after a day |
| `ImportGitHubLinks` | operator | `records[]{login, emails[], approved_by, approved_at}`, `origin` | `imported[]`, `skipped[]{login, reason}` | at most 500 records; each must be approved, vouched and live in the directory, and a member of a connected organisation; never displaces a person's link |

### Status and App fields

| Field | Contents |
|---|---|
| Ownership | `owner_directory`, `owner_domain`, `can_change_owner`; `owner_choices[]` and `may_connect_without_owner` on status and list |
| Owner rule | installation-wide operator names any connected directory or none; an operator of one directory owns what it connects; an operator of several chooses |
| `organisations[].connection` | `app_id`, `app_slug`, `installed`, `html_url`, `connected_at`, `connected_by`; never the key |
| Members | `{email, login, role, state, action, reason}`; state `not-linked`, `pending`, `invited`, `synced`, `leaving`, `held`, `retrying`, `ignored`, `reported`; action `invite`, `add`, `set-role`, `remove` |
| Organisation extras | `tick{at, outcome, error, changes, held, waiting, retrying}`, `teams[]`, `unlinked[]{login, reason}`, `seats{known, total, filled, pending, free, short}`, `breaker{affected, members, fingerprint, confirmed}` (a pass removing over half), `removal_confirmation`, `outside_collaborators[]`, `ignored[]`, `pass_requested_at` |
| Links | `links[]{account_id, login, emails[], state, reason, linked_at, checked_at, changed_at}`; state `linked`, `lost`, `unverifiable`; `source` `self`, `profile`, `imported`; `link_app`, `link_url`; never a token |
| Deprecated | `link_app`, `runner_apps[]`, `catalogue_apps[]`, `connection` stay filled for old clients; nothing new is added |
| App `id` | `link`, `<org>-controller`, `<org>-runners-<tier>`, or the catalogue id; a catalogue id wins a collision and the preset takes a `-preset` suffix |
| App enums | `purpose` `APP_PURPOSE_LINK`, `_CONTROLLER`, `_RUNNERS`, `_TOKENS`; `origin` `APP_ORIGIN_PRESET`, `_CATALOGUE`; `state` `APP_STATE_NOT_CREATED`, `_CREATED`, `_INSTALLED`, `_DRIFTED`; `attention` `APP_ATTENTION_DONE`, `_NEEDS_YOU`, `_WAITING_PERSON`, `_WAITING_CONTROLLER` |
| App fields | `id`, `org`, `purpose`, `tier`, `origin`, `name`, `app_slug`, `description`, `public`, `installation`, `repository_selection`, `state`, `attention`, `state_detail`, `declared`, `drift[]`, `permissions[]{name, declared, app, installation}`, `events[]`, `grants[]{group, repositories[], permissions{}, group_declared}`, `html_url`, `settings_url`, `app_id`, `installation_id`, `connected_at`, `connected_by`, `checked_at`, `reason`, `secret`, `secret_keys[]`, `linked_accounts`; never the key |
| Checks | every App is checked against its declaration; the link App is not (no key), and `reason` says so; `secret` is empty without Kubernetes state |
| Report | ConfigMap `<release>-github-status`, key `<login>.json` per organisation; the service creates it, the controller replaces data; `reports_available=false` only without Kubernetes state |

### GitHub redirects at the origin root

| Route | Role in the flow |
|---|---|
| `GET /connect/github/callback`, `GET /connect/github/setup` | after Create (exchanges the one-time code) and after Install (asks GitHub, as the App, where it is installed) |
| `GET /connect/github/runner/callback`, `.../runner/setup` | same, state also names the tier |
| `GET /connect/github/catalogue/callback`, `.../catalogue/setup` | same, state names the App id |
| `GET /connect/github/link-app/callback` | keeps the link App's client id and secret |
| `GET /connect/github/link`, `GET /connect/github/link/callback` | person-started link flow; links each verified address the directory vouches for |

Signed state must match the flow cookie. Failures are `4xx` pages. Link flows need no session.

## SlackService

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `GetSlackStatus` | viewer, installation-wide or of the owning directory | none | `reports_available`, `connecting_available`, `bot_scopes[]`, `redirect_url`, `owner_choices[]`, `may_connect_without_owner`, `workspaces[]` | never a client secret or bot token; a scoped viewer sees its own workspaces; `declared=false` marks a connected workspace the policy dropped |
| `BeginSlackWorkspaceConnect` | operator | `workspace`, `configuration_token?`, `owner?` | `url` | creates the Slack App from its manifest or prepares a reinstall; the token is used once, never stored or logged; needed to create and when scopes grew; sets a cookie pinning the browser |
| `RequestSlackPass` | owner operator or installation-wide | `workspace` | `requested_at` | writes `_pass.<workspace>.json`, noticed within 30 seconds; `resource_exhausted` within a minute; no audit action |
| `ChangeSlackWorkspaceOwner` | installation-wide operator | `workspace`, `owner` | none | empty owner removes; audit `roster.slack_workspace.owner_changed` |
| `DisconnectSlackWorkspace` | owner operator or installation-wide | `workspace`, `forget_anyway?` | `revoked` | revokes the bot token, forgets the connection; `forget_anyway` forgets even if Slack refuses revocation, and the token stays valid until the App is deleted in Slack; without it a refused revocation is `unavailable` and keeps the connection; channel records stay |
| `ConfirmSlackRemovals` | operator | `workspace`, `channel?`, `fingerprint` | none | empty `channel` is the workspace breaker; one confirmation covers every gate of that fingerprint; lapses after a day |

| Status value | Members |
|---|---|
| `connection_state` | `not_connected`, `created`, `installed`, `scopes_missing` |
| `tick.outcome` | `in-sync`, `applied`, `dry-run`, `held`, `retrying`, `waiting`, `failed` |
| Channel `state` | `ok`, `will-create`, `will-adopt`, `will-accept`, `waiting`, `held` |
| Member `state` | `ok`, `will-invite`, `will-remove`, `held`, `retrying`, `reported`, `ignored` |
| Member `action` | `create`, `adopt`, `invite`, `remove`, `share-invite`, `share-accept` |
| Workspace row | `workspace`, `team_id`, `owner`, `owner_domain`, `connection{app_id, app_settings_url, bot_user_id, granted_scopes[], connected_at, connected_by}`, `can_operate`, `can_change_owner`, `declared`, `reported`, `acting`, `tick`, `channels[]`, `leavers[]`, `breaker`, `removal_confirmation`, `needs_configuration_token`, `missing_scopes[]`, `pass_requested_at` |
| Channel row | `{name, id, private, mode, shared, host, state, reason, members[], breaker, removal_confirmation, console, sources[]}`; `console` marks a console channel, `sources` the internal groups of a git channel |

Report: ConfigMap `<release>-slack-status`, one key per workspace.

## SlackChannelService

Members come from the owning directory.

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `ListSlackChannels` | viewer of the owner | none | `available`, `channels[]`, `workspaces[]{key, can_operate, owner, discovered_more}`, `discovered[]`, `source_directories[]` | `state` is `not_reported`, `pending`, `active`, `held`, `invalid`; `discovered` is capped per workspace |
| `CreateSlackChannel` | owner operator or installation-wide | `channel{...}` | `channel` | record `_channel.<workspace>.<name>.json`; needs `sources` or `members`; `mode` `extend` (default) or `strict` (private only); `already_exists` for a name or channel id already managed, here or as Slack Connect; `invalid_argument` for an undiscovered id, a visibility mismatch, a person as a group or a group as a person; `failed_precondition` for a workspace with no owner; audit `roster.slack_console_channel.created` |
| `UpdateSlackChannel` | same | `channel{...}` | `channel` | `workspace`, `name`, `channel_id`, `private` are immutable; audit `roster.slack_console_channel.updated` |
| `DeleteSlackChannel` | same | `workspace`, `name`, `archive?` | `note`, `archived` | forgets the record, channel stays; `archive` pre-checks and calls `conversations.archive`; `FailedPrecondition` for dry-run, shared or invisible channels; `Unavailable` if Slack is down; audit `roster.slack_console_channel.deleted`, `roster.slack_channel.archived` |

## SlackSharedChannelService

Records `_shared.<name>.json`: `name`, `host` (immutable), `with`, `from`, `members`, `private` or `private_per_side`, `channel_id?`.

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `ListSlackSharedChannels` | viewer of host or a `with` workspace | none | `available`, `channels[]`, `workspaces[]`, `source_directories[]`, `discovered[]` | `state` adds `waiting` (guest to accept); `privacy` is `public`, `private`, `unknown`; `listed=false` means unknown |
| `CreateSlackSharedChannel` | host owner operator or installation-wide | `channel{...}` | `channel` | refuses duplicates and channels the policy or console already defines; needs `from` or `members`; audit `roster.slack_shared_channel.created` |
| `UpdateSlackSharedChannel` | same | `channel{...}` | `channel` | changes `with`, `from`, `members`, privacy; `host`, `name`, `channel_id` are refused; audit `roster.slack_shared_channel.updated` |
| `DeleteSlackSharedChannel` | same | `name` | `note` | forgets the record only; never archives; audit `roster.slack_shared_channel.deleted` |

## SlackAppService

Catalogue of Slack Apps (`slackApps`).

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `ListSlackApps` | viewer of the installation or owner | none | `available`, `apps[]{id, workspace, team_id, name, description, bot_scopes[], state, declared, app_id, app_settings_url, installed_team_id, installed_team_name, bot_user_id, granted_scopes[], missing_scopes[], created_at, created_by, installed_at, installed_by, needs_configuration_token, can_operate}` | `state` is `declared`, `created`, `installed`, `scopes_missing`; never a secret or token |
| `CreateSlackApp` | owner operator or installation-wide | `id`, `configuration_token` | `app` | the workspace must be connected first; audit `roster.slack_app.created` |
| `InstallSlackApp` | same | `id`, `configuration_token?` | `url` | token needed only to reinstall for missing scopes; audit `roster.slack_app.installed` or `.install_refused` |

| Route | Does |
|---|---|
| `GET /connect/slack/workspace/callback` | after a workspace install |
| `GET /connect/slack/catalogue/callback` | after a catalogue App install |

Both check the signed state and the role. A refusal is an audited `4xx` page.

## CloudflareService

Minted Cloudflare tokens and R2 credentials. Only `GetCloudflareCredential` returns a credential.

| RPC | Role | Request | Response | Notes |
|---|---|---|---|---|
| `ListCloudflare` | viewer | none | `available`, `accounts[]{name, id_last4}`, `presets[]`, `can_operate` | prototype `status` is `ok`, `active`, `forbidden`, `missing`, `unreachable`, read live; never a token value |
| `RotateCloudflarePreset` | operator | `preset` | `token_id`, `expires_on` | mints under the tick lease (`aborted` if held), sweeps expired; audit `roster.cloudflare.token.minted` |
| `RevokeCloudflareToken` | operator | `preset`, `token_id` | `replaced` | `not_found` for a token sluis did not mint; a revoked stored token is replaced; audit `roster.cloudflare.token.revoked` |
| `ListMyCloudflarePresets` | any signed-in | none | `available`, `presets[]{name, description, endpoint, lifetime_seconds}` | presets granted by `cloudflare.grants` |
| `GetCloudflareCredential` | granted by `cloudflare.grants` | `preset`, `lifetime_seconds?` | `preset`, `token_id`, `expires_on`, `token` or `access_key_id`, `secret_access_key`, `endpoint` | shown once, `Cache-Control: no-store`; audit `roster.cloudflare.token.minted` or `.refused` |

## SessionService

Served by the issuer. It only removes.

| Caller | Allowed |
|---|---|
| Own identity | SSO cookie or a token this issuer minted |
| Other identity or whole installation | `all:sluis:operator` |

| RPC | Auth | Request | Response | Notes |
|---|---|---|---|---|
| `ListSessions` | own identity; operator otherwise | `identity?`, `client_id?`, `contains?`, `page_size?`, `page_token?` | `sessions[]{id, identity, client_id, how, issued_at, expires_at, last_refreshed?, sso, session_class, deadline}`, `next_page_token`, `sign_ins[]{id, identity, how, auth_time, expires_at}` | newest first; `how` is `HOW_CODE`, `HOW_DEVICE`, `HOW_EXCHANGE`; `page_size` default 50, cap 500; `sign_ins` on page one only; operator also for client-only, global and `contains` listings |
| `RevokeSessions` | own identity; operator for anyone | `identity`, `client_id?`, `session_id?`, `sso?`, `every_identity?`, `scope?` | `ended` | `identity` required unless `every_identity` with `client_id` (operator, scope `client_every_identity`); `session_id` ends one, `sso` one browser's sign-in, `client_id` that client; none ends everything; unknown or foreign id returns `ended: 0`; idempotent; audit `roster.session.revoked` |

| `scope` (own identity, no id given) | Ends |
|---|---|
| `REVOKE_SCOPE_INTERACTIVE` | sign-ins and interactive sessions; agent sessions kept |
| `REVOKE_SCOPE_AGENTS` | agent sessions; sign-ins kept |
| unset, `REVOKE_SCOPE_EVERYTHING`, unknown | everything |

On another identity `scope` is ignored.

## Errors

| Service | Codes |
|---|---|
| `DirectoryService` | `invalid_argument` for an empty or unparseable address; `internal` for faults that are not a backend read; a failed backend read is a non-authoritative answer |
| `WorkspaceService` | `permission_denied`; `not_found` unknown id; `failed_precondition` operation refused for the workspace kind; `invalid_argument` unparseable key or admin without domain |
| `AccessService` | `unauthenticated` without a session; `permission_denied`; `not_found` undeclared group; `failed_precondition` deployment-owned state |
| `SlackChannelService`, `SlackSharedChannelService` | `aborted` when another edit changed the records first: reload and retry; `failed_precondition` with no Kubernetes state |
| `SessionService` | `unauthenticated` without a cookie or token; `permission_denied` for another identity, or a client-only, global or `contains` listing, without operator; `invalid_argument` for an empty `identity`, or `every_identity` with anything but `client_id` |

## Guides

| Topic | Page |
|---|---|
| Issuer endpoints | [endpoints](endpoints.md) |
| GitHub organisation | [connect a GitHub organisation](../../guides/sluis/connect/github-organisation.md) |
| GitHub Apps | [GitHub Apps catalogue](../../guides/sluis/connect/github-apps-catalogue.md) |
| Installation tokens | [GitHub App tokens](../../guides/sluis/connect/github-app-tokens.md) |
| Slack workspace | [connect a Slack workspace](../../guides/sluis/connect/slack-workspace.md) |
| Slack console channels | [Slack console channels](../../guides/sluis/connect/slack-console-channels.md) |
| Slack Connect channels | [Slack Connect channels](../../guides/sluis/connect/slack-connect-channels.md) |
| Slack Apps | [Slack Apps catalogue](../../guides/sluis/connect/slack-apps-catalogue.md) |
| Cloudflare | [Cloudflare tokens](../../guides/sluis/cloudflare-tokens.md) |
| Sessions | [revoke sessions](../../guides/sluis/operate/revoke-sessions.md) |
| Audit trail | [read the audit trail](../../guides/sluis/operate/read-the-audit-trail.md) |

## Audit trail

| Contract | Used for |
|---|---|
| `audit.v1.RegistryService` | sluis registers its catalogue at start |
| `audit.v1.SinkService` | sluis sends records with its projected ServiceAccount token |
| `audit.v1.QueryService` | the console forwards it under `<mount>/audit/` with a token minted for the signed-in person; only `POST` to its methods passes, else `404`; `401` signed out; `403` when the audit audience does not admit the person; `502` when unreachable |
| [`roster.yaml`](../../../internal/audit/catalogue/roster.yaml) | the action catalogue; table in [audit actions](audit-actions.md) |

## Installation tokens at `/token`

RFC 8693 exchange on `/token`. It applies when `requested_token_type` is the type below and `audience` starts `github-app:`.

`POST /token`, `application/x-www-form-urlencoded`:

| Parameter | Value |
|---|---|
| `grant_type` | `urn:ietf:params:oauth:grant-type:token-exchange` |
| `requested_token_type` | `urn:sluis:params:oauth:token-type:github-installation-token` (Go: `tokens.TypeSluisGitHubInstallationToken`); the old spelling `urn:access-roster:params:oauth:token-type:github-installation-token` (`tokens.TypeGitHubInstallationToken`) is accepted until v1.76. The response's `issued_token_type` repeats the one asked |
| `audience` | `github-app:<catalogue id>`, one |
| `subject_token` | GitHub Actions token for the issuer URL, federated ServiceAccount token, or sign-in access token |
| `subject_token_type` | `urn:ietf:params:oauth:token-type:jwt` (first two), `urn:ietf:params:oauth:token-type:access_token` (sign-in) |
| `repositories` | optional, names without owner, space separated, at most 500 |
| `scope` | optional, `name:level` pairs, space separated, such as `contents:read pull_requests:write` |
| `actor_token` | refused |

| Client authentication | Rule |
|---|---|
| Scheme | HTTP Basic, form-encoded per RFC 6749 §2.3.1 |
| A job | presents `github-app%3A<id>` with an empty password, or nothing |
| A sign-in | proves only for the client it was issued to, which declares `sign_in_exchange: true` |

The response is `200` with `Cache-Control: no-store`:

```json
{
  "access_token": "ghs_…",
  "issued_token_type": "urn:sluis:params:oauth:token-type:github-installation-token",
  "token_type": "N_A",
  "expires_in": 3599,
  "repositories": ["app", "lib-core"],
  "permissions": {"contents": "read", "pull_requests": "write"}
}
```

| Field | Fact |
|---|---|
| `access_token` | GitHub's installation token: `Authorization: Bearer`, or `x-access-token` as git password |
| `expires_in` | from GitHub's `expires_at` |
| `repositories`, `permissions` | what GitHub granted, not what was asked; `repositories` is absent when not narrowed |
| Decision | the first grant in catalogue order, for the proof's groups, that covers all of the request; named repositories must all match it; none requires a `["*"]` grant; GitHub gets the narrowing explicitly |

Errors are RFC 6749 JSON, `{"error": "...", "error_description": "..."}`:

| `error` | Status | When |
|---|---|---|
| `invalid_request` | 400 | malformed parameters, or `actor_token` |
| `invalid_client` | 401 | a presented client other than the audience failed to authenticate |
| `invalid_grant` | 400 | the subject token is not a proof |
| `invalid_target` | 400 | App undeclared, not created, not installed or uninstalled; or no grant names a group the proof holds |
| `invalid_scope` | 400 | wider than any one grant, or GitHub refused the narrowing (422) |
| `server_error` | 500 | GitHub or the App's key failed |

Each request is one `roster.github_token.minted` record ([fields](slack.md#audit)), never with the token.

## The whoami endpoint

`GET /.access/whoami` adds `roles`, `scopes` and `source` to the fields of `identity.WhoAmI`.

```json
{
  "status": "signed-in",
  "email": "alice@example.com",
  "name": "Alice Ant",
  "givenName": "Alice",
  "familyName": "Ant",
  "roles": ["operator", "viewer"],
  "scopes": ["C0north"],
  "source": "session",
  "groups": ["prod:k8s:viewer", "all:sluis:operator"],
  "version": "v1.8.0",
  "signOutUrl": "/logout",
  "issuerUrl": "https://access.example"
}
```

## Calling from a shell

```sh
# The projected ServiceAccount token, minted for config.exchange.audience, is the bearer.
TOKEN=$(cat /var/run/secrets/sluis/token)

# WhoAmI over GET (Connect's idempotent-GET encoding)
curl -s -H "authorization: Bearer $TOKEN" \
  'http://sluis.<namespace>.svc:8080/console/<package>.AccessService/WhoAmI?encoding=json&message=%7B%7D'

# ListHolders over POST
curl -s -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' \
  -d '{"group":"prod:k8s:viewer"}' \
  http://sluis.<namespace>.svc:8080/console/<package>.AccessService/ListHolders
```
