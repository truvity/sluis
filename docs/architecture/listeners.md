---
title: Listeners
description: "What accepts connections in sluis and where: the entry API on loopback TCP, every other role and driver on Unix sockets, and the Raft port between nodes."
status: target
sidebar:
  order: 5
---

A role or driver that listens on a TCP port can be reached by anything that can route to the port. A role that listens on a Unix socket can be reached only by a process that can open the file, and the receiver learns the caller's uid. sluis uses the second kind everywhere except for the one API that has to be public.

## The rule

- **`entry`'s public API listens on TCP, bound to `127.0.0.1`, on every shape.** It is the only role that has a TCP listener. The platform's edge reaches that loopback port: `cloudflared` on the self-hosted shapes, the Lambda Web Adapter on Lambda.
- **Every other role and every driver listens on Unix sockets only**, under `/run/sluis/<role>/`. Never a key-holding role, never a driver, has a TCP listener.
- **The Raft driver port over mTLS is the only TCP port between nodes.** No role is reachable from another node.
- **Every Unix socket is mode 0660, with a group generated from the manifest.** The group is the set of roles allowed to call the socket. The callee reads the caller's uid, gid and pid with `SO_PEERCRED`, maps the uid to a role, and checks the call graph, default deny.

:::note[Proposed]
On Kubernetes the group cannot do this job, because supplementary groups are set for a whole pod and not for a container. The proposal is that reachability is generated from the manifest **by group on a host and by mount on Kubernetes**: each callee has its own volume for its socket directory, mounted read-only into exactly the containers that the call graph allows. The callee still maps the peer's user id to a role and checks the call graph. The pid is not used on Kubernetes, because containers have separate PID namespaces. See [Kubernetes](/shapes/kubernetes/#what-runs-where).
:::

:::note[Proposed]
**An exception for the edge relay (OD-24).** The rule above says that only `entry`'s API listens on TCP. On Kubernetes the proposal adds one exception: a key-less **edge relay** in the same pod listens on the pod address and forwards to `entry` on `127.0.0.1`, so that a Gateway API implementation or an Ingress controller can route to the pod. The relay holds no key. `entry` itself stays on `127.0.0.1`. If the maintainer prefers to keep the rule as it is, the alternative is `cloudflared` only on Kubernetes, at the price of working with a gateway. See [OD-24](/open-decisions/#od-24).
:::

The `entry` listener is bound to the loopback address, never to `0.0.0.0` or a pod or network address (on Kubernetes see below), so the node opens no inbound port: the security group or host firewall needs no inbound rule for sluis. The port is set in configuration; this site uses `8080` as an example.

An example of the socket directory on one node (illustrative):

```text
/run/sluis/
  entry/rpc.sock            internal calls from other roles
  signer/rpc.sock           group: entry
  policy/rpc.sock           group: entry, integrations
  key-holder/rpc.sock       shared driver; ACL by peer credentials
  state-owner/rpc.sock      shared driver; ACL by peer credentials
  audit-writer/rpc.sock     group: every role that emits
  node/cred.sock            workload JWT; one answer per uid

127.0.0.1:8080              entry's public API (TCP, loopback only)
```

## The edge connector reaches a loopback port

On self-hosted shapes, the edge connector is `cloudflared` running as a tunnel. It connects to `entry`'s loopback port with an ordinary `http://` service, and makes outbound connections only. The node accepts no inbound connection from the network.

An example `config.yaml` (illustrative; the tunnel id, the domain and the port are placeholders):

```yaml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json

ingress:
  - hostname: sluis.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```

Three details keep this safe:

- **The listener is public by design.** The API in front of the connector is the one that anyone on the internet may call, and it authenticates every request itself (a sign-in, a client credential, a token). A loopback port that other local processes can also open therefore adds no capability that the public API does not already offer.
- `entry` holds no key. The role behind the unauthenticated listener cannot sign a token or read the ring; it must ask `signer`, whose Unix socket neither `cloudflared` nor any other process outside the call graph can open.
- `entry` binds `127.0.0.1` only, and the host firewall drops inbound traffic to every other port, so nothing but a local process reaches the listener.

## Where the TCP listener is, per shape

| Shape | Listener | Who connects |
| ----- | -------- | ------------ |
| T0 Local | `127.0.0.1:<port>` inside the container | The developer's browser or test harness, through a published port |
| T1, T2 boards | `127.0.0.1:<port>` | `cloudflared` on the same board |
| T3, T4 EC2 | `127.0.0.1:<port>` | `cloudflared` on the same instance |
| T5 Lambda | `127.0.0.1:<port>` inside the function's sandbox | The Lambda Web Adapter, pinned at v1.1.0 and vendored into sluis's own layer. The edge in front of the function is API Gateway |
| T6 Kubernetes (proposed) | `entry` on `127.0.0.1:<port>`, bridged in the pod, as below | An edge relay in the same pod (proposed default), or `cloudflared` |

On Lambda the loopback port is inside a micro-VM that holds only sluis's own processes. It is not reachable from the network.

:::note[Proposed]
On Kubernetes the loopback port is not reachable from outside the pod, and a gateway reaches a pod by its address. The proposal keeps the listener on `127.0.0.1` and bridges it from inside the pod, in one of two modes: a key-less edge relay on the pod address that forwards to `entry`, which is the default, or a `cloudflared` sidecar that connects to the loopback port and accepts no inbound connection. The kubelet probes `entry` with an `exec` probe, which runs inside the container's network namespace. Binding the pod address in `entry` is not proposed, because it would put an unauthenticated listener on the pod network. Details and options are on [Kubernetes](/shapes/kubernetes/#edge) and in [OD-24](/open-decisions/#od-24).
:::

## The only port between nodes

On cluster shapes (T2 and T4) the state driver's own TCP port carries Raft traffic between the nodes. It is the only TCP port between nodes, and nothing else crosses a node boundary. The traffic runs over mTLS, and the firewall limits the port to the other nodes' addresses. No role listens on a TCP port so that a peer could call it: a role that needs state on another node asks its own node's state driver, and the driver talks Raft.

## Local development and tests

On the local shape (T0) the same sockets live in a runtime directory inside the container and `entry`'s loopback port is the container's own. A test harness or a developer publishes that port to the workstation to reach it from a browser. The published port is a development convenience; production shapes never publish it.

## Related

- [Processes](/architecture/processes/) shows the roles behind these sockets.
- [Deployment](/architecture/deployment/) describes the firewall rules and sandboxing that keep the listeners narrow.
- [Shapes](/shapes/) lists what each shape opens: [single node](/shapes/single-node/), [EC2 cluster](/shapes/aws-ec2-cluster/), [Lambda](/shapes/aws-lambda/) and [Kubernetes](/shapes/kubernetes/).
