# Console roles

Two roles, held by membership of two declared groups, and the same two scoped to one directory.
The groups are in the [policy](policy.md); the methods are in the [contracts](contracts.md).

| Role | Group | May |
|---|---|---|
| viewer | `all:access-roster:viewer` | Every read: `ListWorkspaces`, `GetSettings`, `GetPolicy`, `WhoAmI`, `Explain`, `ListDirectoryGroups`, `GetDirectoryGroup`, `SearchPeople`, `ListHolders`, `GetGitHubStatus`, `ListGitHubApps`, `GetGitHubApp`, `GetSlackStatus`, `ListSlackApps`, `ListSlackChannels`, `ListSlackSharedChannels`, `ResolveDirectoryGroups`, `ListServedDomains`. A scoped viewer sees only what its directory owns. `Explain` of oneself and listing one's own sessions need no role |
| operator | `all:access-roster:operator` | Everything: Connect, Reconnect, UploadKey, `SetServedDomains`, `SetSyncedGroups`, Probe, Refresh, Disconnect, the GitHub connects and disconnects, `ListGitHubAppTokens`, `ConfirmGitHubRemovals`, `ImportGitHubLinks`, `BeginSlackWorkspaceConnect`, `RequestSlackPass`, `DisconnectSlackWorkspace`, `ConfirmSlackRemovals`, `CreateSlackApp`, `InstallSlackApp`, Slack channel and Slack Connect create, update and delete, listing and revoking anyone's sessions. `ChangeSlackWorkspaceOwner` and `ChangeGitHubOrganisationOwner` need the installation-wide operator |

The group names keep a legacy identifier, renamed in v1.75–v1.76.

## Per-directory operators

`<directory-workspace-id>:access-roster:viewer` and `:operator` give the same roles over what that directory owns. That is the Slack workspaces and GitHub organisations connected with it as owner, their Apps and their channels.

| Who connects | Owner recorded |
|---|---|
| Installation-wide operator | Any connected directory, or none |
| Operator of one directory | That directory |
| Operator of several | One of theirs |

Only the installation-wide operator changes the owner (*Change owner*). A workspace or organisation with no owner is operated by the installation-wide operator alone.

Behind a gateway that forwards a token, the email resolves through the directory. The token's groups are not read.
