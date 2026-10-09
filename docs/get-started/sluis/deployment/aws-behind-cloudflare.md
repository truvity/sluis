# AWS Lambda behind Cloudflare

The [AWS Lambda](aws-lambda.md) shape with Cloudflare in front: proxied DNS and an API Gateway custom domain that accepts only Cloudflare's client certificate. A request that did not come through the proxy never reaches the function. The module is `github.com/truvity/sluis/deploy/pulumi/edge/cloudflare`.

## What the module builds

- The ACM certificate for the domain, requested with DNS validation (`Certificate`). Pass `CertificateArn` instead to supply your own.

- The API Gateway custom domain with mutual TLS, pinned to one version of the truststore.

- The truststore: one versioned object, `truststore/client-ca.pem`, holding the Authenticated Origin Pulls CA.

The module needs no Cloudflare credential and imports no Cloudflare provider. You create the proxied record, the ACM validation record and the zone's Authenticated Origin Pulls setting with your own provider. With `Certificate`, the module asks for a callback that creates the validation record.

## The truststore

API Gateway reads a truststore only from S3, and the domain pins one object version. A new list of certificates is a new version and a redeploy.

The bucket policy denies every write, delete and re-label under `truststore/` to every principal except the identities you name. Those are the apply role, the operators' admin role and a break-glass role. The function cannot rewrite the list that authenticates its callers.

Guard the prefix where you declare the bucket, with `StorageArgs.ProtectedPrefixes` and `edgecloudflare.Guard(...)`. The module refuses a bucket that is not versioned or does not guard the prefix.

- **Blobs on S3.** The truststore lives in the blob bucket under the guarded prefix.

- **Blobs on R2.** The module creates one small, versioned S3 bucket for the truststore alone, with the same guard (`TruststoreBucket`).

The arguments are in [the Pulumi library](../../../reference/sluis/pulumi-library.md#the-edge-modules).

## Move a stack that built its own domain

The arguments `API.DomainName`, `API.CertificateArn`, `API.TruststorePEM` and `API.TruststoreBucketName` are deprecated. When you set all four, the core library builds the domain itself with no diff and warns.

The preview must show the domain updated in place, never replaced. Empty the old truststore bucket by hand. The steps are in [cut over](../../../guides/sluis/migrate/move-the-domain-to-the-edge-module.md).
