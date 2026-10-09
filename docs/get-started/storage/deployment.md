# Where the backends run

storage links into the product's process, so nothing is deployed for it. A deployment chooses the backend each block names.

| Shape | `keys` | `state` |
|---|---|---|
| AWS Lambda ([sluis](../sluis/deployment/aws-lambda.md), [audit](../audit/README.md)) | KMS | the product's State (DynamoDB); secrets and blobs through the SSM and S3 backends |
| Kubernetes with AWS storage | KMS | as above |
| Kubernetes with OpenBao (*planned*, [0041](../../decisions/0041-the-secret-contract.md)) | OpenBao transit | OpenBao KV version 2 (`max_versions` 2 or more) |
| Blobs on an S3-compatible service such as R2 | KMS | the S3 backend with an endpoint, and credentials read from an `internal/` address |

The OpenBao backends exist and are tested. A product takes them through the block once its preset is built.
[Use the OpenBao backends](../../guides/storage/use-the-openbao-backends.md) lists the role and policies to write.
