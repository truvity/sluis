# 0008 — Drivers are platform-started services on Unix sockets, and only the public API listens on TCP

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). This record covers the bare-metal and serverless runtimes. Kubernetes edge and pod layout: ADR 0046.
Rewritten in place on 2026-10-30 (alignment with 0007, 0058 and 0012).
Rewritten in place on 2026-10-30 (the point that was not decided here, how a `per-actor` rqlite driver reaches its cluster, is decided: a narrow network rule per unit; tracked on [issue #910](https://github.com/truvity/sluis/issues/910)).

## Context

sluis v2 is several processes, and each one reaches the things it needs through a port ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). Two questions follow that the port model does not answer: how an implementation of a port (a driver) is attached to the actors that use it, and which processes may accept a network connection.

A key-holding process, a state store and a hardware key are the most valuable things in an installation. If any of them listens on a TCP port, anything that can route to the port can try to talk to it, and the receiver does not learn who the caller is. If an actor starts its own helper or plugin, a compromised actor can start anything. If plugins load into the actor's own address space, a fault or a compromise in a plugin is a fault or a compromise in the actor, with its keys and its identity.

Most backends can tell callers apart. A cloud key service checks a role per caller, DynamoDB checks the leading key per role, PostgreSQL has grants and row-level security, and rqlite has users with `execute` or `query` rights and is light enough to run one cluster per owning actor ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). A hardware key cannot: it has one PIN, so every process that can talk to the card can use every key on it, and a software key is one file. For these, one process has to own the resource and decide for every caller.

The public API is the exception. It has to be reachable from the internet, through an edge connector that differs by platform: a tunnel client on a machine, a gateway adapter in a serverless function. The first version of this record bound `entry` to a loopback TCP port on every runtime and rejected a Unix-socket origin. Since then `entry` has become a slim router that holds nothing and has no outbound network, and on bare metal it runs in a private network namespace with Unix sockets only ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). A loopback port in that namespace is not reachable from the tunnel client, so the bare-metal edge changes. (The reason is inferred from the hardening in 0007.)

## Decision

- **Plugins run out of process, always.** Locally this is a Unix socket with peer credentials. On AWS it is a Lambda invoke, and for a remote plugin it is HTTPS with mutual TLS. There are no Go `.so` plugins and no WebAssembly.
- **A driver is a service that speaks ConnectRPC over a Unix socket.** The wire contract is the same for every driver of a port.
- **Nobody execs a driver. The platform starts it.**
  - On bare-metal, systemd starts it (a container runtime service under local Docker).
  - On serverless, a Lambda extension.
  - On Kubernetes, the expected mechanism is a sidecar container in the pod; ADR 0046 decides, and it is not yet decided.
  - An actor's unit has no right to execute anything but its own binary, so a compromised actor cannot start a shell, a helper or a driver of its own.
- **A driver service declares its instance mode: `per-actor` or `shared`.**
  - **`per-actor` is the default.** A `per-actor` driver runs as the actor's own uid, in the actor's own unit or as its sidecar, and its socket is open to that uid only. It holds only its own actor's credential: a cloud role, a database client certificate, an rqlite user or NATS credentials. The backend checks the caller ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). sluis brokers these credentials instead of carrying data through a mediator: `sluis-node` issues a token per uid ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)), the cloud drivers exchange it, `ca` issues database client certificates, and sluis issues NATS credentials.
  - **A `shared` driver is only for a backend that cannot tell callers apart.** It runs under its own uid. The one shared driver is `key-holder`, for keys on a node's hardware key or key file. There is no `state-owner`: every state backend checks its callers itself ([0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/)).
  - **`key-holder` serves one Unix socket per caller.** Each socket sits in a directory that only its caller can open, through systemd socket units with `SocketUser=` and `FileDescriptorName=`. In a container, only that caller's socket is mounted. `key-holder` also checks the peer uid (`SO_PEERCRED`) of every connection as a second factor, signs only content that parses for the purpose ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)), checks an access list generated from the permission manifest, and audits every use.
- **Only the public API listens on TCP, and on bare metal not even that.**
  - **On bare metal, `entry` listens on a Unix socket only.** Its unit runs with `PrivateNetwork=yes` and `RestrictAddressFamilies=AF_UNIX`, so it has no TCP listener and no outbound network ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). The edge connector, `cloudflared`, makes outbound connections only and forwards to `entry` over a Unix-socket origin, at `/run/sluis/entry/<boundary>.sock` by the naming rule of 0007.
  - **On serverless, `entry` listens on TCP bound to `127.0.0.1` inside the function's sandbox.** The edge connector is the Lambda Web Adapter, which connects to that loopback port, and only sluis's own processes run in the sandbox. Only `entry` uses the adapter.
  - **On Kubernetes, the edge and the pod port are ADR 0046**, which is not yet decided.
  - **The API authenticates every request itself**, on every runtime.
  - **Every other actor and every driver listens on Unix sockets only**, under `/run/sluis/<actor>/`, one socket per instance (`/run/sluis/<actor>/<boundary>.sock`). A key-holding actor and a driver never get a TCP listener. `signer` is reachable by Unix socket only.
  - **A `per-actor` rqlite driver reaches its cluster through a narrow network rule, not a private network namespace.** rqlite's client API is TCP, so the unit that runs the driver cannot use `PrivateNetwork=yes`. It stays in the host's network namespace with `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, `IPAddressDeny=any` and `IPAddressAllow=` for its own cluster's addresses only, and it holds a client certificate for that one cluster. Every other actor that needs no network keeps `PrivateNetwork=yes` and `AF_UNIX` only. If rqlite adds a Unix-socket listener, the driver moves to it and the unit goes back to `PrivateNetwork=yes`.
  - **The state store's cluster ports are the only other TCP ports.** On rqlite there is one cluster per boundary and owning actor, each run from the systemd template `sluis-rqlite@<boundary>-<actor>` with a port scheme for its Raft and HTTP ports. Node-to-node and client traffic use mutual TLS with a certificate per cluster, so a node of one cluster cannot join another ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). The Raft ports are the only TCP ports between nodes, and the firewall limits them to the other nodes' addresses. No actor is reachable from another node.
- **A driver declares its requirements, and `sluis config check` enforces them.** The requirements are the ones in [0002](/decisions/0002-actors-declare-ports-placement-is-derived/): a long-running process, a local disk, a node count and a dependency. `sluis config check` validates an installation against them, and a driver that is attached in a way the runtime cannot provide (for example, a hardware-key driver on serverless) fails the check. The runtime kinds are in [0003](/decisions/0003-runtimes-are-runtime-kinds/).

### Kubernetes

Kubernetes edge and pod layout: ADR 0046.

### How a `per-actor` rqlite driver reaches its cluster

The two rules above met here, and the maintainer decided on 2026-10-30. A `per-actor` driver runs as the actor's uid and holds the actor's rqlite user, and an actor that needs no network runs with `PrivateNetwork=yes` and `AF_UNIX` only. rqlite's client API (its HTTP port, beside the Raft port of each `sluis-rqlite@<boundary>-<actor>` cluster) is TCP, and a unit in a private network namespace cannot reach the host's loopback port or a peer's address.

- **The unit gets a narrow rule instead:** `IPAddressDeny=any`, `IPAddressAllow=` for its own cluster's addresses and nothing else, and `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`. The generator writes the addresses from the cluster's port scheme ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). (that the generator writes them is inferred)
- **The driver holds a client certificate per cluster**, so it can reach only the cluster it is allowed, even if the address rule were wider.
- **A Unix socket is the later form.** As of v10.6.6 (2026-10-28) `rqlited` serves its HTTP API on TCP only. An optional Unix-socket listener is proposed upstream in an unmerged pull request, rqlite/rqlite#2979. If rqlite adds one, the driver moves to it and its unit returns to `PrivateNetwork=yes`.

## Consequences

- A compromised actor cannot start a process, load a plugin or open a network port to the world. The only places it can be reached from outside are the one authenticated public API and, between nodes, the state clusters' Raft ports.
- The node opens no inbound port for sluis: the security group or host firewall needs no inbound rule except the Raft ports between the nodes of each state cluster.
- On bare metal `entry` has no network stack to attack from the host side: a local process that is not the edge connector cannot reach it over TCP, and `entry` cannot send anything out. The edge connector needs access to `entry`'s socket, which is one more permission in the generated units. (inferred)
- The bare-metal and serverless edges now differ: a Unix-socket origin on one, a loopback port on the other. `entry` serves the same API on both, and its tests cover both listeners. (inferred)
- Drivers can be added, replaced and combined at deploy time without a rebuild: add a unit, a layer or a sidecar. The platform supervises, restarts and orders them. Our code does not.
- `key-holder` is the one point where the driver's own code is the boundary. Its socket layout, its peer-uid check, its content checks and its generated access list need deny tests. Every `per-actor` driver relies on the backend's check instead, and that check needs a test per driver with the other layers switched off ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).
- A serverless function that has a loopback listener and a gateway adapter needs the adapter pinned and vendored into sluis's own layer, so the listener's only client is a component sluis ships.
- ConnectRPC over Unix sockets costs a hop and a serialisation where a library call would be free. That cost is accepted for the isolation. It also means every driver needs the Protobuf contract of its port.
- A second implementation language is possible for a driver (for example Rust for `key-holder` and the cloud drivers), because the contract is on the wire and not in a Go interface.
- A unit that runs a `per-actor` rqlite driver has a network stack, limited by a per-unit rule rather than by a namespace. A wrong address in the rule is caught by the per-cluster client certificate, and the deny tests check that the unit cannot reach any other address. (the deny test is inferred)
- Testing a listener rule is mechanical: no actor or driver may bind a TCP address, except `entry` on serverless (on loopback) and the state clusters' ports. On bare metal `entry` binds none. The deny tests check it.
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
- Cons: a hardware key has one PIN and a software key is one file, so one process per actor would hold the same credential n times, or could not exist at all.
- Why not: `per-actor` is the default, and every backend that can tell callers apart uses it. For the one that cannot, something has to decide who may do what, and `key-holder` does that in one place.

### A shared `state-owner` in front of the state store

- Pros: works with rqlite as it is, which authorises per endpoint only. One policy point for every state call. Fewer clusters to run. It was the first version of this record.
- Cons: the isolation between actors is the mediator's code, not something the backend enforces. It is in the path of every state call and is the whole state class as blast radius.
- Why not: rqlite is light enough to run a cluster per owning actor, with an `execute` user for the owner and `query` users for readers, so the backend refuses a wrong writer itself ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).

### `entry` on a loopback TCP port on every runtime

- Pros: one arrangement on every runtime, since the Lambda Web Adapter connects to a port. It was the first version of this record.
- Cons: on bare metal `entry` would have to share the host's network namespace, so it could not run with `PrivateNetwork=yes` and Unix sockets only, and any local process could open the port.
- Why not: the slim `entry` has no network at all on bare metal ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). A Unix-socket origin keeps it off the network stack, and the socket's permissions limit who can reach it. Serverless keeps the loopback port, because the adapter needs one and the sandbox runs only sluis's own processes.

### Bind `entry` to a network address

- Pros: a gateway or a load balancer can reach it directly. (inferred)
- Cons: it puts an unauthenticated listener on the network. The firewall and the security group become part of the actor's protection. (inferred)
- Why not: with a Unix-socket origin on bare metal, a loopback port in the sandbox on serverless and an outbound-only connector, the node opens no inbound port. (For the Kubernetes pod address, see ADR 0046.) (inferred)

### `PrivateNetwork=yes` with rqlite on a Unix socket now

- Pros: the actor keeps no network stack at all, and the socket's permissions admit only the owner and its readers.
- Cons: rqlite does not support it today, so sluis would carry a patched build.
- Why not: the narrow rule works with rqlite as it is. The driver moves to a Unix socket when rqlite has one.

### A per-actor sidecar unit that holds the rqlite credential

- Pros: the actor keeps `PrivateNetwork=yes`, and the sidecar alone talks TCP with mutual TLS.
- Cons: one more unit per actor, and a process that is neither a mediator for several callers nor inside the actor's sandbox.
- Why not: the narrow rule with a certificate per cluster gives the same reach with no extra unit.

### Peer traffic over TCP between actors on different nodes

- Pros: an actor could call a driver or another actor on a neighbouring node. (inferred)
- Cons: every actor becomes a network service with its own authentication, and the number of inbound ports grows with the number of actors. (inferred)
- Why not: an actor asks the driver on its own node. Only the state clusters' Raft ports cross a node boundary, over mTLS, and the firewall limits them to the other nodes' addresses. (inferred)
