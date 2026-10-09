# Migrate from google-group-sync

Replace google-group-sync, one Workspace per process, with one sluis deployment that serves every Workspace through the policy and the console's API.

## Before you start

- Install sluis ([install](../operate/install-with-helm.md)), with an audit installation if you want the trail.

- Collect every service-account key and the Secrets that deliver them, and list what reads google-group-sync's answers.

- Render and diff before each upgrade.

- Not carried over: the Lambda and Lambda-extension flavours, per-process configuration, the REST routes and the environment-variable credential path.

- Delete a key in Google Cloud only after the console shows the connection healthy.

## Steps

### 1. Overlay first

Declare every existing key as a [workspace](../../../reference/sluis/declared-workspaces.md), delivering the key Secret as before. Render and upgrade. The console's Directories page lists every domain, all authoritative. Compare `Explain` on sample addresses with the old answers. To undo, remove the entries.

### 2. Move the consumers

Turn a GitHub team sync into `github` bindings in the policy. Anything else asks the console's `AccessService` with its own ServiceAccount token and reads `authoritative` before acting on a removal. One address replaces the per-instance addresses. Watch decisions for a day. To undo, point the consumer back at its instance.

### 3. Connect through consent

For each workspace, press Connect as its admin role account, remove the declared entry, render and upgrade. `Explain` still answers authoritatively. Then delete the key in Google Cloud. Before that, restore the entry to undo; after it, connect again.

### 4. Retire

Remove the google-group-sync deployments and key Secrets, and archive the repository. Check no consumer resolves an old address. Redeploy from the archive to undo.

## Afterwards

The credential lives in `Secret <release>-workspace-credentials` and in state: add it to your [backups](../operate/back-up-and-restore.md).
