# Put the archive on an S3-compatible store (Cloudflare R2)

Put the archive on any store that speaks the S3 API at an endpoint of its own; Cloudflare R2 is the
one measured. Such a store has no Object Lock, so it holds the `operational` or `standard` preset and
refuses `attested`. Its integrity rests on the objects' hashes and the notary's seals.

## 1. Read the credentials sluis rotates

To keep no long-lived R2 secret and no Cloudflare minter in audit, declare an R2 preset in sluis
([mint short-lived Cloudflare tokens](../../sluis/cloudflare-tokens.md)). sluis keeps its current
credential at `external/cloudflare/<preset>` and replaces it every `rotation`. Name that address on
the audit preset:

```yaml
presets:
  operational:
    bucket: acme-audit
    prefix: operational/
    region: auto
    endpoint: https://<account>.r2.cloudflarestorage.com
    credentials_ref: external/cloudflare/audit-r2
```

Each process reads the document, reads it again within a minute and before it expires, and once
more after a 403. It mints nothing.

## 2. Give the Lambda functions the parameter

```go
auditpulumi.New(ctx, "main", &auditpulumi.Args{
    Presets: map[string]auditpulumi.PresetStorage{
        "operational": {
            Bucket:         "acme-audit",
            Prefix:         "operational/",
            Endpoint:       "https://<account>.r2.cloudflarestorage.com",
            CredentialsRef: "external/cloudflare/audit-r2",
        },
    },
    // The sluis installation's SSM root, and the key its SecureStrings are encrypted with.
    Sluis: &auditpulumi.SluisArgs{Root: "/sluis/main", KeyArn: "<sluis secrets key ARN>"},
    // ... Ingest, Writer; no Keys.Archive: the store encrypts at rest itself.
})
```

The library writes `archive: {sluisRoot: /sluis/main}` and grants the writer and the notary
`ssm:GetParameter` on `/sluis/main/external/cloudflare/audit-r2` only, plus `kms:Decrypt` through SSM.

## 3. Give the pods a projected file

Sync the document into a Secret with the external secrets operator, then mount it as a directory:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: audit-r2
spec:
  refreshInterval: 1m
  secretStoreRef: {kind: ClusterSecretStore, name: sluis-ssm}
  target: {name: audit-r2}
  data:
    - secretKey: audit-r2
      remoteRef: {key: /sluis/main/external/cloudflare/audit-r2}
```

```yaml
writer:            # and observe, query, jobs.* that read the archive
  config:
    archive: {sluisDir: /etc/audit/sluis}
  secretMounts:
    - {secretName: audit-r2, mountPath: /etc/audit/sluis/external/cloudflare}
```

Keep `refreshInterval` plus the kubelet's sync well under the sluis preset's `lifetime - rotation`.
Never mount the key with `subPath`: such a file does not follow the rotation. The pods need no AWS
identity; the operator's identity needs `ssm:GetParameter` on the one parameter.

## Static credentials

Without a sluis installation, write a token's pair once as a SecureString at the installation's
`internal/` address. It must not pass through Pulumi, which would keep it in the stack's state:

```sh
aws ssm put-parameter --type SecureString \
  --name /audit/main/internal/archive/operational \
  --value "$(printf '{"accessKeyID":"%s","secretAccessKey":"%s"}' "$ID" "$SECRET")"
```

Name it with `credentials: internal/archive/operational` in place of `credentials_ref`, and set
`archive.stateRoot: /audit/main`. `PresetStorage.CredentialsAddress` defaults to that address, and
the output `ArchiveCredentialsPaths` lists it per preset.

`credentials_preset` makes every process mint its own token with the Cloudflare minter, which can
mint anything the account owner can. Prefer `credentials_ref`.

## What does not carry over

- `key_alias` is refused on a preset at an endpoint, and so are `Create`,
  `Keys.Archive` and the other settings of an AWS bucket. Retention and tiering
  are the store's lifecycle rules, which you configure there.
- `Observe`, `Query` and `ArchiveWriter` are IAM roles over AWS buckets; they get
  nothing for a preset on an endpoint, whose workloads read it with the credentials
  above.

## The compressed-write checksum

Every record object is zstd-encoded. R2 answers `403 SignatureDoesNotMatch` when
the SDK chooses the checksum for an object that also has a `Content-Encoding`, so
the store computes the SHA-256 itself and sends the value, never the algorithm.
`store/s3store`'s tests hold that on the wire, and
`internal/s3test`'s compatible-store check runs it against a real store
(`AUDIT_S3_COMPATIBLE_BUCKET`, `AUDIT_S3_COMPATIBLE_ENDPOINT`).
