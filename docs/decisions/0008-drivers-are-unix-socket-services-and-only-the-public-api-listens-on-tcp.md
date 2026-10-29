# 0008 — Drivers are platform-started services on Unix sockets, and only the public API listens on TCP

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). This record covers the bare-metal and serverless runtimes. Kubernetes edge and pod layout: ADR 0046.

## Context

sluis v2 is several processes, and each one reaches the things it needs through a port ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). Two questions follow that the port model does not answer: how an implementation of a port (a driver) is attached to the actors that use it, and which processes may accept a network connection.

A key-holding process, a state store and a hardware key are the most valuable things in an installation. If any of them listens on a TCP port, anything that can route to the port can try to talk to it, and the receiver does not learn who the caller is. If an actor starts its own helper or plugin, a compromised actor can start anything. If plugins load into the actor's own address space, a fault or a compromise in a plugin is a fault or a compromise in the actor, with its keys and its identity.

Some backends can tell callers apart and some cannot. A cloud key service checks a role per caller. A hardware key has one PIN, a software key has one file, and the replicated state store has one login. For these, one process has to own the resource and decide for every caller.

The public API is the exception. It has to be reachable from the internet, through an edge connector that differs by platform: a tunnel client on a machine, a gateway adapter in a serverless function. An earlier idea was to let the tunnel client reach `entry` through a Unix socket (a `unix:` ingress). That was weighed and rejected (below).

## Decision

- **Plugins run out of process, always.** Locally this is a Unix socket with peer credentials. On AWS it is a Lambda invoke, and for a remote plugin it is HTTPS with mutual TLS. There are no Go `.so` plugins and no WebAssembly.
- **A driver is a service that speaks ConnectRPC over a Unix socket.** The wire contract is the same for every driver of a port.
- **Nobody execs a driver. The platform starts it.**
  - On bare-metal, systemd starts it (a container runtime service under local Docker).
  - On serverless, a Lambda extension.
  - On Kubernetes, the expected mechanism is a sidecar container in the pod; ADR 0046 decides.
  - An actor's unit has no right to execute anything but its own binary, so a compromised actor cannot start a shell, a helper or a driver of its own.
- **A driver service declares its instance mode: `per-actor` or `shared`.**
  - A `per-actor` driver runs as the actor's own uid, and its socket is open to that uid only. It is used where the backend can tell callers apart, and the backend enforces access.
  - A `shared` driver runs under its own uid. It is used where the backend has one credential or one device. It authenticates every caller by peer credentials (`SO_PEERCRED`) and checks an access list that is generated from the permission manifest. The two shared drivers are `key-holder` (keys) and `state-owner` (state). Both audit every use.
- **Only the public API listens on TCP.**
  - **`entry`'s public API listens on TCP bound to `127.0.0.1`, on every runtime.** The edge connector connects to that loopback port. On bare-metal it is `cloudflared`, which makes outbound connections only. On serverless it is the Lambda Web Adapter, which connects to the loopback port inside the function's sandbox, where only sluis's own processes run. The API authenticates every request itself.
  - **There is no `unix:` ingress for `entry`.**
  - **Every other actor and every driver listens on Unix sockets only**, under `/run/sluis/<actor>/`. A key-holding actor and a driver never get a TCP listener. `signer` is reachable by Unix socket only.
  - **The Raft peer port over mTLS is the only TCP port between nodes.** It belongs to the state driver. No actor is reachable from another node.
  - Two places where the edge meets `entry` are named, because they are not an ordinary host: the Lambda Web Adapter's loopback port inside the function's sandbox, which is `entry`'s own listener, and the Kubernetes pod port (ADR 0046).
- **A driver declares its requirements, and `sluis config check` enforces them.** The requirements are the ones in [0002](/decisions/0002-actors-declare-ports-placement-is-derived/): a long-running process, a local disk, a node count and a dependency. `sluis config check` validates an installation against them, and a driver that is attached in a way the runtime cannot provide (for example, a hardware-key driver on serverless) fails the check. The runtime kinds are in [0003](/decisions/0003-runtimes-are-runtime-kinds/).

### Kubernetes

Kubernetes edge and pod layout: ADR 0046.

## Consequences

- A compromised actor cannot start a process, load a plugin or open a network port to the world. The only places it can be reached from outside are the one authenticated public API and, between nodes, the Raft port.
- The node opens no inbound port for sluis: the security group or host firewall needs no inbound rule except the Raft port between the nodes of a cluster.
- Drivers can be added, replaced and combined at deploy time without a rebuild: add a unit, a layer or a sidecar. The platform supervises, restarts and orders them. Our code does not.
- Shared drivers become the points where the access list must be right. A wrong list is a hole in the key or state boundary, so the generated access lists and their deny tests are part of the manifest work, and each shared driver needs conformance tests for what it declares.
- A serverless function that has a loopback listener and a gateway adapter needs the adapter pinned and vendored into sluis's own layer, so the listener's only client is a component sluis ships.
- ConnectRPC over Unix sockets costs a hop and a serialisation where a library call would be free. That cost is accepted for the isolation. It also means every driver needs the Protobuf contract of its port.
- A second implementation language is possible for a driver (for example Rust for the cloud drivers), because the contract is on the wire and not in a Go interface.
- Testing a listener rule is mechanical: no actor or driver other than `entry` may bind a TCP address, and the deny tests check it.
- Kubernetes will be decided in ADR 0046. Docs for it stay marked proposed until that record is accepted.
- The listener rule on the site ([Listeners](/architecture/listeners/)) and the driver page ([Ports and drivers](/concepts/drivers/)) describe this record. When they disagree, this record is the reasoning and the page is fixed.

## Roads not taken

### Plugins in the actor's process (Go `.so`)

- Pros: no serialisation cost. One binary.
- Cons: a plugin shares the actor's memory, keys and identity. Go plugins need the identical toolchain and dependency set at build time, and are not portable across platforms. (The Go-specific limits are inferred from how Go plugins work; they are not recorded in the design notes.)
- Why not: it removes the isolation that the rest of the design depends on. A bug or a compromise in a plugin would be a bug or a compromise in the actor.

### Plugins in WebAssembly

- Pros: a sandbox without a separate process. Portable across platforms.
- Cons: a second runtime to ship, secure and keep current. Hardware devices, cloud SDKs and sockets need host bridges. (Inferred reasons; the design notes record only the decision.)
- Why not: an ordinary process with a Unix socket and the operating system's own isolation gives the same boundary with tools that operators and auditors already know.

### The actor starts its driver

- Pros: simple to understand. One unit holds everything.
- Cons: the actor needs the right to execute programs, so a compromised actor can run a shell. Supervision, restarts and ordering become the actor's job.
- Why not: the platform already supervises services. Taking the exec right away from actors is a defence that costs nothing to keep.

### Every driver is per-actor

- Pros: one rule, no access list to maintain.
- Cons: a hardware key, a software key and the state store have one credential, so one process per actor would hold the same credential n times, or could not exist at all.
- Why not: for these backends something has to decide who may do what. A single shared driver that authenticates callers by peer credentials, audits every use and fronts the credential does that in one place.

### `cloudflared` reaches `entry` through a Unix socket (`unix:` ingress)

- Pros: `entry` would have no TCP listener at all. The socket's file permissions would limit who can reach it.
- Cons: the same arrangement does not exist on every runtime. On serverless the Lambda Web Adapter connects to a loopback port, and there is no Unix socket to point it at, so the two platforms would differ. The tunnel client would need access to the socket's group, and `entry` would have one more door to manage. (Inferred reasons; the design notes record only that the idea was replaced by the loopback listener.)
- Why not: a loopback listener works the same way on every runtime. The API is public by design and authenticates every request itself, so a loopback port that other local processes can also open adds no capability. The listener is bound to `127.0.0.1` only, `entry` holds no key, and the host firewall drops everything else.

### Bind `entry` to a network address

- Pros: a gateway or a load balancer can reach it directly. (inferred)
- Cons: it puts an unauthenticated listener on the network. The firewall and the security group become part of the actor's protection. (inferred)
- Why not: with a loopback listener and an outbound-only connector the node opens no inbound port. (For the Kubernetes pod address, see ADR 0046.) (inferred)

### Peer traffic over TCP between actors on different nodes

- Pros: an actor could call a driver or another actor on a neighbouring node. (inferred)
- Cons: every actor becomes a network service with its own authentication, and the number of inbound ports grows with the number of actors. (inferred)
- Why not: an actor asks the driver on its own node. Only the state driver's Raft port crosses a node boundary, over mTLS, and the firewall limits it to the other nodes' addresses. (inferred)
