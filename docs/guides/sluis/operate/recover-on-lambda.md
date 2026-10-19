# Recover on Lambda

Sign in with the recovery password on a Lambda installation when no directory can vouch for anybody, then turn it off.

## Before you start

- You need `ssm:GetParameter` on `/sluis/<instance>/internal/oidc/recovery-password`, and `kms:Decrypt` on `ParameterKeyArn` when set.

- Lambda has no API server, so recovery is a password. The browser, cookie and `429` refusals of [lost operator access](lost-operator-access.md) apply here, per warm container.

- `sluispulumi.NewLambda` generates a 40-character password that an apply never rotates. It stores the password as the SSM SecureString `/sluis/<instance>/internal/oidc/recovery-password`, and a salt and verifier derived from it at `.../recovery-verifier`. The service reads only the verifier, and the page never sends the password ([ADR 0076](../../../decisions/0076-password-sign-in-is-srp-for-recovery-and-conformance-only.md)).

- With no stored verifier, or no `recovery.verifierSecret` ([secrets](../../../reference/sluis/secrets.md#the-names)), every sign-in is refused.

- Recovery is on by default; the setup checklist shows *Turn off the recovery password*.

- Every attempt is recorded as `roster.recovery.signed_in` ([audit actions](../../../reference/sluis/audit-actions.md)). With the audit trail unwritable, no session is issued.

- `API.KeepDefaultEndpoint: true` skips the custom domain's mutual TLS: for a cutover acceptance run only.

## Steps

1. Read the password.

   ```sh
   # Refuses a terminal unless --show; --profile names an AWS profile.
   sluisctl recovery password --instance <instance> --region <region> | pbcopy

   # Without sluisctl: the parameter is a document, {"value":"<base64>"}.
   aws ssm get-parameter --with-decryption \
     --name /sluis/<instance>/internal/oidc/recovery-password \
     --query Parameter.Value --output text | jq -r .value | base64 --decode
   ```

2. Open `https://<issuer host>/console/login`, expand **Recovery sign-in**, type `recovery` and the password. You sign in as `recovery`, an operator by construction. The issuer's `/login` offers *Password sign-in* too. It signs in as `system:serviceaccount:sluis:recovery`, which needs a `service_account` matcher in a group the console client requires.

3. Fix the directory group, then turn recovery off. Sign in through your identity provider and press **Disable recovery** in Settings ([turn off recovery](turn-off-recovery.md)), or change the configuration. Read the Pulumi preview before the apply.

   ```go
   off := false
   sluispulumi.NewLambda(ctx, "acme", &sluispulumi.LambdaArgs{
   	// ...
   	Recovery: &sluispulumi.RecoveryArgs{Enabled: &off},
   })
   ```

   Or write `recovery: {enabled: false}` in the configuration; the two must agree. The parameter stays.

4. Optionally rotate the password: run `pulumi up --replace` on the `RandomPassword` (`<name>-recovery-password`) after reading the preview.

## Verify

The trail has `roster.recovery.signed_in` with outcome `success` ([read the audit trail](read-the-audit-trail.md)). After step 3 the password is refused and recorded as `denied`.

## Roll back

Sign out. To turn recovery back on, set `Enabled` true: the password is unchanged. After a turn-off from the console, also set a new `recovery.generation` in `Config`. Rotate the password if anyone pasted it elsewhere.

## Decided in

[Recovery](../../../concepts/sluis/recovery.md).
