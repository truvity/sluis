# Mint short-lived Cloudflare tokens and R2 credentials

Make sluis clone a disabled Cloudflare prototype token into expiring account tokens. R2 with static credentials needs none of this ([blobs on R2](operate/blobs-on-r2.md)).

## Before you start

- Set `secrets.source: ssm` and `secrets.layout: v4` or `transition`.

- The minter can mint anything the account owner can (checked 2026-10-08). Only sluis's refusal list guards the account.

- Keep the minter under `internal/` as a KMS-encrypted parameter (`secrets.kmsKeyId`), readable only by the function's role or the Kubernetes identity.

- sluis refuses an active or missing prototype, and one granting token admin, Billing, Account Settings, Memberships or Access identity rights. `cloudflare.forbiddenPermissionGroups` only adds names. A refusal is audited as `roster.cloudflare.token.refused`.

## 1. Create the minter token

Create an account API token with Account API Tokens Read and Write only. Keep it out of service documents, Pulumi inputs and command lines.

```sh
aws ssm put-parameter --type SecureString --name /sluis/<instance>/internal/cloudflare/main/minter \
  --value '{"schema":"cloudflare-minter/v1","token":"<the token>"}'
```

## 2. Create and disable a prototype

Create an account token with the rights a minted token should have, such as DNS Write on one zone. Then disable it with an update, since creation ignores `status: disabled`.

For R2, scope it to the resource key `com.cloudflare.edge.r2.bucket.<account-id>_default_<bucket>` (`_eu_` for the EU) with Bucket Item Read or Write.

## 3. Declare the preset and the grant

```yaml
cloudflare:
  accounts:
    main:
      id: <account-id>
      minter: internal/cloudflare/main/minter
  presets:
    dns-example:
      account: main
      prototype: <prototype token id>
      description: DNS edits on example.com for the release job
      lifetime: 15m
      rotation: 5m
    r2-archive:
      account: main
      prototype: <R2 prototype token id>
      endpoint: https://<account-id>.r2.cloudflarestorage.com
      lifetime: 15m
      rotation: 5m
```

`endpoint` makes a preset an R2 preset. `lifetime` sets each token's `expires_on`. `rotation`, shorter than `lifetime`, sets how often the stored credential is replaced.

In the policy, grant a group per preset. Pin a CI job by `github` matchers, never by workflow name:

```yaml
groups:
  all:ci:release:
    matchers:
      - github:
          repository: example-org/example-repo
          ref: refs/tags/v*
          job_workflow_ref: example-org/example-repo/.github/workflows/release.yml@refs/tags/v*
cloudflare:
  grants:
    - group: all:ci:release
      presets: [dns-example, r2-archive]
```

## 4. Mint for a person

```sh
sluisctl login
sluisctl whoami                       # lists your granted presets
eval "export $(sluisctl cloudflare token dns-example)"
sluisctl aws-config                   # adds a profile r2-archive@r2 per R2 preset
aws --profile r2-archive@r2 s3 ls s3://example-archive/
```

The CLI caches under `<config>/cloudflare/` and mints again with a third of the lifetime left.

## 5. Mint for CI or a workload

A workflow with `permissions: id-token: write` signs in as the job:

```yaml
- run: echo "CLOUDFLARE_API_TOKEN=$(sluisctl cloudflare token dns-example --format json | jq -r .token)" >> "$GITHUB_ENV"
```

A pod fed `external/cloudflare/<preset>` by a secrets operator needs no identity. `--file` refuses an expired document:

```ini
[profile archive]
credential_process = sluisctl cloudflare r2 r2-archive --file /var/run/secrets/cloudflare/r2-archive.json
```

The blob adapter mints its own R2 credentials with `ports.blob.s3.credentials: {preset: r2-blobs}`, exclusive with `credentialsRef`.

## 6. Verify

The stored credential is `external/cloudflare/<preset>` as `cloudflare/v1`. With the Pulumi library, declare `Installation.Cloudflare` ([Pulumi library](../../reference/sluis/pulumi-library.md)). The alert `AccessRosterCloudflareRotationStale` fires past twice the rotation.

## Roll back

Delete a live token by id. If it was the stored one, the next pass mints a replacement. 

## Decided in

[ADR 0070](../../decisions/0070-cloudflare-tokens-and-r2-credentials-by-prototype-clone.md).
