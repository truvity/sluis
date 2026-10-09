# AWS edge module

*Planned.* [0041](../../../decisions/0041-the-secret-contract.md) decides it; the module
`deploy/pulumi/edge/aws` does not exist yet.

The shape is the [AWS Lambda](aws-lambda.md) shape with AWS in front of it: an API Gateway custom domain, an ACM
certificate validated in Route 53, and the Route 53 records. There is no mutual TLS, because nothing proxies to the
domain. Until the module is built, put a domain in front of `Lambda.FrontDoor()` with the estate's own Pulumi code, or
use [the Cloudflare edge](aws-behind-cloudflare.md).
