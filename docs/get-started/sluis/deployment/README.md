# Deployment shapes

sluis is one process everywhere ([0037](../../../decisions/0037-one-process-everywhere.md)); a shape is where that
process runs and what stands in front of it. One page per shape. A shape that is decided and not built is marked
*planned* and says only what is decided.

| Shape | State | Runs on | Front door | Page |
|---|---|---|---|---|
| AWS Lambda | available | Lambda, DynamoDB, S3, KMS | API Gateway with an ACM certificate (the estate's DNS) | [AWS Lambda](aws-lambda.md) |
| AWS Lambda behind Cloudflare | available | as above | Cloudflare proxy, Authenticated Origin Pulls, mutual TLS | [AWS behind Cloudflare](aws-behind-cloudflare.md) |
| AWS Lambda with the AWS edge module | *planned* | as above | API Gateway custom domain, ACM validated in Route 53 | [AWS edge](edge-aws.md) |
| Kubernetes with AWS storage | available | a pod on EKS; DynamoDB, S3, KMS | the cluster's gateway | [Kubernetes with AWS storage](../kubernetes-aws.md) |
| Kubernetes with OpenBao | *planned* | a pod; OpenBao KV and transit | the cluster's gateway | [Kubernetes with OpenBao](kubernetes-openbao.md) |

Which adapter serves which concern in each preset, and which presets are built, is generated from the registry:
[adapters](../../../reference/sluis/adapters.md). The decision that names the shapes, and moves secrets and keys behind one
contract, is [0041](../../../decisions/0041-the-secret-contract.md); audit installs beside sluis by default
([audit deployment](../../audit/README.md)).
