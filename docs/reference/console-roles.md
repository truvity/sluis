# Console roles

Two roles, held by membership of two declared internal groups, and the same two scoped to one directory. The groups are
declared in the policy ([policy](policy.md)); the methods are the console's ([contracts](contracts.md)).

| Role | Group | May |
|---|---|---|
| viewer | `all:access-roster:viewer` | every read: `ListWorkspaces`, `GetSettings`, `GetPolicy`, `WhoAmI`, `Explain`, `ListDirectoryGroups`, `GetDirectoryGroup`, `SearchPeople`, `ListHolders`, `GetGitHubStatus`, `ListGitHubApps`, `GetGitHubApp`, `GetSlackStatus`, `ListSlackApps`, `ListSlackChannels`, `ListSlackSharedChannels`, `ResolveDirectoryGroups`, `ListServedDomains` (a scoped viewer sees only what its directory owns) for anybody else (one's own reach, like `Explain` of oneself, needs no role), and listing one's own sessions — the whole console, read-only |
| operator | `all:access-roster:operator` | everything: Connect, Reconnect, UploadKey, `SetServedDomains`, `SetSyncedGroups`, Probe, Refresh, Disconnect, the GitHub connects and disconnects, `ListGitHubAppTokens` (a request names who asked), `ConfirmGitHubRemovals`, `ImportGitHubLinks`, `BeginSlackWorkspaceConnect`, `RequestSlackPass`, `DisconnectSlackWorkspace`, `ConfirmSlackRemovals`, `CreateSlackApp`, `InstallSlackApp`, the Slack channel and Slack Connect create, update and delete, `ChangeSlackWorkspaceOwner` and `ChangeGitHubOrganisationOwner` (installation-wide operator only), listing and revoking anyone's sessions |

## Per-directory operators

Beside the two installation-wide groups, `<directory-workspace-id>:access-roster:viewer` and `:operator` give the same
roles over what that directory owns: the Slack workspaces and GitHub organisations connected with it as owner, their
Apps and the channels in them. The owner is recorded at connect (the installation-wide operator may name any connected
directory or none; an operator of one directory owns what it connects; an operator of several chooses among theirs) and
only the installation-wide operator changes it (*Change owner*). A workspace or organisation with no owner is operated
by the installation-wide operator alone.

Behind a gateway that forwards a token, the forwarded identity's email is resolved through the directory like any other;
the groups in the token itself are not consulted, because the directory is the source they came from.
