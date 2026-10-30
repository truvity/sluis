# 0007 — Core is a few actors with one responsibility each: a slim entry, session, console, exchange, signer, policy, ca and ssh-ca, with quota beside them

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (slim `entry`; new actors `session`, `console`, `exchange` and `ssh-ca`; the naming rule; mediators only where a backend cannot tell callers apart).

## Context

In v1 the issuer is one function that serves sign-in, machine token exchange and downstream minting, and it touches 25 resources. v1 first ran everything as one process (v1 ADR 0037, on `master`), then split the modules by credential holder (v1 ADR 0071, on `master`) but kept the signer and the console inside the issuer (v1 ADR 0072, on `master`). A flaw in request parsing in that function still reaches the signing keys. Least privilege cannot be expressed for something that broad.

v2 states the permissions of every process in a manifest ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)). A manifest is only as narrow as the process it describes. So the question is where the lines between processes go, who holds which key, and when two of them may share a unit on a small machine.

The first v2 version of this record still gave `entry` the whole OIDC protocol: sign-in, sessions, token exchange and the console API. That made the one actor that parses unauthenticated internet input also the broadest one. A flaw in its parsing would reach every session, every enrolment and every operator write, even without a key. The same version kept X.509 and SSH in one `ca`, and it let one integration process serve every tenant of a vendor, so one process held several companies' credentials.

Two further facts changed the shape of the shared drivers. Most backends can already tell their callers apart (IAM roles, key policies, database roles, per-user credentials), so a mediator in front of them adds a hop and a single point of failure without adding a check. And rqlite turned out to be light (measured on 2026-10-30: about 7 to 9 MB idle and 16 to 26 MB loaded per node, plus about 30 MB shared per host), so a cluster per owning actor is affordable.

Some processes must also run exactly once. Two audit writers on one log cannot keep one hash chain. Two reconcilers of one external system fight over its state. Each such process needs a rule the tools can check ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).

## Decision

### Actors, split from the first slice

- **An actor is a process with one responsibility.** It declares the ports it requires ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). The word replaces "role" and "process role".
- **The core actors are these. Beside them, `quota` is the engine of the Quota piece ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).**

  | Actor | Responsibility | Holds |
  |---|---|---|
  | `entry` | Parses each request, validates it against closed parameter lists generated from the contracts, and routes it to the actor that owns it. It sits behind the edge connector. | Nothing: no key, no secret, no state and no outbound network. It reaches only the sockets of the actors it routes to (on serverless, it invokes them) |
  | `session` | Session records, the session cookie, and freshness per boundary (step-up) | The cookie-sealing key, behind the keys port |
  | `console` | The operator API, as the browser's backend. It checks the operator's delegation and routes each write to the owning actor | No key |
  | `exchange` | Token exchange (RFC 8693), enrolment, and the verifiers of proofs | Verifier drivers, which hold public material only. The enrolment private key stays in `key-holder` or the cloud key service |
  | `signer` | Holds the signing ring and signs tokens under a signing policy. It is not a pure signing oracle | The token-signing keys |
  | `policy` | The decision point. A declared Cedar policy maps facts to grants ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/)). It has no state and no network. It signs mint grants with its own key | The grant key |
  | `ca` | The X.509 certificate authority | The X.509 CA keys |
  | `ssh-ca` | The SSH certificate authority, for user and host certificates | The SSH CA keys |
  | `quota` | Rate limits, caps and metering, on the state port with compare-and-set. Not a core actor: the engine of the Quota piece | No key. It writes quota balances (issued) and meters (derived) ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)) |

- **`entry` routes and does nothing else.** Each OIDC and API endpoint is parsed by `entry` and served by the actor that owns the step: `session` for sign-in and sessions, `exchange` for token exchange and enrolment, `console` for the operator API, and `signer` for signing. (The mapping of endpoints to actors is inferred from the responsibilities above.)
- **`ssh-ca` is its own actor, separate from `ca`.** An SSH CA is a different authority from an X.509 CA: a different key, a different format and different relying parties. A flaw in one does not sign for the other ([0013](/decisions/0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca/)).
- **`signer`, `policy`, `ca`, `ssh-ca` and `quota` run per boundary.** What a boundary is, and how boundaries are separated, is [0009](/decisions/0009-one-installation-per-boundary/).
- **A copied token is useless.** Tokens are sender-constrained, by DPoP or by mutual TLS, and authorization codes are bound by PKCE. A request that reaches `entry` with a stolen token still fails at the actor that checks the binding. (Which actor checks it is inferred: the actor that serves the endpoint.)
- **`entry` is hardened as the edge it is.** It is written in a memory-safe language, and fuzzed against closed parameter lists. It loads no plugins and runs on a read-only file system. On bare-metal it runs with seccomp and Landlock, `PrivateNetwork=yes` and only `AF_UNIX` sockets, and the edge connector forwards to it over a Unix-socket origin. Which language each actor uses is not decided here: ADR 0060.
- **The central actor is named `entry`** (`sluis-entry`). It was called central in earlier drafts.
- **`signer` and `policy` are split from the first slice.** They are not left for later.
- **Quota is a separate piece that can be combined with sluis.** Its actor, `quota`, is the engine of that piece, and the quota state is the `state` port with compare-and-set ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). When combined with sluis in one installation, it shares sluis's driver instances. Quota meters by default, and a policy rule can enforce it for chosen subjects or audiences.
- **The other actors are named elsewhere.** Integration actors return facts or carry out effects core has authorised. Platform actors are the node agent `sluis-node` and the one mediating driver, `key-holder` (see below).
- **The audit actors are writer, witness, indexer and reader.** There is no notary actor. The writer signs checkpoints with the log key. On a node it reaches the key through its own `key-holder` socket, and only the writer has that socket. With a cloud key service the key policy is the access list, and only the writer's role may use the log key ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).

### Names

- **Every actor instance is named `<actor>@<boundary>`,** for example `ca@prod`. The name is generated from the manifest, and the same name appears in every layer:

  | Layer | Example for `ca@prod` |
  |---|---|
  | systemd unit | `sluis-ca@prod.service` |
  | Unix user | `sluis-ca-prod` |
  | socket | `/run/sluis/ca/prod.sock` |
  | Lambda function and IAM role | `sluis-ca-prod` |
  | Kubernetes container and ServiceAccount | `ca-prod` |
  | KMS alias | `alias/sluis/prod/ca/<purpose>` |
  | state prefix | `prod/ca/…` |
  | lease name, audit field | the same name; `actor=ca@prod` in audit |
  | SPIFFE ID | `spiffe://prod.<td>/sluis/ca`, in the boundary's own trust domain |

- **An integration instance is named `<vendor>[-<capability>]@<tenant>`,** and integrations are split by credential, so each process holds one vendor tenant's credential only. Examples:
  - `google-signin@<company>` and `google-directory@<company>`: sign-in and the directory reader are two credentials, so they are two processes;
  - `github@<org>`, `slack@<workspace>`, `cloudflare@<company>` and `aws-minter@<org>`;
  - `oidc@<name>` for the generic OpenID Connect upstream.

### The console

- **The console API is the `console` actor, the browser's backend.** It moved out of `entry`. The session is an HttpOnly cookie that `session` seals. There are no tokens in the browser.
- **Operator-set writes are done by the owning actor.** `console` checks the operator's delegation, then routes the write to the actor that owns the record kind, and that actor validates it with its own logic. The manifest declares which kinds accept operator-set writes, and the state backend's own per-caller check refuses any writer but the owner ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). `console` therefore gains no write access to other actors' records. Every operator change is authorised for a named person and audited under that name.
- **The console's single-page application is a separate artifact.** It is deployable on its own and served same-origin by each runtime's static host: a directory on bare-metal, the bucket behind the API gateway on serverless, and its own Deployment on Kubernetes ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).

### One credential holder per unit

- **Each actor instance is its own OS user and systemd unit on bare-metal.** It is its own function on serverless, and its own container on Kubernetes.
- **A compromise of one actor gives the attacker that actor's credentials and nothing else.** An actor that can use an authority key does not parse unauthenticated internet input. Of the core actors, `signer`, `policy`, `ca` and `ssh-ca` use an authority key, each a different one. `session` uses its cookie-sealing key through the keys port. `entry`, which faces the edge, holds nothing.
- **The processes follow the owner of a credential, not the port.** A process per owner, never a process per port.

### Mediators only where a backend cannot tell callers apart

- **A backend that can authorise each caller itself is used with per-actor credentials and no mediator.** sluis brokers credentials instead of carrying data through a mediator:
  - `sluis-node` issues a token per uid, and the cloud drivers exchange it for that actor's cloud role;
  - `ca` issues database client certificates, one identity per actor;
  - sluis issues NATS credentials per actor ([0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/) names the keys).
- **State on rqlite has one cluster per boundary and owning actor, and no `state-owner`.** The owning actor holds the `execute` user. Each declared reader holds a `query`-only user. The clusters run node-to-node and client mutual TLS, and each runs as its own sandboxed unit that may write only its data directory. The state port itself is [0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/).
- **`key-holder` is the one remaining mediator,** because a hardware key on a node cannot tell its callers apart. It is one process with one Unix socket per caller, and three checks stand in front of a signature:
  1. **The socket.** Each caller's socket sits in a directory that only that caller can open (systemd socket units with `SocketUser=` and `FileDescriptorName=`). In a container, only that caller's socket is mounted.
  2. **The peer uid,** checked on every connection as a second factor.
  3. **Content-aware signing per purpose.** The `ca` purpose signs only a parseable TBSCertificate within its constraints. The `ssh-ca` purpose signs only an OpenSSH certificate body. A token purpose signs only a JWS signing input.

  `key-holder` has no network and runs under seccomp. It keeps the card's PIN in locked memory, unlocked by a peer node or by `sluis node unlock`, and it rate-limits and audits every signature. More than one card per node is a later option. The ceremony tool uses the OpenPGP card directly, with no mediator.
- **With a cloud key service there is no `key-holder`.** The key policy, with an encryption-context condition where it applies, is the access list.

### The fusion rule

- **Actors are not the same as units.** An installation may run several actors in one unit, to fit a small machine, only if both of these hold:
  1. **The manifest of the unit is the union of the manifests of its actors.** Fusing never grants something no member actor was allowed.
  2. **No unit holds a key next to an unauthenticated listener.**
- **The rule is tested for every installation.** For example, `signer` cannot be fused into `entry`, because the signing key would sit beside the internet-facing listener. On serverless the same reasoning keeps `signer` a separate function. Small actors that hold no key and have no listener, such as `quota`, can share a unit on a small board.
- **On Kubernetes the unit is the container.** A generated check fails the build when a container that holds a key credential exposes a listener. The pod layout and pod identity are recorded in ADR 0046.

### Actors that run exactly once

- **An actor may declare `instances: exactly 1`.** At any moment there must be one and only one of it. The audit writer of a log, the facts-source reconciler and the indexer declare it. For the writer the rule is exactly one per log, not one per installation ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
- **With more than one node, or elastic nodes, such an actor needs a lease.** Leases live on the state store, with compare-and-set and a fencing token, independent of any Raft leader. A stale holder is refused by its fencing token. Where the target store can fence, it fences itself: a Postgres write checks the token in the same transaction, and audit batches are created only if absent, so a log cannot fork. With one log per node, a node's writer needs no lease. External side effects rely on the lease plus idempotent reconcilers. A database session lock is never used alone, because it has no fencing. On a single node a local lock is enough.
- **On serverless, whether the platform's concurrency limit of 1 can stand in for the lease is still to be verified** for a writer triggered by a queue. Until it is, the lease applies there too. (inferred)

## Consequences

- A flaw in request parsing in `entry` reaches no key, no session record and no state, and `entry` cannot send anything out. To obtain a credential an attacker needs a grant that `policy` signed and a minter that accepts it. To forge a token they need `signer` to agree and the signing policy to allow it. A copied token fails its sender binding.
- The cost is more processes. Each actor instance is its own user, unit, socket and manifest, and each call between actors is a hop over a Unix socket. A request now crosses `entry` and then its owning actor, one hop more than before. Small machines need the fusion rule to fit, and the rule has to be tested.
- The split is made once, in the first slice. v1 deferred the same split and never made it.
- One name per instance, generated for every layer, means a unit, a role, a key alias and an audit record can be matched without a lookup table. A rename is a manifest change, not a hunt.
- Splitting integrations by credential means more processes, one per vendor tenant and capability. A company's sign-in, directory, GitHub organisation or Slack workspace then fails, rotates and leaves on its own.
- Dropping `state-owner` takes a process out of every state call, and each backend's own check becomes the one that refuses a wrong writer. That check needs a test per driver, with the other layers switched off. rqlite costs one small cluster per owning actor and boundary, about 16 to 26 MB per node each under load.
- `key-holder` is in the signing path of every hardware key on a node. Its three checks, its rate limit and its audit need their own tests, and a mistake in its per-purpose parsers refuses signatures rather than signing the wrong thing.
- `sluis-node` becomes the broker of cloud credentials on nodes, so it is part of the trusted base.
- The fusion rule is a tested property of every installation. The generator of the manifests must produce the union for a fused unit, and a check must fail the build when a unit holds a key beside a listener.
- `instances: exactly 1` turns a deployment trap (two writers, two reconcilers) into a requirement the tools check: `sluis config check` is where an installation with several nodes and no lease for such an actor is caught.
- Every actor is a producer on the transport port, because every success is audited. Splitting the core into actors multiplies audit producers, not audit writers.
- Routing operator-set writes to the owning actor keeps `console` without write access to other actors' records, and every owning actor needs its own validation and tests for the writes it accepts. A flaw in the single-page application reaches no token, because the browser holds none.
- On bare-metal `entry` listens on a Unix socket behind the edge connector. The listener rule of [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/) is aligned to that in its own rewrite. (inferred)
- Quota can run next to sluis and use its state and driver instances. Enforcement makes the mint path depend on that state, so its availability then affects minting.
- Docs, labels and file paths that said "role" say "actor". The directory of the actors' executables is named `actors`.

## Roads not taken

### One process everywhere

- Pros: fewer functions, roles and documents to run (v1 ADR 0037, on `master`). One configuration, one failure surface to monitor.
- Cons: a flaw in any part holds every credential the others use. The signing key's wrapping key, the sign-in secrets and each integration's credential sit in one role (v1 ADR 0071, on `master`).
- Why not: v1 ADR 0071 already replaced it for the reason above. v2 starts from the split.

### A broad `entry` that runs the OIDC protocol, sessions, token exchange and the console API

- Pros: one actor fewer per request, and no hop between parsing and the logic. It was the first v2 version of this record.
- Cons: the actor that parses internet input would also hold every session, run every enrolment and carry every operator write. A parsing flaw would reach all of them without needing a key.
- Why not: `entry` parses, validates and routes, and holds nothing. Sessions, the console and token exchange are their own actors.

### The signer inside the entry process, as in the v1 issuer

- Pros: one process fewer. No hop for signing.
- Cons: the signing key sits beside the internet-facing listener. A bug in request parsing reaches the keys, and `entry` could be made to ask for any token.
- Why not: the fusion rule forbids it. `signer` is a separate actor and signs only what its signing policy allows.

### Split the signer and the policy later

- Pros: a smaller first slice.
- Cons: splitting later is what v1 deferred and never did. The code grows around the shared process, and the split becomes a rewrite.
- Why not: `signer` and `policy` are split from the first slice.

### One `ca` for X.509 and SSH

- Pros: one actor fewer, and one place for certificate profiles.
- Cons: two authorities with different keys, formats and relying parties would share one process, so a flaw in one could sign for the other.
- Why not: the SSH CA is a different authority, so it is its own actor, `ssh-ca`.

### The decision point with state or network access

- Pros: policy could read live facts itself and need no facts broker.
- Cons: an actor that can be made to read or exfiltrate anything is a target, and its grants can no longer be reviewed as a file.
- Why not: `policy` has no state and no network. Its output is a grant signed by its own key, and the file it reads is reviewed. (inferred)

### A process per port

- Pros: each driver is easy to reason about alone.
- Cons: a process that serves many owners holds many credentials, which defeats the split.
- Why not: the rule is one credential holder per process, a process per owner and never per port. (the cons are inferred)

### One integration process per vendor, serving every tenant

- Pros: fewer processes, as in v1, where one controller served several workspaces.
- Cons: one process holds every company's credential for that vendor, and a company cannot leave or rotate alone.
- Why not: integrations are split per credential, `<vendor>[-<capability>]@<tenant>`, one process each.

### A `state-owner` mediator in front of every state store

- Pros: one place that checks which actor writes which record kind, whatever the backend.
- Cons: it sits in the data path, so it is one more hop and one more process to keep available, and a bug in it crosses every actor. For a backend that already checks each caller it adds no check.
- Why not: a backend with per-caller authorisation is used with per-actor credentials, and rqlite is light enough for a cluster per owning actor. A mediator stays only where the backend cannot tell callers apart, which leaves `key-holder`.

### `key-holder` with one shared socket and an access list

- Pros: one socket to manage. The access list alone decides.
- Cons: every caller can reach the socket, so a bug in the access list or the peer check is the whole defence, and the key will sign any bytes it is given.
- Why not: one socket per caller, the peer-uid check and content-aware signing are three independent checks.

### Free fusion, with no rule

- Pros: small installations run in a few processes, tuned by hand.
- Cons: a unit can quietly gain a key next to a listener, or gain a permission no member actor was allowed.
- Why not: fusion is allowed only as the union of the manifests and never with a key beside an unauthenticated listener, and the rule is tested.

### Names chosen per layer

- Pros: each platform's own conventions, with no generator.
- Cons: a unit, a role, a key alias and an audit field for the same instance would need a lookup table, and a drift between them is a silent permission error. (inferred)
- Why not: one name per instance, `<actor>@<boundary>`, generated from the manifest for every layer.

### Another name for the entry actor

- Pros: `central`, `coordinator` and `hub` were all considered. `central` is familiar from earlier drafts.
- Cons: they say what the actor is in the middle of, not what it does. `entry` is where requests come in, and it is the one core actor that holds nothing. (inferred)
- Why not: the maintainer chose `entry` (`sluis-entry`).

### Quota as a part of sluis that cannot be separated

- Pros: one piece fewer to document.
- Cons: every installation pays for quota state, and quota cannot run on its own. (inferred)
- Why not: pieces are separate by default and combined by choice ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). Quota is a separate piece that can be combined with sluis and then shares its driver instances.

### A notary actor for audit checkpoints

- Pros: a separate actor holds the log key, so a compromised writer cannot sign by itself.
- Cons: one more process to run and secure on every node. It still has to be told what to sign.
- Why not: `key-holder` already holds keys, and the writer's own socket gives most of the protection without a new actor. With a cloud key service the key policy does the same.

### Several writers or several reconcilers, kept apart by procedure

- Pros: no lease to provide.
- Cons: two audit writers on one log cannot keep one hash chain, and two reconcilers fight over the same external state. A procedure does not stop a failover from starting a second one.
- Why not: `instances: exactly 1` is a declared requirement, and the lease enforces it where there is more than one node. (inferred)

### A Kubernetes Lease for actors that run exactly once

- Pros: the platform's own leader election, familiar to operators.
- Cons: the chart would need a Role for leases, and the lease would not fence a write in the target store.
- Why not: leases live on the state store with a fencing token on every runtime. A Kubernetes Lease driver is in the backlog.
