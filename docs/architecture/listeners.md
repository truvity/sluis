---
title: Listeners
description: "What accepts connections in sluis and where: Unix sockets only, the edge connector reaching central's socket, and the written-down exceptions."
status: target
sidebar:
  order: 5
---

:::note[Proposed]
This page is the design as proposed, awaiting the maintainer's review. The exceptions are written down as exceptions on purpose.
:::

A role or driver that listens on a TCP port can be reached by anything that can route to the port. A role that listens on a Unix socket can be reached only by a process that can open the file, and the receiver learns the caller's uid. sluis uses the second kind everywhere it can.

## The rule

- **Roles and drivers listen only on Unix sockets under `/run/sluis/<role>/`.** The public API of `central` is `/run/sluis/central/api.sock`.
- **Only `central`'s public API ever gets a TCP listener**, and only on the shapes where the platform needs one (below). Never a key-holding role, never a driver.
- **Every socket is mode 0660, with a group generated from the manifest.** The group is the set of roles allowed to call the socket. The callee reads the caller's uid, gid and pid with `SO_PEERCRED`, maps the uid to a role, and checks the call graph, default deny.

An example of the socket directory on one node (illustrative):

```text
/run/sluis/
  central/api.sock          public API; group: edge connector
  central/rpc.sock          internal calls from other roles
  signer/rpc.sock           group: central
  policy/rpc.sock           group: central, integrations
  key-holder/rpc.sock       shared driver; ACL by peer credentials
  state-owner/rpc.sock      shared driver; ACL by peer credentials
  audit-writer/rpc.sock     group: every role that emits
  node-agent/cred.sock      workload JWT; one answer per uid
```

## The edge connector reaches a socket, not a port

On self-hosted shapes, the edge connector is `cloudflared` running as a tunnel. Its ingress rules accept a `unix:` service, so it forwards to `central`'s socket directly and the node opens no inbound port. The connector makes outbound connections only.

An example `config.yaml` (illustrative; the tunnel id and domain are placeholders):

```yaml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json

ingress:
  - hostname: sluis.example.com
    service: unix:/run/sluis/central/api.sock
  - service: http_status:404
```

Two details make this safe:

- The `cloudflared` user is the only member of the socket's group, so only the connector can open `api.sock`. No other process on the node reaches the public API except through its own authenticated call.
- `central` holds no key. The role behind the unauthenticated listener cannot sign a token or read the ring; it must ask `signer`, whose socket `cloudflared` cannot open.

## The exceptions

These are the places where a TCP listener exists, and why.

| Exception | Where | Why | What it exposes |
| --------- | ----- | --- | --------------- |
| **Lambda Web Adapter** | On the AWS Lambda shape. The adapter forwards the function's HTTP events to a loopback TCP port inside the sandbox, such as `127.0.0.1:8080` | The Lambda runtime has no Unix-socket front. The adapter is vendored into our own layer, pinned to v1.1.0 | A loopback port inside a micro-VM that holds only our own processes. It is not reachable from the network |
| **Kubernetes pod port** | On the Kubernetes shape. The pod port the gateway routes to | The gateway reaches a pod by address, so the public API needs a port the gateway can route to | One port on `central`, the public API only. Every other role listens on a Unix socket in its pod, or on a cluster-internal port that a default-deny NetworkPolicy opens only to the callers in the call graph |
| **Raft peer port** | On cluster shapes (T2 and T4). The state driver's own TCP port between nodes | Raft replicas must talk to each other across machines | Raft traffic only, over mTLS, and limited to the other nodes' addresses by the firewall |

:::caution[Open decision OD-4: Which TCP ports exist between nodes]
The Raft driver port over mTLS is the one inter-node port. Whether any other inter-node port exists, such as a role port so that a node can reach a role on a peer, is undecided. The options are: only the Raft driver port, or that plus a role port. The lean is the Raft driver port only.
:::

## Local development and tests

On the local shape (T0) the same sockets live in a runtime directory inside the container, and a test harness may expose `central`'s socket on a loopback port for a browser. That port is a development convenience and is not a shape-level exception: no production shape opens it.

## Related

- [Processes](/architecture/processes/) shows the roles behind these sockets.
- [Deployment](/architecture/deployment/) describes the firewall rules and sandboxing that keep the exceptions narrow.
- [Shapes](/shapes/) lists what each shape opens: [EC2 cluster](/shapes/aws-ec2-cluster/), [Lambda](/shapes/aws-lambda/) and [Kubernetes](/shapes/kubernetes/) carry the exceptions.
