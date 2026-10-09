# Put the archive on an S3-compatible store

Store the archive on Cloudflare R2 or another store that speaks the S3 API at its own endpoint.

## Before you start

- A preset on an endpoint has no Object Lock, so it is `operational` or `standard`. The `attested` preset is refused at configuration and at store construction.

- `key_alias`, `Create`, `Keys.Archive` and the other AWS bucket settings are refused on such a preset. Set retention and tiering as lifecycle rules at the store. The `Observe`, `Query` and `ArchiveWriter` roles grant nothing here.


## Steps

1. Create the bucket and an API token that can read and write it. Write the pair once, by hand, as a SecureString. Keep it out of Pulumi and its stack state.

   ```sh
   aws ssm put-parameter --type SecureString \
     --name /audit/main/internal/archive/operational \
     --value "$(printf '{"accessKeyID":"%s","secretAccessKey":"%s"}' "$ID" "$SECRET")"
   ```

   The output `ArchiveCredentialsPaths` lists the names per preset.

2. Add the store as a preset in the deployment document. An installation can keep another preset on AWS S3 beside it.

   ```yaml
   presets:
     operational:
       bucket: acme-audit
       prefix: operational/
       region: auto
       endpoint: https://<account>.r2.cloudflarestorage.com
       credentials: internal/archive/operational   # below the state root
   ```

   With the Pulumi library, `Region` defaults to `auto` and `CredentialsAddress` to `internal/archive/operational`. Leave `Keys.Archive` unset.

   ```go
   auditpulumi.New(ctx, "main", &auditpulumi.Args{
       Presets: map[string]auditpulumi.PresetStorage{
           "operational": {
               Bucket:   "acme-audit",
               Prefix:   "operational/",
               Endpoint: "https://<account>.r2.cloudflarestorage.com",
           },
       },
       // ... Ingest, Writer
   })
   ```

3. Name where the process reads the credentials.

   ```yaml
   archive:
     stateRoot: /audit/main
   ```

   On Kubernetes the chart renders `presets` into the deployment document. The pod identity needs `ssm:GetParameter` on the credentials parameter.

## Mint R2 credentials instead

Use `credentials_preset` instead of `credentials` to have the process clone a disabled Cloudflare prototype token.

```yaml
presets:
  operational:
    bucket: audit-example
    region: auto
    endpoint: https://<account-id>.r2.cloudflarestorage.com
    path_style: true
    credentials_preset:
      account: <account-id>
      minter: internal/cloudflare/main/minter   # below archive.stateRoot; cloudflare-minter/v1
      prototype: <id of the disabled prototype token>
      lifetime: 15m
```

The minter is `{"schema": "cloudflare-minter/v1", "token": "..."}` in the state store. The process refuses an active prototype and one granting token admin, billing, account settings, memberships or Access identity providers. It records minted token ids at `cloudflare-minted/<preset>` and deletes expired ones at the next mint.

## Other store settings

Set `path_style: true` when the certificate does not cover a bucket subdomain. Set `archive.ca` for a private CA; the chart's `trust` mounts a bundle at `/etc/audit/trust/<key>`. The store must support conditional writes (`If-None-Match`) and `ListObjectsV2` continuation. The keys are in [profiles](../../../reference/audit/profiles.md).

## Verify

Run `audit verify` on a range written after the change.

## Trap: the compressed-write checksum

R2 answers `403 SignatureDoesNotMatch` when the SDK picks the checksum for an object that also has a `Content-Encoding`. Record objects are zstd-encoded, so the store sends its own SHA-256 value. Set `AUDIT_S3_COMPATIBLE_BUCKET` and `AUDIT_S3_COMPATIBLE_ENDPOINT` to run `internal/s3test` against a real store.

## Decided in

[0068 Storage is configured per preset](../../../decisions/0068-storage-is-configured-per-preset.md), [0070 Cloudflare tokens and R2 credentials](../../../decisions/0070-cloudflare-tokens-and-r2-credentials-by-prototype-clone.md).
