---
title: Listeners
description: "What accepts connections in sluis and where: the entry API behind the edge, every other actor and driver on Unix sockets, and the Raft ports between nodes."
status: target
sidebar:
  order: 5
---

An actor or driver that listens on a TCP port can be reached by anything that can route to the port. An actor that listens on a Unix socket can be reached only by a process that can open the file, and the receiver learns the caller's uid. sluis uses the second kind everywhere except for the one API that has to be public.

## The rule

- **`entry`'s public API is the only listener that faces the edge.** On bare-metal it is a Unix socket, and `cloudflared` forwards to it as a Unix-socket origin, so `entry` runs with `PrivateNetwork=yes` and no network at all. Where the edge cannot reach a Unix socket, the listener is TCP bound to `127.0.0.1`: the Lambda Web Adapter on serverless, the edge relay on Kubernetes.
- **Every other actor and every driver listens on Unix sockets only**, under `/run/sluis/<actor>/`, one socket per instance, `/run/sluis/<actor>/<boundary>.sock`. Never a key-holding actor, never a driver, has a TCP listener.
- **The Raft ports of the rqlite clusters, over mTLS, are the only TCP ports between nodes.** No actor is reachable from another node.
- **Every Unix socket is mode 0660, with a group generated from the manifest.** The group is the set of actors allowed to call the socket. The callee reads the caller's uid, gid and pid with `SO_PEERCRED`, maps the uid to an actor, and checks the call graph, default deny. `key-holder` goes further: each caller has a socket of its own, in a directory only that caller can open.

:::note[Proposed]
On Kubernetes the group cannot do this job, because supplementary groups are set for a whole pod and not for a container. The proposal is that reachability is generated from the manifest **by group on a host and by mount on Kubernetes**: each callee has its own volume for its socket directory, mounted read-only into exactly the containers that the call graph allows. The callee still maps the peer's user id to an actor and checks the call graph. The pid is not used on Kubernetes, because containers have separate PID namespaces. See [Kubernetes](/runtimes/kubernetes/#what-runs-where).
:::

:::note[Proposed]
**An exception for the edge relay (OD-24).** The rule above says that only `entry`'s API listens on TCP. On Kubernetes the proposal adds one exception: a key-less **edge relay** in the same pod listens on the pod address and forwards to `entry` on `127.0.0.1`, so that a Gateway API implementation or an Ingress controller can route to the pod. The relay holds no key. `entry` itself stays on `127.0.0.1`. If the maintainer prefers to keep the rule as it is, the alternative is `cloudflared` only on Kubernetes, at the price of working with a gateway. See [OD-24](/open-decisions/#od-24).
:::

Where `entry` listens on TCP, it is bound to the loopback address, never to `0.0.0.0` or a pod or network address (on Kubernetes see below), so the node opens no inbound port: the security group or host firewall needs no inbound rule for sluis. The port is set in configuration; this site uses `8080` as an example.

An example of the socket directory on one node (illustrative):

```text
/run/sluis/
  entry/prod-public.sock    entry's public API; cloudflared forwards here
  session/prod.sock         group: entry
  exchange/prod.sock        group: entry
  console/prod.sock         group: entry
  signer/prod.sock          group: exchange
  policy/prod.sock          group: exchange, integrations
  key-holder/signer-prod/   one socket per caller; only that caller opens it
  key-holder/ca-prod/
  writer/prod.sock          group: every actor that emits
  node/cred.sock            workload JWT; one answer per uid
```

## The edge connector reaches a Unix socket

On bare-metal, the edge connector is `cloudflared` running as a tunnel. It forwards to `entry`'s Unix socket as a `unix:` origin, and makes outbound connections only. The node accepts no inbound connection from the network, and `entry` has no network namespace of its own to listen in.

An example `config.yaml` (illustrative; the tunnel id, the domain and the path are placeholders):

```yaml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json

ingress:
  - hostname: sluis.example.com
    service: unix:/run/sluis/entry/prod-public.sock
  - service: http_status:404
```

Three details keep this safe:

- **The listener is public by design.** The API in front of the connector is the one that anyone on the internet may call, and every request is authenticated by the actor it is routed to (a sign-in, a client credential, a token). A socket that other local processes can also open therefore adds no capability that the public API does not already offer.
- `entry` holds nothing: no key, no secret, no state. The actor behind the unauthenticated listener cannot sign a token or read a session; it can only route to `session`, `exchange` and `console`, whose sockets neither `cloudflared` nor any other process outside the call graph can open.
- `entry` has no network, so even a compromised `entry` cannot reach out.

## Where `entry`'s listener is, per runtime

The API is the same on every runtime; the listener follows the edge connector. It is a Unix socket on bare metal, and a loopback TCP port only where the connector needs one: inside the Lambda sandbox for the Web Adapter, and on Kubernetes as proposed in ADR 0046. The tested installations are the worked examples.

| Runtime and installation | Listener | Who connects |
| ------------------------ | -------- | ------------ |
| bare-metal: local | A Unix socket on a shared volume; `entry`'s container has no network | An edge-connector container, or a development proxy that publishes a port to the workstation; which one is ADR 0055, not yet decided |
| bare-metal: boards | A Unix socket | `cloudflared` on the same board, a tunnel to the installation's public hostname |
| bare-metal on a virtual machine (a possible installation, not a tested one) | A Unix socket | `cloudflared` on the same instance |
| serverless: lambda | `127.0.0.1:<port>` inside the function's sandbox | The Lambda Web Adapter, pinned at v1.1.0 and vendored into sluis's own layer. The edge in front of the function is API Gateway |
| kubernetes: EKS (proposed) | `entry` on `127.0.0.1:<port>`, bridged in the pod, as below | An edge relay in the same pod (proposed default), or `cloudflared` |

On serverless the loopback port is inside a micro-VM that holds only sluis's own processes. It is not reachable from the network.

:::note[Proposed]
On Kubernetes the loopback port is not reachable from outside the pod, and a gateway reaches a pod by its address. The proposal keeps the listener on `127.0.0.1` and bridges it from inside the pod, in one of two modes: a key-less edge relay on the pod address that forwards to `entry`, which is the default, or a `cloudflared` sidecar that connects to the loopback port and accepts no inbound connection. The kubelet probes `entry` with an `exec` probe, which runs inside the container's network namespace. Binding the pod address in `entry` is not proposed, because it would put an unauthenticated listener on the pod network. Details and options are on [Kubernetes](/runtimes/kubernetes/#edge) and in [OD-24](/open-decisions/#od-24).
:::

## The console's static host

The console's single-page application is served by each runtime's static host: a directory that `entry` serves on bare-metal, the bucket behind the API gateway on serverless, and on Kubernetes its own Deployment behind the same gateway. That container is a **key-less listener**: it serves static files only, holds no key, no secret and no state, and calls nothing.

## The only ports between nodes

On bare-metal installations with N nodes (boards is the tested one) each rqlite cluster's own TCP port carries its Raft traffic between the nodes, one cluster per (boundary, owning actor) and a port each from a port scheme. They are the only TCP ports between nodes, and nothing else crosses a node boundary. The traffic runs over mTLS with a certificate per cluster, and the firewall limits each port to the other nodes' addresses. No actor listens on a TCP port so that a peer could call it: an actor that needs its state on another node talks to its own cluster's node on this one, and rqlite talks Raft.

## Local development and tests

In the local installation the same sockets live on shared volumes, `entry`'s public socket among them, and `entry`'s container has no network, as on any bare-metal installation. How a test harness or a developer's browser reaches that socket, through an edge-connector container or a development proxy that publishes a port to the workstation, is not decided yet (ADR 0055, see [ADR 0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/)). A published port is a development convenience; production installations never publish one.

## Related

- [Processes](/architecture/processes/) shows the actors behind these sockets.
- [Deployment](/architecture/deployment/) describes the firewall rules and sandboxing that keep the listeners narrow.
- [Runtimes](/runtimes/) lists what each runtime opens: [bare-metal](/runtimes/bare-metal/), [serverless](/runtimes/serverless/) and [Kubernetes](/runtimes/kubernetes/). The [tested installations](/runtimes/installations/) are local, boards, lambda and kubernetes.
