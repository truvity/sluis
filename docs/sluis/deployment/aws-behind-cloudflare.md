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

API Gateway reads a truststore only from S3. The bucket policy denies writes to `truststore/*` to every principal that
is not one of the apply identities the estate names, so the function cannot rewrite the list of certificates that
authenticate its own callers. An installation whose blobs are on R2 keeps one small, versioned S3 bucket for the
truststore alone, with the same policy.

## Planned changes

[0041](../../decisions/0041-the-secret-contract.md) moves the truststore into the installation's blob bucket and
deprecates the separate `TruststoreBucketName`. Until that ships, follow the module's own documentation
(`deploy/pulumi/edge/cloudflare/edge.go`) for the arguments.
