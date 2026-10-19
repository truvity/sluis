# Where the backends run

storage links into the product's process, so nothing is deployed for it. A deployment chooses the backend each block names.

| Shape | `keys` | `state` |
|---|---|---|
| AWS Lambda ([sluis](../sluis/deployment/aws-lambda.md), [audit](../audit/README.md)) | KMS | the product's State (DynamoDB); secrets and blobs through the SSM and S3 backends |
| Kubernetes with AWS storage | KMS | as above |
| Kubernetes with OpenBao ([0041](../../decisions/0041-the-secret-contract.md)) | OpenBao transit: built for audit, planned for sluis signing | OpenBao KV version 2 (`max_versions` 2 or more): no product takes it yet |
| Blobs or the audit archive on an S3-compatible service such as R2 | KMS | the SSM backend holds the credentials, read from an `internal/` address |

The OpenBao backends exist and are tested. audit takes transit through `keys.adapter: transit`.
[Use the OpenBao backends](../../guides/storage/use-the-openbao-backends.md) lists the role and policies to write.
