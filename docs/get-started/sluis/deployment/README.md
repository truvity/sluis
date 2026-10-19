# Deployment shapes

A shape is where the sluis process runs and what stands in front of it. Decided in: [0037](../../../decisions/0037-one-process-everywhere.md), [0041](../../../decisions/0041-the-secret-contract.md).

| Shape | State | Runs on | Front door | Page |
|---|---|---|---|---|
| AWS Lambda | available | Lambda, DynamoDB, S3, KMS | API Gateway with an ACM certificate | [AWS Lambda](aws-lambda.md) |
| AWS Lambda behind Cloudflare | available | as above | Cloudflare proxy, Authenticated Origin Pulls, mutual TLS | [AWS behind Cloudflare](aws-behind-cloudflare.md) |
| AWS Lambda with the AWS edge module | planned | as above | API Gateway custom domain, ACM validated in Route 53 | below |
| Kubernetes with AWS storage | available | a pod on EKS; DynamoDB, S3, KMS | the cluster gateway | [Kubernetes with AWS storage](../kubernetes-aws.md) |
| Kubernetes with OpenBao | planned | a pod; OpenBao KV and transit | the cluster gateway | below |

## AWS edge module

The module `deploy/pulumi/edge/aws` does not exist yet. It fronts the [AWS Lambda](aws-lambda.md) shape with an API Gateway custom domain, an ACM certificate validated in Route 53 and the Route 53 records. There is no mutual TLS. Until it exists, put a domain in front of `Lambda.FrontDoor()` with your own Pulumi code, or use [the Cloudflare edge](aws-behind-cloudflare.md).

## Kubernetes with OpenBao

The preset is not built; see [adapters](../../../reference/sluis/adapters.md#availability). The OpenBao backends are in the [storage module](../../../concepts/storage/README.md). Today, use [Kubernetes with AWS storage](../kubernetes-aws.md). Audit installs beside sluis by default; see [audit deployment](../../audit/README.md).
