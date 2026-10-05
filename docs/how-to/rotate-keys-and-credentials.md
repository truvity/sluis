# Rotate keys and credentials

## Purpose

Replace a workspace credential, the session key, an OAuth client secret or a Slack token, and know it took effect.

## Preconditions

- Operator access to the console, and `kubectl` on the namespace for the restart.
- The new value, written to the place that holds it (the console's Reconnect or Upload key, or the named Secret).

## Before you start

- **The Secrets and ConfigMaps are a projection, not a live source.** Nothing in the service watches them: writing a new
  value rotates nothing on a running pod. Which ones need a restart differs, as `charts/sluis/values.yaml` states it:
  - `workspace-credentials`: **restart**. The credential is read once, when a replica first opens that workspace (at
    start, or at the console's connect ceremony), and the open reader is held in memory. Only disconnecting drops a
    reader, which also revokes the credential and deletes the snapshot: that is removal, not rotation.
  - `session-key`: **restart**. Read once while the stores are wired. Deleting it takes effect on the restart, not on the
    delete.
  - `github-links` (App): **no restart**. The record and its credential are read on every use.
- **The silence is the hazard.** A rotation that has not taken effect looks exactly like one that has, until a restart
  hours or weeks later picks up the new value, or, if the written value was wrong, takes the directory down at a moment
  nobody connects to the change. Measured on 2026-09-21: a deliberately corrupted credential went unnoticed for 14
  minutes of normal operation, probes and a successful directory read included.
- **Restoring from a backup is a rotation** and needs the same restart.
- **Never copy the session key anywhere.** There is no `session.push`, deliberately.

## Steps

### 1. Write the new value

**Run** per credential:

- **Consent credential:** Reconnect. The old refresh token is revoked at Google as part of it, and the replica that
  served the click opens the new reader; every other replica holds its old one.
- **Service-account key, connected through the console:** Upload key again with the new JSON; the old one is replaced.
- **Service-account key, declared:** replace the named Secret. Delete the old key in Google Cloud only after step 3
  shows the new one was read.
- **OAuth client secret:** update the declared Secret. The console cannot set it (`SettingsService` has `GetSettings` and
  no `SetOAuthClient`, because a credential a console can change is one somebody can change from a browser). Existing
  refresh tokens keep working; the secret is used only to exchange and refresh.
- **Slack bot token:** press **Connect** on the workspace (a reinstall; a configuration token is needed only when the
  roster's scopes grew). The new token is written to `<release>-slack-credentials`; the controller reads it within about
  two minutes and passes straight away, **no restart**.
- **Catalogue Slack App:** reinstall from the Apps tab. A consumer of a `slackApps[].push` copy reads the new value when
  External Secrets refreshes it (default 1h).
- **Session key:** delete `Secret <release>-session-key`.

**Expect** the new value stored.
**Verify** step 3.
**Rollback**: write the previous value back and do step 2 again; the old credential may be revoked already, in which case
it is Reconnect.

### 2. Restart

**Run** `kubectl -n <namespace> rollout restart deploy/<release>` (not for the Slack bot token or a GitHub App).
**Expect** a rolling restart; for the session key, everyone signs in again (the service mints a fresh one).
**Verify** `kubectl -n <namespace> rollout status deploy/<release>`.
**Rollback**: none, because a restart changes nothing but what it reads.

### 3. Confirm from the log

**Run** `kubectl -n <namespace> logs deploy/<release> --since=10m | grep 'opened a workspace this replica had not seen'`.
**Expect** a line for each rotated workspace, on every replica.
**Verify** the directory page's health is ok and its next probe succeeds.
**Rollback**: none, because it only reads.

## Afterwards

- Delete the old credential at its source only after the log shows the new one was read.
- Add the date to the installation's rotation record; tell whoever owns the upstream account.
