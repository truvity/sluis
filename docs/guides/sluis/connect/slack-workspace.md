# Connect a Slack workspace

Connect a Slack workspace to sluis from the console and run the Slack controller for it. The controller makes the
workspace's channels match the policy's `slack` table; it is a loop inside the one `sluis serve` process (v1.63), like
the [GitHub controller](github-organisation.md), with no listener of its own. How it decides is in
[How a Slack pass decides](../../../concepts/sluis/slack-pass.md); states, scopes, the Slack area, metrics and
audit actions are in [Slack reference](../../../reference/sluis/slack.md). How a channel is bound (`from`, `mode`, `ignore`,
`adopt`, `private`) is the [policy's Slack section](../../../reference/sluis/policy-bindings.md#slack-channels).

There are three kinds of channel: a **policy channel** bound in git to internal groups, a **console channel**
([manage them](slack-console-channels.md)) and a **Slack Connect channel** ([connect them](slack-connect-channels.md)).
A workspace's team, owner and domains are not in the policy; see
[where they come from](../../../concepts/sluis/slack-reconciler.md#where-a-workspaces-team-owner-and-domains-come-from).

The operator procedure to switch the controller from dry run to acting (reading the report, adding the key to
`enabledWorkspaces`, rolling out) is [Enable a Slack workspace](../enable-slack-workspace.md).

## Connect a workspace from the console

The **Workspaces** tab of the Slack area lists every workspace the policy
declares (`slack.workspaces`) that you may view, with where it stands and what
the controller last did there. A workspace the policy no longer declares but
that is still connected stays listed, marked *no longer declared*, so it can be
disconnected. Connecting one is three steps, and the only thing
you type is a throwaway token (and, where you have a choice, the owning
directory):

1. **Generate an app configuration token.** Open
   [api.slack.com/apps](https://api.slack.com/apps), scroll to **Your App
   Configuration Tokens** and press **Generate Token** for the workspace that
   will own the App. It is one word starting `xoxe.`, it expires in **12
   hours**, and the console uses it **once**: it creates (or updates) the App
   and is dropped. It is not written to the Secret or a record, not put in the
   signed state, and not in any log line, audit record or error.
2. **Press Connect** on the workspace's card, choose the owning directory
   where the form offers a choice, and paste the token into the password field
   (the field is cleared before the call). The console builds the
   App's manifest (the bot user, the scopes below, and its own callback as the
   only redirect URL: nothing that receives a request from Slack), creates the
   App, and keeps its client id and secret as **created, not installed**. You
   are then sent to Slack.
3. **An owner of the workspace approves the App** on Slack's page. Slack sends
   the browser back to `/connect/slack/workspace/callback`; the console
   exchanges the code for the bot token and keeps it, in the Secret
   `<release>-slack-credentials`. The **first** install records the team Slack
   reports; a **later** install is kept only if it belongs to that same team.
   Any other team is refused: the token is **revoked** (`auth.revoke`) and
   dropped, nothing is kept, and `roster.slack_workspace.connect_refused` is
   recorded (so is a first install into a team already connected under another
   key). A successful install records `roster.slack_workspace.connected`.

### A pass runs promptly after an install

Besides its 15-minute interval (`config.controllers.slack.interval`), the controller looks
every 30 seconds at the mounted credentials and records. When a workspace's own
credential or record, or a console channel or Slack Connect record, changed (a new bot token after an install, a reconnect), it
runs a full pass without waiting for the interval. Allow up to about two
minutes: the look is every 30 seconds, and the kubelet takes up to about a
minute to project a changed Secret or ConfigMap into the pod. Until a report
newer than the connection exists, the card says *Installed - waiting for the
first pass*.

**Refresh** on an installed workspace's card (operators only) asks for a pass
over that workspace now: the console writes a marker `_pass.<workspace>.json`
into the records ConfigMap and the controller notices it at its next look. A
second request less than 60 seconds after the first is refused. The card says
*Pass requested* until a newer report exists. The marker is not a record: it is
left out of the recovery copy, and there is no audit action for it (the
requester is logged). Saving a console channel or a Slack Connect record
starts a pass the same way: the controller's look at the records includes them,
and saves made close together are answered by one pass.

**Reconnect** approves the App again (to rotate the token, or to grant scopes a
later release asks for). When the roster now asks for a scope the App was
created without, the card shows **scopes missing** and Reconnect asks for a
fresh configuration token first, because Slack changes an App's scopes only for
one; it updates the manifest with it, then sends an owner to Slack.

**Disconnect** (with a confirm dialog) revokes the bot token, deletes the
credential, the record and any confirmation, and records
`roster.slack_workspace.disconnected`. The controller then reports the
workspace as not connected, within a couple of minutes, and carries nothing over
from the old connection: the Channels and People views stop showing its channels
and members. If Slack will not revoke the token the connection is
kept and the dialog offers **Forget anyway**, which forgets it and says in the
audit record that the token was not revoked; remove the App in its Slack
settings then. The App itself stays in Slack until it is deleted there.
Disconnect leaves the workspace's console channel records
(`_channel.<workspace>.*`) and the Slack Connect records it hosts in place:
delete them first, or they apply to whichever Slack team is connected under that
key next (the dialog says so). Their definitions are not reviewed for a
different team, so delete them when the key is reused for one.

A workspace's connection is an **owner's** to operate: the installation-wide
operator, or the operator of the directory workspace recorded as its owner. A viewer sees the page and no buttons; every
row carries `can_operate`, the server's answer. A deployment that keeps no state
in Kubernetes cannot connect a workspace: a bot token would not survive a
restart.

## Running the controller

```yaml
config:
  controllers:
    slack:
      consoleURL: http://sluis.access.svc:8080/console   # this release's own Service
policy:
  controllers:
    slack:
      enabledWorkspaces: []   # born disabled: nothing is changed until a workspace is listed
exchange:
  clusters:
    - name: prod        # this cluster: the service verifies the controller's token against its key set
      issuer: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE
      jwksUri: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE/keys
```

The other value is `config.controllers.slack.interval` (the pass interval, default 15m);
the pod's `resources` are the release's. The chart and the controller refuse a policy
whose `controllers.slack.enabledWorkspaces` names a workspace the policy does not declare.

The policy puts its account in the group that reads who holds a group:

```yaml
groups:
  all:access-roster:viewer:
    matchers:
      - service_account: { cluster: prod, namespace: access, name: sluis }   # the release's own ServiceAccount: the controller runs as it
```

The chart refuses to render the controller without an `exchange.clusters` row or
a console mount. The controller records what it did into the audit trail itself,
with its own token, when `audit.*` is set; the installation must map the
release's account `<release>` to the source `roster` (v1.62's `<release>-slack-roster` is gone).

**Egress.** The controller calls Slack's API at `slack.com:443`. The chart
cannot open that: a Kubernetes NetworkPolicy cannot name a host, and the chart's
policy governs the service's ingress only (as for `api.github.com` and the GitHub
controller). Allow `slack.com:443` for the controller's pods,
`app.kubernetes.io/name: sluis`, in the cluster's egress
policy (the one pod: it is the service's). The chart does admit the controller to the service's port, for reading
the console's API, when `networkPolicy.enabled`.

**What it reads.** The console's API (with the pod's ServiceAccount token): who
holds each group (`ListHolders`), who is in each directory group and each
individually listed address (`ResolveDirectoryGroups`), whether the directory
vouches for an address (`Explain`) and which domains each directory serves (`ListServedDomains`, a
viewer's read: the controller's account is in the group the chart grants it,
and sees every directory). The rest are mounted volumes, so the account needs no permission to
read any Secret or other ConfigMap through the API: the Secret
`<release>-slack-credentials` (one `<workspace>.json` per workspace: app id,
client id and secret, the bot token once installed) and the ConfigMap
`<release>-slack-workspaces` (each workspace's record, `_channel.*` console
channels, `_shared.*` Slack Connect channels, `_confirm.*` confirmations and
`_pass.*` pass requests). Both are optional: before anything is
connected the controller reports each declared workspace as not connected. Keys
that start with an underscore are other documents and are never read as a
workspace.

The service also keeps `<release>-slack-records`, a mirror Secret of the
records ConfigMap, for the recovery copy `slackState.push` renders; the
controller does not read it. See [Slack state](../operate/back-up-and-restore.md#1-know-what-there-is).

Every answer the console gives carries the digest of the policy it was computed under, and a pass under another
policy changes nothing and is tried again within seconds, so a rollout needs no ordering.
