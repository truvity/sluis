# Lost operator access

## Purpose

Get an operator back into the console when nobody is in the operators group, the group was renamed, or the directory
sign-in is what is broken.

## Preconditions

- On Kubernetes: you can mint a ServiceAccount token for the recovery account (the same RBAC as
  [day one](day-one.md)). Recovery is the cluster anchor used as the floor ([trust](../explanation/trust.md)): the
  issuer depends on the directory, the directory is what is assumed broken, so the API server is all that is left.
- On Lambda: you can read the recovery parameter ([recover on Lambda](recover-on-lambda.md)).

## Before you start

Each of these is a refusal that has been mistaken for something else.

- **`503 recovery could not be checked`**: the check did not run. The API server is unreachable, or the service may not
  create TokenReviews (the ClusterRole `<release>-<namespace>-tokenreview`). It is not a wrong token.
- **`401 that proof was not accepted`**: the token expired (they are minted for minutes), was minted for another
  audience, or belongs to an account that may not recover. Mint another with both `--audience` and `--duration`.
- **Outside a cluster, `429 too many attempts`**: ten wrong passwords stop the password answering for a minute, the
  correct one included, so the limit is no hint about which guess was close. Wait, then try once.
- **`400 this sign-in did not start in this browser`**: the recovery form must be posted from the browser that loaded
  the sign-in page, which sets a recovery cookie (`__Host-access_roster_recovery`) bound to the form's state. A form
  loaded before an upgrade is refused once; reload `/login` and paste again. Opening another sign-in page or following a
  provider button does not break a recovery form already open, but two recovery forms share the one cookie, so only the
  last one loaded can recover. A body over 16 KiB is answered 413.
- **Recovery disabled** (`config.recovery.enabled: false`): there is no password to recover, only RBAC to hold. Step 1
  turns it back on.
- **Behind a gateway that fronts the console with its own cookie**, sign-out has to run the whole chain (the proxy's
  sign-out, then the issuer's `end_session` with that console's client id, then the console's front page), and the
  issuer's client must list that front page in `signed_out`. With only the proxy's half the issuer keeps the session and
  the next click signs the person straight back in, which looks exactly like success.

## Steps

### 1. Enable recovery if it is off

**Run** set `config.recovery.enabled: true` with `config.inCluster: true` in the values, and roll the Deployment.
**Expect** the ServiceAccount to exist and `/login` to offer *Recovery sign-in*.
**Verify** `kubectl -n <namespace> get serviceaccount <recovery serviceAccount>`.
**Rollback**: set it back to `false` and roll.

### 2. Mint a token and sign in

**Run**

```sh
kubectl -n <namespace> create token <recovery serviceAccount> --audience <recovery audience> --duration 10m
```

then `/login`, *Recovery sign-in*, paste.
**Expect** the console, signed in as `recovery`, an operator by construction.
**Verify** who did it is in the service's log and in the cluster's audit log, by name; the audit trail records
`roster.recovery.signed_in`. A recovery sign-in has no address and cannot read the Audit page.
**Rollback**: sign out.

### 3. Fix the membership

**Run** repair the directory group, or attach the right one to the operators group.
**Expect** the group's members to appear as operators.
**Verify** sign out, sign in through the directory, search for yourself.
**Rollback**: restore the previous group in the directory.

### 4. Optional: sign everyone out

**Run** delete `Secret <release>-session-key` and restart the Deployment. Sessions are stateless signed cookies; the
service generates a new key at start.
**Expect** everyone, you included, to sign in again.
**Verify** the old cookie is refused. See [rotate keys and credentials](rotate-keys-and-credentials.md) for why the
restart matters.
**Rollback**: none, because the old key is gone by design.

## Afterwards

- On Lambda, turn the recovery password off again once a directory group grants operator.
- Tell the people whose access was repaired, and add the cause to your incident notes.
