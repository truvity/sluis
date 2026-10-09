# Day one: the first sign-in of a standalone installation

Sign the first operator in to a standalone installation and finish its setup in the console.

## Before you start

- The chart is installed and the Deployment is Ready ([install with Helm](install-with-helm.md)).

- You are a cluster administrator, or you may `create` on `serviceaccounts/token` for the recovery ServiceAccount (`config.recovery.serviceAccount`).

- With a workspace and a non-empty operators group in the values, there is no day one. People sign in through the directory ([connect a Google Workspace](../connect/google-workspace.md)).

- Outside a cluster there is no API server to prove access. The service prints a generated recovery password once at start. Read it from the log, or on Lambda from SSM ([recover on Lambda](recover-on-lambda.md)). Such an installation has a fifth step: turn the password off.

## Steps

1. Check the install created the session key and the recovery ServiceAccount. No password exists.

   ```sh
   kubectl -n <namespace> get secret <release>-session-key serviceaccount/<recovery serviceAccount>
   ```

2. Mint a recovery token. It expires in ten minutes and is not stored. `config.recovery.serviceAccount` and `config.recovery.audience` name the account and audience.

   ```sh
   kubectl -n <namespace> create token <recovery serviceAccount> \
     --audience <recovery audience> --duration 10m
   ```

3. Port-forward the service, or go through the gateway. Open `/login`, expand **Recovery sign-in** and paste the token. The session shows the identity that minted it. A refusal is one of the cases in [lost operator access](lost-operator-access.md#before-you-start).

4. Follow the four steps Overview lists. Register an OAuth client with the directory, give it to the service, connect the first directory, and attach a directory group to the operators group. Each disappears when done. Only the first leaves the console: [connect a Google Workspace](../connect/google-workspace.md) walks it, and Overview shows the two values to paste. The redirect URI is the installation's own hostname, so copy the values Overview shows.

5. Sign out, sign in through the directory and search for yourself.

## Verify

Your page shows *operator* and the membership that granted it. The directory page shows the workspace healthy once the first snapshot has run ([check health](check-health.md)).

## Roll back

Sign out to end the recovery session. *Disconnect* the directory to undo step 4. Uninstall the release to undo step 1.
