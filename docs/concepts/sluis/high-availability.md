# Why is more than one replica safe?

Replicas share one State, so a login, a refresh or a key rotation does not depend on which pod answers. The procedure is
[run more than one replica](../../guides/sluis/operate/high-availability.md). The key layout is in [keys](../../reference/sluis/keys.md).

## What do replicas share?

Everything a login touches sits behind one interface, `issuer.State`. It is a key/value store with TTLs, plus small
unordered sets for listing a client's or an identity's sessions. The State port chooses the implementation:

| `ports.adapter` | Shared | Replicas |
|---|---|---|
| `dynamodb` | one table for every replica and process | the State a replica set uses on AWS, required by the chart above one controller replica |
| `legacy` with `valkey.address` | sessions and the key ring in Valkey | the chart refuses a controller above one replica |
| `memory`, or `legacy` with no Valkey | nothing, process-local | a browser returning to another pod finds nothing |

Every record carries its own TTL, so nothing is swept. Without a shared State a redirect can reach a pod that never
opened the request. Nothing is corrupted. You see intermittent failures that worsen with the replica count.

## What happens when the State is unreachable?

Liveness follows nothing outside the process, so an outage never restarts every replica at once. Readiness follows the
State, so a replica that cannot reach it leaves rotation at the gateway.

The issuer refuses new sign-ins, refreshes and code exchanges with the store's error. It has no in-memory fallback.
Issued access tokens keep working, because they are JWTs verified offline against the JWKS. `/userinfo` fails closed.

`KeyRing.Observe` never returns a State error. A replica keeps signing with its active key but cannot learn of a key rotated elsewhere.

The full behaviour is in [failure semantics](failure-semantics.md).

## Signing keys across replicas

Every replica mounts the same key and polls it every `config.signingKey.pollInterval` (default `30s`). The schedule in
`internal/issuer/keyring.go` is shared through the State:

A new key enters the JWKS at once and signs after `activationDelay` (default `15m`). A superseded key stays published
for `overlap`: the token lifetime plus 5 minutes of skew. The replica that records a key id first sets its `ActivateAt`.
Every other replica reads it back. `KeyRing.Active` falls back to the newest key read from disk and logs an anomaly.

Each algorithm in `signingKey.additional` has its own ring. Two keys for one algorithm are refused by the chart and by `NewKeyRings`.

Put no caching header in front of `/keys`. The issuer sets none on `/keys` or discovery, so a go-oidc verifier refetches
on an unknown `kid`. A proxy that adds a hint reopens the stale-JWKS window that `activationDelay` and `overlap` close.

The client-metadata-document cache is per replica, in process memory for 10 minutes. Replicas can see old and new values
for that long. Each fetch re-checks that the document's `client_id` matches its URL.

## The controllers in the one process

The GitHub and Slack controllers run inside `sluis serve`, so the Deployment rolls with the default `RollingUpdate`. An
old pod leaves when the new one is Ready: policy loaded, stores open, audit catalogue accepted (`/readyz` on
`probes.address`, default `:7070`). Pods that crash at start leave the running pods alone.

A controller runs in every replica, so each target is ticked under a lease from the State port. A lease keeps another
pod off only when the State is shared.

| `ports.adapter` | Leases | `replicaCount` above 1 |
|---|---|---|
| `dynamodb` | in the shared State, one holder per target | safe |
| `legacy` (the default), `memory` | in each pod's memory | refused at render, naming the controller |

`rails.Leases.Do` takes `lease.<kind>:<target>` with `State.Create`. It renews by compare-and-swap every third of its
two-minute lifetime and cancels the tick's context when it loses the lease.

The console's Refresh notifies the target on the Trigger port, and each replica also polls for new requests every 30
seconds. One replica takes the lease and the rest log "leased to another runner".

The installation-token cache, the profile-miss cache, the held-row ledger and the last-report memory are per replica. A
held row can reach the audit trail once per replica. A change to GitHub or Slack is never duplicated.

A target is ticked about twice as often. The counter `access_roster.leases.contended` counts sweeps that found a target
taken, which is normal. The alert `AccessRosterLeaseLost` should stay quiet. Both are legacy identifiers, renamed in v1.75–v1.76.

A rollout or node loss of the only replica pauses reconciling until the new pod starts. Every pass recomputes from the
console and the target system, so nothing is missed. A console outage never empties a channel or a team, because
removals need the directory to vouch.

## Do upgrades stay safe?

The chart sets no `strategy`, so a rollout is a rolling update. A new pod reads the same key and computes the same
schedule from the same State. It never mints a `kid` the pods beside it cannot verify. On a process-local store a
rollout loses what that replica held.

## Decided in

- [ADR 0027](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md): the State port
- [ADR 0029](../../decisions/0029-ticks-per-target-under-a-lease.md): ticks per target under a lease
- [ADR 0037](../../decisions/0037-one-process-everywhere.md): one process everywhere
