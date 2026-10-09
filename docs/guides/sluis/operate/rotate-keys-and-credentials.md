# Rotate keys and credentials

Replace a workspace credential, the session key, an OAuth client secret or a Slack token, and confirm it took effect.

## Before you start

- You need operator access to the console and `kubectl` on the namespace.

- Secrets and ConfigMaps are a projection, not a live source. Nothing watches them, so writing a value rotates nothing on a running pod.

  - `workspace-credentials`: restart. A replica reads the credential once, when it first opens that workspace.
  - `session-key`: restart. It is read once while the stores are wired.
  - `github-links` (App): no restart. The record and credential are read on every use.

- A rotation that has not taken effect looks like one that has, until a later restart picks up the value. A wrong value then takes the directory down at an unrelated moment.

- A restore from a backup is a rotation and needs the same restart.

- Never copy the session key anywhere. No `session.push` exists.

## Steps

1. Write the new value:

   - Consent credential: **Reconnect**. This revokes the old refresh token at Google. Only the replica that served the click opens the new reader.
   - Service-account key connected through the console: **Upload key** again with the new JSON.
   - Declared service-account key: replace the named Secret. Delete the old key in Google Cloud only after step 3.
   - Generated confidential client secret (`secret: {generate: true}`): follow [rotate a client secret](rotate-a-client-secret.md); no restart.
   - OAuth client secret: update the declared Secret. The console cannot set it. Existing refresh tokens keep working.
   - Slack bot token: press **Connect** on the workspace. The controller reads the new token from `<release>-slack-credentials` within about two minutes. No restart. A configuration token is needed only when the scopes grew.
   - Catalogue Slack App: reinstall from the Apps tab. A `slackApps[].push` consumer sees the new value when External Secrets refreshes it (default 1h).
   - Session key: delete `Secret <release>-session-key`.

2. Restart, except for the Slack bot token and a GitHub App. For the session key everyone signs in again.

   ```sh
   kubectl -n <namespace> rollout restart deploy/<release>
   kubectl -n <namespace> rollout status deploy/<release>
   ```

3. Confirm from the log that every replica read the new credential.

   ```sh
   kubectl -n <namespace> logs deploy/<release> --since=10m | grep 'opened a workspace this replica had not seen'
   ```

## Verify

The directory page's health is ok and its next probe succeeds. Then delete the old credential at its source and record the rotation date.

## Roll back

Write the previous value back and restart. If the old credential is already revoked, **Reconnect**.
