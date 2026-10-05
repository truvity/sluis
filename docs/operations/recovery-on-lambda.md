# Recovery on Lambda

Recovery is the way in for the day no directory can vouch for anybody: the first
installation, before a directory is connected, and any day the directory or its
group is what is broken. In a cluster it is a short-lived ServiceAccount token
proven against the API server, and nothing is stored. A Lambda installation has no
API server to prove access to, so recovery is a **password**.

## What the Pulumi library does

`sluispulumi.NewLambda` generates the password and keeps it where the http function
reads it:

- a `random.RandomPassword`, 40 letters and digits with no look-alikes (`0 O 1 l I`
  are swapped for other letters), with no keepers, so an apply never rotates it;
- stored as the SSM SecureString `/sluis/<instance>/private/config/recovery/password`
  (under `ParameterKeyArn` when set; `<instance>` is the library's `Instance`), secret
  in state and in `pulumi up`'s output;
- named in the http function's document as `recovery.passwordSecret: recovery/password`
  (the library writes that key and `recovery.enabled`; a different value of your own
  is refused), and delivered by the document's `secrets` source (`ssm`, root
  `/sluis/<instance>`), which reads it from the parameter at cold start and again every
  five minutes. No file is written and no environment variable carries it;
- exported by name only: the `RecoveryPasswordParameter` output holds
  `/sluis/<instance>/private/config/recovery/password`, never the value.

The github and slack functions are not given it, and their roles cannot read it:
the library's policy gives them an explicit Deny on `ssm:Get*`, `Put` and `Delete`
under `/sluis/<instance>/private/config/` (the recovery password, the state secret, the
OAuth client and the declared clients), which is the http function's. The http role
reads `private/config/` and `private/credentials/` but writes only `private/credentials/`
(and `export/`): it never writes `config/`.

## Reading it

```sh
aws ssm get-parameter --with-decryption \
  --name /sluis/<instance>/private/config/recovery/password \
  --query Parameter.Value --output text
```

Reading it needs `ssm:GetParameter` on `/sluis/<instance>/private/config/*` (and `kms:Decrypt`
on `ParameterKeyArn` when one is set), which is the stack's operators and not the
Lambda roles' business (the http role may read it, for its `secrets` source;
the controllers' roles may not). The password is read from a terminal and not pasted into a
ticket or a chat.

## Signing in

Open `https://<issuer host>/console/login`, expand **Recovery sign-in** and paste the
password. The session is the console's own and carries the identity `recovery`; it
is an operator by construction. This is the console's sign-in page, so it is
reachable even where `console.client` sends everybody else to the issuer's page
(which, on Lambda, offers no recovery of its own: the issuer's token form needs a
cluster).

Every attempt is recorded as `roster.recovery.signed_in`, the refused ones too
(outcome `denied`, with the reason: `the proof was not accepted`, `too many refused
attempts`, `recovery sign-in is turned off`) and a failure to check as `failure`. A
refused attempt names nobody (`anonymous`), and none carries the password. A
successful one is written durably *before* the session exists, and no session is
issued when the audit trail cannot be written.

Refusals that cost nothing (recovery turned off, throttled) are recorded once a
minute per instance with a count, so a loop of requests cannot flood the trail; every
real check, right or wrong, is recorded.

On Lambda the password is never generated: with recovery on and no
`recovery.passwordSecret` the service logs an ERROR and builds no
recovery, rather than printing a password into the function's log.

After ten refused attempts within a minute a function instance answers 429 for the
next minute. The count lives in each instance, so it bounds the cost of guessing per
instance and not per installation; a 40-character generated password is not guessable
either way, and the limit exists because a check costs memory on purpose.

## On or off

`Recovery{Enabled}` on `LambdaArgs` writes `recovery.enabled` into the http
configuration. **The default is on**: a first installation has no other way in until
a directory is connected, and the console's setup checklist shows *Turn off the
recovery password* for as long as it is on. Once a group in your directory grants
operator, turn it off:

```go
off := false
sluispulumi.NewLambda(ctx, "kernel", &sluispulumi.LambdaArgs{
	// ...
	Recovery: &sluispulumi.RecoveryArgs{Enabled: &off},
})
```

or write `recovery: {enabled: false}` in the configuration yourself (the two must
agree when both are set). Off keeps the parameter and the secret file and refuses a
recovery sign-in with a message saying that recovery is turned off and that turning
it on is a configuration change. **Turning it back on rotates nothing**: it is the
same password. To rotate it deliberately, `pulumi up --replace` the
`RandomPassword` (`<name>-recovery-password`); the next cold start of the http function
reads the new one.

The service keeps only an Argon2id digest of the password in memory, and compares
digests in constant time.

## Outside the library

`recovery.passwordSecret` ([configuration](../reference/configuration.md#secrets)) is the
service's own key: for any installation that is not in a cluster, the NAME
(`recovery/password`) of the password, delivered by the document's `secrets` source
and read at start. Unset, the service generates one and prints it once.

## Hardening

- `API.KeepDefaultEndpoint: true` (for a cutover's acceptance run) serves the
  default `execute-api` endpoint without the custom domain's mutual TLS, so recovery
  is then reachable without the client certificate. Leave it off in service.
- Optionally cap the http function's reserved concurrency: each recovery check costs
  memory on purpose, and the throttle is per instance, so a ceiling on instances is a
  ceiling on guesses per minute.
