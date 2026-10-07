# Audit actions

Every action sluis records, from the catalogue [`internal/audit/catalogue/roster.yaml`](../../internal/audit/catalogue/roster.yaml)
(the version and the number of actions are in the table below). All are kept under the `security` profile in the installation's tenant `@platform`.
Delivery `async` goes on a bounded queue in the process; `block` is recorded before the action completes and refuses
it when it cannot be. Why the trail is shaped this way: [audit](../explanation/audit.md).

Actor kinds: `person`, `recovery`, `ci`, `workload`, `system`, `anonymous`. Target types: `client`, `workspace`,
`organisation`, `team`, `github_account`, `github_app`, `slack_workspace`, `slack_channel`, `slack_user`, `slack_app`,
`directory_group`, `directory_user`.

<!-- generated: audit-actions -->

Catalogue version 1.9.0, 68 actions.

| Action | Operation | Targets | Delivery | Summary |
|---|---|---|---|---|
| `roster.person.signed_in` | authentication | client | async | A person signed in to a client, or was refused. |
| `roster.recovery.signed_in` | authentication | client | block | Somebody signed in with the recovery identity, which bypasses the directory. |
| `roster.token.exchanged` | authentication | client | async | A token was exchanged for one another client accepts, or the exchange was refused. |
| `roster.github_token.minted` | create | github_app | async | A GitHub App installation token was minted, or refused. Never the token. |
| `roster.session.ended` | authentication | — | async | A person signed out, ending their sessions. |
| `roster.session.revoked` | remove | client | async | A person's sessions were revoked, by somebody, or by the issuer (scope `refresh_token_reuse`) when a spent refresh token was presented again after its grace window, (scope `pre_upgrade_cookie`, by an anonymous actor) when a browser signed out with a sign-in cookie set before the cookie had a secret of its own, or (scope `sign_in_replaced`, by the person signing in) when another person signed in in the same browser. |
| `roster.session.refresh_refused` | authentication | client | async | A session was refused a refresh because its holder is no longer admitted to the client. |
| `roster.client.secret.created` | create | client | async | The secret of a generated client was made. |
| `roster.client.secret.adopted` | create | client | async | An existing secret of a generated client was taken as its stored secret, unchanged. |
| `roster.client.secret.rotated` | modify | client | async | The secret of a generated client was replaced, the old one staying valid for an overlap. |
| `roster.client.secret.orphaned` | access | client | async | A stored client secret was found with no generated client of that id in the policy. |
| `roster.client.secret.deleted` | remove | client | async | The stored secret of a client no longer in the policy was deleted. |
| `roster.client.secret.denied` | access | client | async | A signed-in caller was refused when managing the secret of a generated client. |
| `roster.workspace.connected` | create | workspace | async | A directory was connected. |
| `roster.workspace.reconnected` | modify | workspace | async | A connected directory was given a new consent. |
| `roster.workspace.disconnected` | remove | workspace | async | A directory was disconnected. |
| `roster.workspace.domains_changed` | modify | workspace | async | The domains a directory answers for changed. |
| `roster.workspace.groups_changed` | modify | workspace | async | The groups served from a directory changed. |
| `roster.github_app.created` | create | github_app, organisation | async | A GitHub App was created for an organisation. |
| `roster.github_org.connected` | create | organisation | async | A GitHub organisation was connected by installing the App. |
| `roster.github_org.pass_requested` | modify | organisation | async | An operator asked the GitHub controller to pass over an organisation now. |
| `roster.github_org.owner_changed` | modify | organisation | async | The directory that owns a GitHub organisation was changed by the installation-wide operator. |
| `roster.github_org.disconnected` | remove | organisation | async | A GitHub organisation was disconnected. |
| `roster.github_removals.confirmed` | modify | organisation | async | An operator confirmed a set of removals the safety breaker had held. |
| `roster.link_app.connected` | create | github_app | async | The App that links people's GitHub accounts was connected. |
| `roster.link_app.disconnected` | remove | github_app | async | The App that links people's GitHub accounts was disconnected. |
| `roster.catalogue_app.created` | create | github_app, organisation | async | A catalogued GitHub App was created. |
| `roster.catalogue_app.installed` | create | github_app, organisation | async | A catalogued GitHub App was installed. |
| `roster.catalogue_app.disconnected` | remove | github_app, organisation | async | A catalogued GitHub App was disconnected. |
| `roster.runner_app.created` | create | github_app, organisation | async | A GitHub App for self-hosted runners was created. |
| `roster.runner_app.installed` | create | github_app, organisation | async | A GitHub App for self-hosted runners was installed. |
| `roster.runner_app.disconnected` | remove | github_app, organisation | async | A GitHub App for self-hosted runners was disconnected. |
| `roster.github_link.created` | create | github_account | async | A person linked their GitHub account. |
| `roster.github_link.matched` | create | github_account | async | A GitHub account was linked to a person because its profile publishes their work address. |
| `roster.github_link.imported` | create | github_account | async | An operator imported a link between a GitHub account and a person. |
| `roster.github_link.moved` | modify | github_account | async | A link moved to another address of the same person. |
| `roster.github_link.narrowed` | modify | github_account | async | A link lost some of its addresses. |
| `roster.github_link.unverifiable` | modify | github_account | async | A link could no longer be verified. |
| `roster.github_link.lost` | remove | github_account | async | A link was lost. |
| `roster.github_member.invited` | create | organisation, team, github_account | async | A person was invited to a GitHub organisation or team. |
| `roster.github_member.added` | create | organisation, team, github_account | async | A person was added to a GitHub team. |
| `roster.github_member.role_set` | modify | organisation, team, github_account | async | A person's role on a GitHub team changed. |
| `roster.github_member.removed` | remove | organisation, team, github_account | async | A person was removed from a GitHub organisation or team. |
| `roster.github_member.held` | modify | organisation, team, github_account | async | A change to a person's GitHub membership is held, and needs an operator. |
| `roster.github_owner.reported` | access | organisation, github_account | async | An organisation owner the directory does not vouch for was reported rather than removed. |
| `roster.slack_workspace.connected` | create | slack_workspace, slack_app | async | A Slack workspace was connected by installing the App. |
| `roster.slack_workspace.owner_changed` | modify | slack_workspace | async | The directory that owns a Slack workspace was changed by the installation-wide operator. |
| `roster.slack_workspace.connect_refused` | create | slack_workspace, slack_app | async | An install of the roster's Slack App was refused because it was made into a workspace other than the one first connected. |
| `roster.slack_workspace.disconnected` | remove | slack_workspace, slack_app | async | A Slack workspace was disconnected. |
| `roster.slack_app.created` | create | slack_app, slack_workspace | async | A catalogued Slack App was created. |
| `roster.slack_app.installed` | create | slack_app, slack_workspace | async | A catalogued Slack App was installed into its workspace. |
| `roster.slack_app.install_refused` | create | slack_app, slack_workspace | async | An install of a catalogued Slack App was refused because it was made into another workspace. |
| `roster.slack_shared_channel.created` | create | slack_workspace, slack_channel, directory_group, directory_user | async | A Slack Connect channel was defined from the console. |
| `roster.slack_shared_channel.updated` | modify | slack_workspace, slack_channel, directory_group, directory_user | async | A Slack Connect channel's definition was changed from the console. |
| `roster.slack_shared_channel.deleted` | remove | slack_workspace, slack_channel, directory_group, directory_user | async | A Slack Connect channel's definition was deleted from the console; the channel stays in Slack. |
| `roster.slack_console_channel.created` | create | slack_workspace, slack_channel, directory_group, directory_user | async | An ordinary Slack channel was put under management from the console. |
| `roster.slack_console_channel.updated` | modify | slack_workspace, slack_channel, directory_group, directory_user | async | A console-managed Slack channel's record was changed from the console. |
| `roster.slack_console_channel.deleted` | remove | slack_workspace, slack_channel, directory_group, directory_user | async | A console-managed Slack channel's record was deleted from the console; the channel stays in Slack. |
| `roster.slack_channel.created` | create | slack_workspace, slack_channel | async | A Slack channel was created. |
| `roster.slack_channel.adopted` | modify | slack_workspace, slack_channel | async | An existing Slack channel was adopted into management, by name. |
| `roster.slack_channel.archived` | modify | slack_workspace, slack_channel | async | An operator archived a Slack channel when forgetting its console record. |
| `roster.slack_member.invited` | create | slack_workspace, slack_channel, slack_user | async | A person was invited to a Slack channel. |
| `roster.slack_member.removed` | remove | slack_workspace, slack_channel, slack_user | async | A person was removed from a private Slack channel. |
| `roster.slack_shared.invited` | create | slack_workspace, slack_workspace, slack_channel | async | A workspace's bot was invited to a shared channel. |
| `roster.slack_shared.accepted` | modify | slack_workspace, slack_workspace, slack_channel | async | A workspace accepted an invitation to a shared channel. |
| `roster.slack_action.held` | modify | slack_workspace, slack_channel, slack_user | async | A change to a Slack workspace or channel is held, and needs an operator. |
| `roster.slack_removals.confirmed` | modify | slack_workspace, slack_channel | async | An operator confirmed a set of Slack removals that were held. |
| `roster.slack_leaver.reported` | access | slack_workspace, slack_user | async | A person gone from the directory is still an active Slack member; reported rather than acted on. |
<!-- /generated -->
