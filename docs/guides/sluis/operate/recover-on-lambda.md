# Recover on Lambda

Sign in with the recovery password on a Lambda installation when no directory can vouch for anybody, then turn it off.

## Before you start

- You need `ssm:GetParameter` on `/sluis/<instance>/internal/config/*`, and `kms:Decrypt` on `ParameterKeyArn` when set.

- Lambda has no API server, so recovery is a password. The browser, cookie and `429` refusals of [lost operator access](lost-operator-access.md) apply here.

- `sluispulumi.NewLambda` generates a 40-character password that an apply never rotates. It stores it as the SSM SecureString `/sluis/<instance>/internal/config/recovery/password`, and the service reads it at cold start and every five minutes. The `RecoveryPasswordParameter` output holds the name, never the value.

- Without `recovery.passwordSecret` ([secrets](../../../reference/sluis/secrets.md#the-names)), recovery on Lambda logs an ERROR and builds nothing.

- Recovery is on by default, and the setup checklist shows *Turn off the recovery password* while it is.

- Every attempt is recorded as `roster.recovery.signed_in` ([audit actions](../../../reference/sluis/audit-actions.md)). With the audit trail unwritable, no session is issued.

- `API.KeepDefaultEndpoint: true` skips the custom domain's mutual TLS. Use it for a cutover acceptance run only. Ten refused attempts make one instance answer 429 for a minute, so consider capping reserved concurrency.

## Steps

1. Read the password. It is 40 characters.

   ```sh
   aws ssm get-parameter --with-decryption \
     --name /sluis/<instance>/internal/config/recovery/password \
     --query Parameter.Value --output text
   ```

2. Open `https://<issuer host>/console/login`, expand **Recovery sign-in** and paste the password. You sign in as `recovery`, an operator by construction. Use this page even when `console.client` sends everybody else to the issuer's page.

3. Fix the directory group, then turn recovery off. Read the Pulumi preview before the apply.

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

Sign out. To turn recovery back on, set `Enabled` true: the password is unchanged. Rotate it if anyone pasted it elsewhere.

## Decided in

[Recovery](../../../concepts/sluis/recovery.md).
