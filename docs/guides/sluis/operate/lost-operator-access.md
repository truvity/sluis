# Lost operator access

Get an operator back into the console when nobody is in the operators group, the group was renamed, or directory sign-in is broken.

## Before you start

- On Kubernetes you must be able to mint a ServiceAccount token for the recovery account ([day one](day-one.md)). On Lambda you must read the recovery parameter ([recover on Lambda](recover-on-lambda.md)).

- `503 recovery could not be checked`: the check did not run. The API server is unreachable, or the service may not create TokenReviews (ClusterRole `<release>-<namespace>-tokenreview`). The token is not wrong.

- `401 that proof was not accepted`: the token expired, was minted for another audience, or belongs to an account that may not recover. Mint another.

- `429 too many attempts`, outside a cluster: ten wrong passwords block the password, the correct one too, for a minute. Wait, then try once.

- `400 this sign-in did not start in this browser`: post the form from the browser that loaded the sign-in page, which sets the recovery cookie `__Host-sluis_recovery`. Two open recovery forms share the cookie, so only the last loaded can recover. A body over 16 KiB gets 413.

- Recovery disabled (`config.recovery.enabled: false`): step 1 turns it back on.

- Behind a gateway with its own cookie, sign-out runs the proxy's sign-out, then the issuer's `end_session` with the console's client id, then the front page, which the client lists in `signed_out`. The proxy's half alone signs the person straight back in.

## Steps

1. If recovery is off, set `config.recovery.enabled: true` with `config.inCluster: true` and roll the Deployment. Check the ServiceAccount exists and `/login` offers *Recovery sign-in*.

   ```sh
   kubectl -n <namespace> get serviceaccount <recovery serviceAccount>
   ```

2. Mint a token, open `/login`, choose *Recovery sign-in* and paste it. You sign in as `recovery`, an operator by construction. The audit trail records `roster.recovery.signed_in`. A recovery sign-in cannot read the Audit page.

   ```sh
   kubectl -n <namespace> create token <recovery serviceAccount> --audience <recovery audience> --duration 10m
   ```

3. Repair the directory group, or attach the right one to the operators group. Sign out, sign in through the directory and search for yourself.

4. Optionally sign everyone out: delete `Secret <release>-session-key` and restart the Deployment ([rotate keys and credentials](rotate-keys-and-credentials.md)). This cannot be undone.

## Roll back

Set `config.recovery.enabled` back to `false` and roll to undo step 1. To undo step 3, restore the previous group in the directory. On Lambda, turn the password off once a group grants operator.
