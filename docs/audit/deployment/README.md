# Deployment shapes

audit is three parts installed independently (writer, notary, observe and query), tied together by the bucket layout
([0058](../../decisions/0058-three-parts-installed-independently.md)). A shape is where they run. One installation
serves one application ([0053](../../decisions/0053-one-installation-per-service-or-product.md)).

| Shape | State | Where | Start at |
|---|---|---|---|
| AWS Lambda | available | writer and notary as Lambda functions behind an SQS queue, built by the Pulumi library | [Getting started on AWS Lambda](../getting-started/aws-lambda.md) |
| Kubernetes, direct | available | the writer in the application's namespace, rendered by the chart | [Getting started on Kubernetes](../getting-started/kubernetes.md) |
| Kubernetes, stream | available | a receiver publishing to a stream, N consumers | [Run stream mode](../how-to/run-stream-mode.md) |
| Writer on Lambda, readers in Kubernetes | available | the two combined | [Run readers in Kubernetes](../how-to/aws-run-readers-in-kubernetes.md) |
| Kubernetes with OpenBao | *planned* | the chart behind the cluster's gateway; keys on OpenBao transit; State on OpenBao KV | below |
| AWS Lambda behind an edge module | *planned* | audit's front door from the sluis edge modules | below |

The explanations behind the choice: [deployment shapes](../explanation/deployment-shapes.md),
[direct mode](../explanation/direct-mode.md), [stream mode](../explanation/stream-mode.md),
[AWS Lambda](../explanation/aws-lambda.md). A sluis installation connects to audit by installing it beside it
([getting started, sluis-connected](../getting-started/sluis.md)).

## Planned

[0041](../../decisions/0041-the-secret-contract.md) decides that audit's keys and State are chosen by the shared
[adapter block](../../storage/explanation/adapter-block.md), so the Kubernetes shape can use OpenBao transit for the
`seal`, `pseudonym`, `conceal` and `archive` purposes and OpenBao KV for State. The backends exist in the
[storage module](../../storage/README.md); the chart and the Pulumi library do not take the block yet, and the pages
that describe key providers today ([key custody](../explanation/key-custody.md),
[configure OpenBAO keys](../how-to/configure-openbao-keys.md)) describe the present behaviour.
