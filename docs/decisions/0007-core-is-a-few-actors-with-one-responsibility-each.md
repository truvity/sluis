# 0007 — Core is a few actors with one responsibility each: entry, signer, policy and ca, with quota beside them

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass).

## Context

In v1 the issuer is one function that serves sign-in, machine token exchange and downstream minting, and it touches 25 resources. v1 first ran everything as one process (v1 ADR 0037, on `master`), then split the modules by credential holder (v1 ADR 0071, on `master`) but kept the signer and the console inside the issuer (v1 ADR 0072, on `master`). A flaw in request parsing in that function still reaches the signing keys. Least privilege cannot be expressed for something that broad.

v2 states the permissions of every process in a manifest ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)). A manifest is only as narrow as the process it describes. So the question is where the lines between processes go, who holds which key, and when two of them may share a unit on a small machine.

Some processes must also run exactly once. Two audit writers on one log cannot keep one hash chain. Two reconcilers of one external system fight over its state. Each such process needs a rule the tools can check ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).

## Decision

### Actors, split from the first slice

- **An actor is a process with one responsibility.** It declares the ports it requires ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). The word replaces "role" and "process role".
- **The core has four actors. Beside them, `quota` is the engine of the Quota piece ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).**

  | Actor | Responsibility | Holds |
  |---|---|---|
  | `entry` | The OIDC protocol: sign-in, sessions, token exchange, and the console API as the browser's backend. It sits behind the edge connector. | No key |
  | `signer` | Holds the signing ring and signs tokens under a signing policy. It is not a pure signing oracle. | The token-signing keys |
  | `policy` | The decision point. A declared Cedar policy maps facts to grants ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/)). It has no state and no network. It signs mint grants with its own key. | The grant key |
  | `ca` | X.509 and the SSH certificate authority. | The CA keys |
  | `quota` | Rate limits, caps and metering, on the state port with compare-and-set. Not a core actor: the engine of the Quota piece. | No key. It writes quota balances (issued) and meters (derived) ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)) |

- **The central actor is named `entry`** (`sluis-entry`). It was called central in earlier drafts.
- **`signer` and `policy` are split from the first slice.** They are not left for later.
- **Quota is a separate piece that can be combined with sluis.** Its actor, `quota`, is the engine of that piece, and the quota state is the `state` port with compare-and-set ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). When combined with sluis in one installation, it shares sluis's driver instances. Quota meters by default, and a policy rule can enforce it for chosen subjects or audiences.
- **The other actors are named elsewhere.** Integration actors return facts or carry out effects core has authorised. Platform actors are the node agent and the two shared drivers `key-holder` and `state-owner` ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).
- **The audit actors are writer, witness, indexer and reader.** There is no notary actor. The writer asks `key-holder` to sign checkpoints, and the access list of `key-holder` lets only the writer use the log key ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).

### The console

- **The console API lives in `entry`, which is the browser's backend.** The session is an HttpOnly cookie. There are no tokens in the browser.
- **Operator-set writes are done by the owning actor.** The console API routes a write to the actor that owns the record kind, and that actor validates it with its own logic. The manifest declares which kinds accept operator-set writes, and `state-owner` refuses any other writer ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). `entry` therefore gains no write access to other actors' records.
- **The console's single-page application is a separate artifact.** It is deployable on its own and served same-origin by each runtime's static host: a directory that `entry` serves on bare-metal, the bucket behind the API gateway on serverless, and a static container behind the ingress on Kubernetes ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).

### One credential holder per unit

- **Each actor is its own OS user and systemd unit on bare-metal.** It is its own function on serverless. On Kubernetes it is its own pod or container, as ADR 0046 will decide.
- **A compromise of one actor gives the attacker that actor's credentials and nothing else.** An actor that can use an authority key does not parse unauthenticated internet input. Of the core actors, `signer`, `policy` and `ca` use an authority key, each a different one, and `entry`, which faces the edge, holds none.
- **The processes follow the owner of a credential, not the port.** A process per owner, never a process per port.

### The fusion rule

- **Actors are not the same as units.** An installation may run several actors in one unit, to fit a small machine, only if both of these hold:
  1. **The manifest of the unit is the union of the manifests of its actors.** Fusing never grants something no member actor was allowed.
  2. **No unit holds a key next to an unauthenticated listener.**
- **The rule is tested for every installation.** For example, `signer` cannot be fused into `entry`, because the signing key would sit beside the internet-facing listener. On serverless the same reasoning keeps `signer` a separate function. Small actors that hold no key and have no listener, such as `quota`, can share a unit on a small board.

### Actors that run exactly once

- **An actor may declare `instances: exactly 1`.** At any moment there must be one and only one of it. The audit writer of a log, the facts-source reconciler and the indexer declare it. For the writer the rule is exactly one per log, not one per installation ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
- **With more than one node, or elastic nodes, such an actor needs the lease port.** A stale holder is then refused by its fencing token. On a single node a local lock is enough. On serverless the platform's concurrency limit of 1 may stand in for the lease.

### Units on Kubernetes

The Kubernetes meaning of a unit will be decided in ADR 0046.

## Consequences

- A flaw in request parsing in `entry` reaches no key. To obtain a credential an attacker needs a grant that `policy` signed and a minter that accepts it. To forge a token they need `signer` to agree and the signing policy to allow it. Two independent checks replace one function's role.
- The cost is more processes. Each actor is its own user, unit, socket directory and manifest, and each call between actors is a hop over a Unix socket. Small machines need the fusion rule to fit, and the rule has to be tested.
- The split is made once, in the first slice. v1 deferred the same split and never made it.
- The fusion rule is a tested property of every installation. The generator of the manifests must produce the union for a fused unit, and a check must fail the build when a unit holds a key beside a listener.
- `instances: exactly 1` turns a deployment trap (two writers, two reconcilers) into a requirement the tools check: `sluis config check` is where an installation with several nodes and no lease driver for such an actor is caught.
- Every actor is a producer on the transport port, because every success is audited. Splitting the core into actors multiplies audit producers, not audit writers.
- Routing operator-set writes to the owning actor keeps `entry` without write access to other actors' records, and every owning actor needs its own validation and tests for the writes it accepts. A flaw in the single-page application reaches no token, because the browser holds none.
- Quota can run next to sluis and use its state and driver instances. Enforcement makes the mint path depend on that state, so its availability then affects minting.
- Dropping the notary leaves one fewer actor to run. The protection of the log key rests on the access list of `key-holder`, which needs tests ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)).
- Docs, labels and file paths that said "role" say "actor". The directory of the actors' executables is named `actors`.

## Roads not taken

### One process everywhere

- Pros: fewer functions, roles and documents to run (v1 ADR 0037, on `master`). One configuration, one failure surface to monitor.
- Cons: a flaw in any part holds every credential the others use. The signing key's wrapping key, the sign-in secrets and each integration's credential sit in one role (v1 ADR 0071, on `master`).
- Why not: v1 ADR 0071 already replaced it for the reason above. v2 starts from the split.

### The signer inside the entry process, as in the v1 issuer

- Pros: one process fewer. No hop for signing.
- Cons: the signing key sits beside the internet-facing listener. A bug in request parsing reaches the keys, and `entry` could be made to ask for any token.
- Why not: the fusion rule forbids it. `signer` is a separate actor and signs only what its signing policy allows.

### Split the signer and the policy later

- Pros: a smaller first slice.
- Cons: splitting later is what v1 deferred and never did. The code grows around the shared process, and the split becomes a rewrite.
- Why not: `signer` and `policy` are split from the first slice.

### The decision point with state or network access

- Pros: policy could read live facts itself and need no facts broker.
- Cons: an actor that can be made to read or exfiltrate anything is a target, and its grants can no longer be reviewed as a file.
- Why not: `policy` has no state and no network. Its output is a grant signed by its own key, and the file it reads is reviewed. (inferred)

### A process per port

- Pros: each driver is easy to reason about alone.
- Cons: a process that serves many owners holds many credentials, which defeats the split.
- Why not: the rule is one credential holder per process, a process per owner and never per port. (the cons are inferred)

### Free fusion, with no rule

- Pros: small installations run in a few processes, tuned by hand.
- Cons: a unit can quietly gain a key next to a listener, or gain a permission no member actor was allowed.
- Why not: fusion is allowed only as the union of the manifests and never with a key beside an unauthenticated listener, and the rule is tested.

### Another name for the entry actor

- Pros: `central`, `coordinator` and `hub` were all considered. `central` is familiar from earlier drafts.
- Cons: they say what the actor is in the middle of, not what it does. `entry` is where requests come in, and it is the one core actor with no key. (inferred)
- Why not: the maintainer chose `entry` (`sluis-entry`).

### Quota as a part of sluis that cannot be separated

- Pros: one piece fewer to document.
- Cons: every installation pays for quota state, and quota cannot run on its own. (inferred)
- Why not: pieces are separate by default and combined by choice ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). Quota is a separate piece that can be combined with sluis and then shares its driver instances.

### A notary actor for audit checkpoints

- Pros: a separate actor holds the log key, so a compromised writer cannot sign by itself.
- Cons: one more process to run and secure on every node. It still has to be told what to sign.
- Why not: `key-holder` already holds keys and enforces an access list, and letting only the writer use the log key gives most of the protection without a new actor.

### Several writers or several reconcilers, kept apart by procedure

- Pros: no lease port to provide.
- Cons: two audit writers on one log cannot keep one hash chain, and two reconcilers fight over the same external state. A procedure does not stop a failover from starting a second one.
- Why not: `instances: exactly 1` is a declared requirement, and the lease port enforces it where there is more than one node. (inferred)

### Decide the Kubernetes unit in this record

- Pros: the fusion rule would be complete for all three runtimes.
- Cons: Kubernetes comes later in the delivery order, and its pod layout, edge and cloud identity belong together.
- Why not: the Kubernetes meaning of a unit will be decided with them in ADR 0046. The rule here is stated for the units that exist today.
