# Migrate from google-group-sync

## Purpose

Replace google-group-sync (one Workspace per process, from a service-account key) with sluis, which serves every
Workspace from one deployment and gives its answers to consumers through the policy and the console's API.

## Preconditions

- sluis installed ([install](../operate/install-with-helm.md)), with an audit installation connected if you want the trail.
- Every existing service-account key, and the Secrets that deliver them.
- A list of what reads google-group-sync's answers today.

## Before you start

- **Preview before every apply, and read the preview**: each step changes the installation, so render, diff the
  documents, then upgrade.
- **Deleting a declared workspace is not deleting its credential.** After step 3 the credential lives in state, not in your
  delivery; remove a key from the cloud only after the console shows the connection healthy.
- **Not carried over:** the Lambda and Lambda-extension flavours (single workspace by construction), the per-process
  configuration, the REST routes and the environment-variable credential path.

## Steps

### 1. Overlay first

**Run** declare every existing key as a workspace (`directory.workspaces[]` in the installation, the key Secrets delivered
the way they were to google-group-sync), render and upgrade. No Connect step.

**Expect** the console's Directories page lists every domain the old instances served, all authoritative.

**Verify** spot-check a sample of addresses with `Explain` against the old instances' answers.

**Rollback** remove the entries and upgrade; nothing reads sluis yet.

### 2. Move the consumers

**Run** a GitHub team sync becomes `github` bindings in the policy, and the GitHub controller that runs inside sluis. Anything
else asks the console's `AccessService` with its own ServiceAccount token, and reads `authoritative` before acting on a
removal. One address replaces N per-instance addresses; the service routes by domain.

**Expect** each consumer's decisions match the old ones.

**Verify** watch the consumers' decisions for a day.

**Rollback** point the consumer back at its google-group-sync instance, which still runs.

### 3. Connect through consent

**Run** for each workspace, press Connect as its admin role account, then remove the declared entry, render and upgrade.

**Expect** the console shows the connected workspace taking over its domains.

**Verify** `Explain` on a sample address still answers authoritatively. Then delete the service-account key in Google Cloud.

**Rollback** restore the declared entry and upgrade, before the key is deleted. After it, connect again.

### 4. Retire

**Run** remove the google-group-sync deployments and their key Secrets; archive the repository.

**Expect** nothing else is affected.

**Verify** no consumer still resolves an old address.

**Rollback** none, because the deployments and keys are deleted; redeploy from the archive if ever needed.

## Afterwards

The credential now lives in `Secret <release>-workspace-credentials` and in state rather than in your delivery: add it to
your backups ([day two](../operate/back-up-and-restore.md)).
