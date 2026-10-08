# Put the archive on an S3-compatible store (Cloudflare R2)

The archive may live on any store that speaks the S3 API at an endpoint of its
own. Cloudflare R2 is the one this has been measured against. Such an archive is
written with **no Object Lock** (the lock is an AWS S3 guarantee another store
does not make, so a lock mode on an endpoint is refused at configuration and at
store construction) and is therefore an `operational` or `standard` installation,
not an `attested` one. Its integrity rests on the objects' hashes and, with the
notary, on the seals.

## 1. The bucket and its credentials

Create the bucket at the store, and an API token that can read and write it.
Write the pair as a SecureString at the installation's `internal/` address, as a
JSON object, once, by hand. It must not pass through Pulumi, which would keep it
in the stack's state:

```sh
aws ssm put-parameter --type SecureString \
  --name /audit/main/internal/archive \
  --value "$(printf '{"accessKeyID":"%s","secretAccessKey":"%s"}' "$ID" "$SECRET")"
```

The Pulumi library's output `ArchiveCredentialsPath` is that name.

## 2. The installation

```go
auditpulumi.New(ctx, "main", &auditpulumi.Args{
    Archive: auditpulumi.ArchiveArgs{
        BucketName: "acme-audit",
        Endpoint:   "https://<account>.r2.cloudflarestorage.com",
        // StoreRegion defaults to "auto", which R2 signs with.
    },
    // ... Ingest, Writer; no Keys.Archive: the store encrypts at rest itself.
})
```

The writer's configuration then reads:

```yaml
archive:
  bucket: {name: acme-audit, endpoint: "https://<account>.r2.cloudflarestorage.com", region: auto}
  lockMode: none
  credentials: {root: /audit/main, address: internal/archive}
```

On Kubernetes the same block goes in the component's `config`; the pod's
identity needs `ssm:GetParameter` on the credentials parameter.

## What does not carry over

- `Archive.ObjectLockMode`, `DefaultRetentionDays`, `Encryption`, `KeyArn`,
  `Keys.Archive`, `GlacierIRDays` and `DeepArchiveDays` are the settings of an AWS
  bucket and are refused with an endpoint. Retention and tiering are the store's
  lifecycle rules, which you configure there.
- `Observe`, `Query` and `ArchiveWriter` are IAM roles over an AWS bucket. The
  workloads read the archive with the same credentials.

## The compressed-write checksum

Every record object is zstd-encoded. R2 answers `403 SignatureDoesNotMatch` when
the SDK chooses the checksum for an object that also has a `Content-Encoding`, so
the store computes the SHA-256 itself and sends the value, never the algorithm.
`store/s3store`'s tests hold that on the wire, and
`internal/s3test`'s compatible-store check runs it against a real store
(`AUDIT_S3_COMPATIBLE_BUCKET`, `AUDIT_S3_COMPATIBLE_ENDPOINT`).
