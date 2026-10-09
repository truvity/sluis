# Move the domain to the edge module

Move a stack's custom domain, certificate and truststore from the core library to [the edge module](../../../reference/sluis/pulumi-library.md#the-edge-modules) without replacing the domain. DNS is untouched and nothing is down.

## Before you start

- Run a release that has the edge module, with `API.DomainName`, `API.CertificateArn`, `API.TruststorePEM` and `API.TruststoreBucketName` set. The core library deprecates all four.
- Name the roles that may write the truststore: the CD role, the operators' admin role and a break-glass role. Any other principal, the functions' role included, is denied.
- The edge's domain and mapping are aliased to the core's. A different Lambda component name or stack breaks the alias.

## Steps

### 1. Guard the blob bucket

```go
Versioning: true,
ProtectedPrefixes: []sluispulumi.ProtectedPrefix{edgecloudflare.Guard(cdRole, adminRole, breakglassRole)},
```

Versioning keeps old versions of every object, so set a lifecycle rule if that matters. On R2 skip this and give the edge a `TruststoreBucket`. Run `pulumi up` alone: the preview changes only the bucket's versioning and policy.

### 2. Add the edge

Remove the four `API` inputs and keep `API.KeepDefaultEndpoint`. Add `edgecloudflare.NewEdge` with the same domain, `CertificateArn`, `TruststorePEM` and `Storage`.

### 3. Unprotect the old truststore bucket

```sh
pulumi stack --show-urns
pulumi state unprotect 'urn:pulumi:<stack>::<project>::sluis:aws:Lambda$aws:s3/bucket:Bucket::<l>-truststore'
```

`<l>` is the Lambda component's name.

### 4. Preview

Expect the new object `<edge>-truststore-pem` created in the blob bucket, and the domain `<l>-domain` updated in place (`truststoreUri`, `truststoreVersion`). Expect the old object, policy, public-access block, versioning, encryption and bucket deleted. A replace of the domain or mapping is wrong: stop.

### 5. Apply and finish

Run `pulumi up`. The old bucket fails with `BucketNotEmpty`; the domain is already on the new truststore. Delete every version and delete marker, then apply again:

```sh
aws s3api list-object-versions --bucket <old-bucket>
aws s3api delete-objects --bucket <old-bucket> --delete file://batch.json
aws apigatewayv2 get-domain-name --domain-name <domain>
```

Repeat the first two until the listing is empty. `MutualTlsAuthentication.TruststoreUri` must name the blob bucket. Ask the issuer with a client certificate ([tutorial](../../../get-started/sluis/aws-lambda.md#6-point-dns-at-it-and-ask-the-issuer)).

To keep the old bucket, run `pulumi state delete --force` on its six resources after step 3: the bucket, `-encryption`, `-versioning`, `-public-access`, `-policy` and `<l>-truststore-pem`. Skip the emptying.

## Roll back

Before step 5, revert the program. After it, restore the four inputs and apply. The library builds its truststore bucket again, so the old bucket's name must be free. The domain updates back in place.
