# Kubernetes with OpenBao

*Planned.* [0041](../../../decisions/0041-the-secret-contract.md) decides it; the OpenBao backends it needs are in the
[storage module](../../../concepts/storage/README.md) and the preset that names them is not built yet
([adapters](../../../reference/sluis/adapters.md#availability)).

What is decided: the charts sit behind the cluster's gateway, so there is no truststore; State (DynamoDB) and the blob
bucket are kept and reached through workload identity ([0030](../../../decisions/0030-workload-identity-on-both-platforms.md));
OpenBao KV (State) and transit (keys) are reached through the JWT auth method with the projected ServiceAccount token;
audit records go over HTTP to the in-cluster audit writer.

What exists today for a cluster is [Kubernetes with AWS storage](../kubernetes-aws.md).
