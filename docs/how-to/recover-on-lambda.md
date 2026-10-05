# Recover on Lambda

## Purpose

Sign in with the recovery password on a Lambda installation, the day no directory can vouch for anybody, and turn it off
again afterwards.

## Preconditions

- `ssm:GetParameter` on `/sluis/<instance>/private/config/*`, and `kms:Decrypt` on `ParameterKeyArn` when one is set.
  That is the stack's operators, not the function's role business.
- The installation's console address, `https://<issuer host>/console/login`.

## Before you start

In a cluster, recovery is a short-lived ServiceAccount token proven against the API server and nothing is stored
([lost operator access](lost-operator-access.md)). A Lambda installation has no API server to prove access to, so
recovery is a **password**. The password's design is in [recovery](../explanation/recovery.md).

- **What the Pulumi library does.** `sluispulumi.NewLambda` generates the password (a `random.RandomPassword`, 40 letters
  and digits with no look-alikes, no keepers, so an apply never rotates it) and stores it as the SSM SecureString
  `/sluis/<instance>/private/config/recovery/password` (under `ParameterKeyArn` when set), secret in state and in
  `pulumi up`'s output. The service document names it `recovery.passwordSecret: recovery/password` (the library writes
  that key and `recovery.enabled`; a different value of your own is refused) and the document's `secrets` source (`ssm`,
  root `/sluis/<instance>`) reads it at cold start and again every five minutes. No file is written and no environment
  variable carries it. The `RecoveryPasswordParameter` output holds the parameter's name, never the value.
- **There is one function and one role** (since v1.63). It reads `private/config/` and `private/credentials/` and writes
  only `private/credentials/` and `export/`; it never writes `config/`.
- **Read the password from a terminal.** Not into a ticket or a chat.
- **Ten refused attempts within a minute make a function instance answer 429 for the next minute.** The count lives in
  each instance, so it bounds guessing per instance, not per installation; a 40-character password is not guessable
  either way, and the limit exists because a check costs memory on purpose.
- **Every attempt is recorded** as `roster.recovery.signed_in`, the refused ones too (outcome `denied`, with the reason:
  `the proof was not accepted`, `too many refused attempts`, `recovery sign-in is turned off`) and a failure to check as
  `failure`. A refused attempt names nobody (`anonymous`), and none carries the password. A successful one is written
  durably *before* the session exists, and no session is issued when the audit trail cannot be written. Refusals that cost
  nothing (turned off, throttled) are recorded once a minute per instance with a count.
- **The default is on.** A first installation has no other way in until a directory is connected, and the console's setup
  checklist shows *Turn off the recovery password* for as long as it is on.
- **`API.KeepDefaultEndpoint: true`** (for a cutover's acceptance run) serves the default `execute-api` endpoint without
  the custom domain's mutual TLS, so recovery is then reachable without the client certificate. Leave it off in service.
  Optionally cap the function's reserved concurrency: each recovery check costs memory on purpose, so a ceiling on
  instances is a ceiling on guesses per minute.
- **Unset, the service on Lambda never generates a password:** with recovery on and no `recovery.passwordSecret` it logs
  an ERROR and builds no recovery, rather than printing a password into the function's log. Outside the library,
  `recovery.passwordSecret` ([configuration](../reference/secrets.md#the-names)) is the NAME of the password
  (`recovery/password`) for any installation outside a cluster, delivered by the document's `secrets` source.

## Steps

### 1. Read the password

**Run**

```sh
aws ssm get-parameter --with-decryption \
  --name /sluis/<instance>/private/config/recovery/password \
  --query Parameter.Value --output text
```

**Expect** 40 letters and digits.
**Verify** the length is 40.
**Rollback**: none, because it only reads.

### 2. Sign in

**Run** open `https://<issuer host>/console/login`, expand **Recovery sign-in**, paste the password. This is the
console's own sign-in page, so it is reachable even where `console.client` sends everybody else to the issuer's page
(which on Lambda offers no recovery of its own: the issuer's token form needs a cluster).
**Expect** a session carrying the identity `recovery`, an operator by construction.
**Verify** `roster.recovery.signed_in` with outcome `success` is in the trail ([read the audit
trail](read-the-audit-trail.md)).
**Rollback**: sign out.

### 3. Repair, then turn recovery off

**Run** fix the directory group, then set `Recovery{Enabled}` false on `LambdaArgs`:

```go
off := false
sluispulumi.NewLambda(ctx, "kernel", &sluispulumi.LambdaArgs{
	// ...
	Recovery: &sluispulumi.RecoveryArgs{Enabled: &off},
})
```

or write `recovery: {enabled: false}` in the configuration yourself (the two must agree when both are set). Read the
Pulumi preview in full before the apply.
**Expect** a recovery sign-in is refused with a message saying that recovery is turned off and that turning it on is a
configuration change. The parameter and the secret stay.
**Verify** sign out and try the password: refused, recorded as `denied`.
**Rollback**: set `Enabled` true. **Turning it back on rotates nothing**: it is the same password.

### 4. Optional: rotate the password

**Run** `pulumi up --replace` the `RandomPassword` (`<name>-recovery-password`), after reading the preview.
**Expect** the next cold start of the function reads the new one (within five minutes for a warm one).
**Verify** read the parameter again; sign in with the new one.
**Rollback**: none, because the old value is gone from state; generate again.

## Afterwards

- The service keeps only an Argon2id digest of the password in memory and compares digests in constant time.
- Tell whoever read the password that it was read; rotate it if it was pasted anywhere it should not have been.
