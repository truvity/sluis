# Lost operator access

Get an operator back into the console when nobody is in the operators group, the group was renamed, or directory sign-in is broken.

## Before you start

- On Kubernetes you must be able to mint a ServiceAccount token for the recovery account ([day one](day-one.md)). On Lambda you must read the recovery parameter ([recover on Lambda](recover-on-lambda.md)).

- `503 recovery could not be checked`: the API server is unreachable, or the service may not create TokenReviews (ClusterRole `<release>-<namespace>-tokenreview`). The token is not wrong.

- `401 that proof was not accepted`: the token expired, has another audience, or belongs to an account that may not recover. Mint another.

- `429 too many attempts`: your address spent its refused proofs for the minute (5 at the issuer, 10 at the console). The correct proof is refused too. Wait, then try once.

- `400 this sign-in did not start in this browser`: post the form from the browser that loaded the sign-in page, which sets the cookie `__Host-sluis_recovery`. Two open recovery forms share it: only the last loaded can recover. A body over 16 KiB gets 413.

- Recovery disabled (`config.recovery.enabled: false`, or [in the console](turn-off-recovery.md)): step 1 turns it back on.

- Behind a gateway with its own cookie, sign-out runs the proxy's sign-out, then the issuer's `end_session` with the console's client id, then the front page listed in `signed_out`. Without an `id_token_hint`, `end_session` waits for a click. The proxy's half alone signs the person straight back in.

## Steps

1. If recovery is off, set `config.recovery.enabled: true`, `config.inCluster: true` and a new `config.recovery.generation`, and roll the Deployment. Check that the ServiceAccount exists and `/login` offers *Recovery sign-in*.

   ```sh
   kubectl -n <namespace> get serviceaccount <recovery serviceAccount>
   ```

2. Mint a token, open `/login`, choose *Recovery sign-in* and paste it. You sign in as `recovery`, an operator by construction. The audit trail records `roster.recovery.signed_in`. A recovery sign-in cannot read the Audit page.

   ```sh
   kubectl -n <namespace> create token <recovery serviceAccount> --audience <recovery audience> --duration 10m
   ```

3. Repair the directory group, or attach the right one to the operators group. Sign out, then sign in through the directory.

4. Optionally sign everyone out: delete `Secret <release>-session-key` and restart ([rotate keys and credentials](rotate-keys-and-credentials.md)). This cannot be undone.

## Roll back

Set `config.recovery.enabled` back to `false` and roll to undo step 1. To undo step 3, restore the previous group in the directory. On Lambda, turn the password off once a group grants operator.
