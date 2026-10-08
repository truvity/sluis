# Put the archive on an S3-compatible store (Cloudflare R2)

The archive may live on any store that speaks the S3 API at an endpoint of its
own. Cloudflare R2 is the one this has been measured against. Such an archive is
written with **no Object Lock** (the lock is an AWS S3 guarantee another store
does not make, so the `attested` preset on an endpoint is refused at configuration
and at store construction), so it holds the `operational` or `standard` preset,
not `attested`. Its integrity rests on the objects' hashes and, with the
notary, on the seals.

## 1. The bucket and its credentials

Create the bucket at the store, and an API token that can read and write it.
Write the pair as a SecureString at the installation's `internal/` address, as a
JSON object, once, by hand. It must not pass through Pulumi, which would keep it
in the stack's state:

```sh
aws ssm put-parameter --type SecureString \
  --name /audit/main/internal/archive/operational \
  --value "$(printf '{"accessKeyID":"%s","secretAccessKey":"%s"}' "$ID" "$SECRET")"
```

The Pulumi library's output `ArchiveCredentialsPaths` lists the names, per preset.

## 2. The installation

The store is the storage of a **preset**, in the deployment document
([0068](../../decisions/0068-storage-is-configured-per-preset.md)). A preset on an
endpoint is `operational` or `standard`; an installation may keep another preset
on AWS S3 beside it.

```yaml
presets:
  operational:
    bucket: acme-audit
    prefix: operational/
    region: auto
    endpoint: https://<account>.r2.cloudflarestorage.com
    credentials: internal/archive/operational   # below the state root
```

With the Pulumi library:

```go
auditpulumi.New(ctx, "main", &auditpulumi.Args{
    Presets: map[string]auditpulumi.PresetStorage{
        "operational": {
            Bucket:   "acme-audit",
            Prefix:   "operational/",
            Endpoint: "https://<account>.r2.cloudflarestorage.com",
            // Region defaults to "auto", which R2 signs with.
            // CredentialsAddress defaults to internal/archive/operational.
        },
    },
    // ... Ingest, Writer; no Keys.Archive: the store encrypts at rest itself.
})
```

The process configuration names where the credentials are read from:

```yaml
archive:
  stateRoot: /audit/main
```

On Kubernetes the chart renders `presets` into the deployment document and the
component's `config.archive` carries the state root; the pod's identity needs
`ssm:GetParameter` on the credentials parameter.

## What does not carry over

- `key_alias` is refused on a preset at an endpoint, and so are `Create`,
  `Keys.Archive` and the other settings of an AWS bucket. Retention and tiering
  are the store's lifecycle rules, which you configure there.
- An `attested` preset is refused at an endpoint: Object Lock is S3 only.
- `Observe`, `Query` and `ArchiveWriter` are IAM roles over AWS buckets; they get
  nothing for a preset on an endpoint, whose workloads read it with the credentials
  in the state store.

## The compressed-write checksum

Every record object is zstd-encoded. R2 answers `403 SignatureDoesNotMatch` when
the SDK chooses the checksum for an object that also has a `Content-Encoding`, so
the store computes the SHA-256 itself and sends the value, never the algorithm.
`store/s3store`'s tests hold that on the wire, and
`internal/s3test`'s compatible-store check runs it against a real store
(`AUDIT_S3_COMPATIBLE_BUCKET`, `AUDIT_S3_COMPATIBLE_ENDPOINT`).
