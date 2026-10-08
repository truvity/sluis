# AWS Lambda behind Cloudflare

The [AWS Lambda](aws-lambda.md) shape with Cloudflare in front: proxied DNS, and an API Gateway custom domain that
accepts only Cloudflare's client certificate, so a request that did not come through the proxy never reaches the
function. The module is `github.com/truvity/sluis/deploy/pulumi/edge/cloudflare`, a Go module of its own so that the
core library does not depend on an edge.

## What the module builds

- the ACM certificate for the domain, requested with DNS validation;
- the API Gateway custom domain with mutual TLS, pinned to one version of the truststore;
- the truststore: one versioned object, `truststore/client-ca.pem`, holding the Authenticated Origin Pulls CA.

It needs no Cloudflare credential and imports no Cloudflare provider. The DNS records (the proxied record and the ACM
validation record) and the zone's Authenticated Origin Pulls setting are the estate's, created with its own provider;
the module asks for a callback that creates the validation record.

## The truststore

API Gateway reads a truststore only from S3, so it is one versioned object, `truststore/client-ca.pem`, and the domain pins
its version: a new list of certificates is a new version and a redeploy. The bucket policy denies every write, delete and
re-label under `truststore/` to every principal that is not one of the identities the estate names (the role the stack is
applied with, the operators' admin role, a break-glass role), so the function cannot rewrite the list of certificates that
authenticate its own callers. The prefix is guarded where the bucket is declared (`StorageArgs.ProtectedPrefixes`, with
`edgecloudflare.Guard(...)`), and the module refuses a bucket that is not versioned or does not guard the prefix.

- **Blobs on S3.** The truststore lives in the installation's blob bucket, under the guarded prefix.
- **Blobs on R2.** There is no S3 blob bucket for API Gateway to read, so the module creates one small, versioned S3
  bucket for the truststore alone, with the same guard (`TruststoreBucket`).

The arguments are in [the Pulumi library](../../reference/pulumi-library.md#the-edge-modules).

## Moving a stack that built its own domain

Before this module, the core library built the custom domain and a truststore bucket from `API.DomainName`,
`API.CertificateArn`, `API.TruststorePEM` and `API.TruststoreBucketName`. Those four are deprecated: set, all four
together, the core still builds what it built (no diff) and warns. The move keeps the domain: the preview must show it
updated in place, never replaced, and the old truststore bucket is emptied by hand. The steps are in
[cut over](../../how-to/cutover.md#moving-a-stack-from-the-core-librarys-domain-to-the-edge-module).
