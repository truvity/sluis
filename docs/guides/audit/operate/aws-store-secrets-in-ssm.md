# Keep the writer's secrets in SSM

Give the writer Lambda a secret, such as an OpenBao token, by naming it in `Writer.Keys` and storing it as an SSM SecureString.

## Before you start

- Deploy the writer with the [Pulumi library](../../../reference/audit/aws-pulumi-library.md) and use version-2 configuration, which has the [`secrets` block](../../../reference/audit/configuration.md#secrets).

- A `...Env` key in `Writer.Keys` is refused at preview. Name the secret with `...Secret` instead, for example `tokenSecret: openbao/token`.

- The library never creates the parameters: a value passed through Pulumi is kept in stack state. A missing parameter fails the function's init at its first invocation.

- `Writer.Secrets.Root` must be under `/audit/`, with at least two segments, no wildcard, no trailing slash and no segment that starts with a dot. Plan time and the binary both refuse a name that climbs out of the root.

- A customer-managed key needs `Writer.Secrets.KeyArn`, or the writer lacks `kms:Decrypt`. Without a key the parameters use the AWS-managed `alias/aws/ssm`.

## Steps

1. Name the secret in the stack. Set `Writer.Secrets` only to change the default root.

   ```go
   Writer: auditpulumi.WriterArgs{
       Keys: map[string]any{"provider": "transit", "transit": map[string]any{
           "openbao": map[string]any{"address": "https://openbao.example.com:8200",
               "tokenSecret": "openbao/token"}}},
       // Secrets: &auditpulumi.SecretsArgs{Root: "/audit/audit/private/config", KeyArn: kmsKey},
   },
   ```

   The preview shows the layer's `audit.yaml` gaining `secrets: {source: ssm, root: ...}` and the writer role gaining the SSM statement. The `SecretsRoot` output is the root you expect.

2. Create the SecureString outside this program. The name is the root plus the secret's name.

   ```sh
   aws ssm put-parameter --type SecureString \
     --name "$(pulumi stack output SecretsRoot)/openbao/token" --value "$TOKEN"
   ```

3. Deploy and read the first invocation's log.

   ```sh
   pulumi up
   ```

## Verify

The trail gains an `audit.writer.started` record. A failure names the field, the source and the root, never the name or the value. To check the grant, run `aws ssm get-parameter --with-decryption --name <name>` as the writer's role, or `aws iam simulate-principal-policy`.

## Roll back

Remove the key from `Writer.Keys` and the grant goes with it. Delete the parameter with `aws ssm delete-parameter`. Run `pulumi up` with the previous program: the previous layer version is kept.

## Rotate

Write a new version of the parameter and deploy a new function version. The secret is read at cold start.

## What the library grants

`ssm:GetParameter` and `ssm:GetParameters` on `arn:aws:ssm:<region>:<account>:parameter<root>/*`. With `KeyArn`, also `kms:Decrypt` through SSM only, for parameters under the root only. The model is in [AWS Lambda](../../../concepts/audit/aws-lambda.md#why-no-secret-is-in-the-environment).
