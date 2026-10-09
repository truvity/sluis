# Why more than one replica is safe

What has to be shared for `sluis serve` to run at more than one replica, what each piece does when the shared State is
unreachable, and why a rollout and a key rotation stay safe. The procedure is
[run more than one replica](../../guides/sluis/operate/high-availability.md); the key layout is in [keys](../../reference/sluis/keys.md).

## What is shared, and through what

Everything a login touches lives behind one interface (`issuer.State`, `internal/issuer/state.go`): a key/value store
with TTLs, plus small unordered sets for listing "every session for this identity" or "every session for this client"
without scanning keys. The issuer does not know which implementation it was given. The service builds it from the State
port (`internal/issuerapp/app.go`, `openState`):

- **`dynamodb`** (`ports.adapter: dynamodb`): one table, shared by every replica and process. This is the State a replica
  set uses on AWS and the one the chart requires when a controller runs at more than one replica.
- **`legacy` with `valkey.address`** (deprecated in v1.74.0, removed in v1.75; [migrate](../../guides/sluis/migrate/migrate-state.md) with `sluis migrate`): the Kubernetes objects plus a Valkey that holds sessions and the key ring. It is the
  transitional store of an installation not yet moved ([migrate the State](../../guides/sluis/migrate/migrate-state.md)); it shares the
  issuer's state, and the chart still refuses a controller at more than one replica on it.
- **`memory`, or `legacy` with no Valkey**: process-local. The service logs `keeping logins in progress in memory:
  correct for one replica, and at more than one a browser that comes back to a different pod finds nothing`.

The records, their lifetimes and why each exists are the key table in [keys](../../reference/sluis/keys.md): a pending authorize
request, an authorization code, a session and its refresh token, the mark of a spent refresh token (its grace and its reuse detection),
the browser-wide SSO session, a minted token's record, an identity's last-known directory groups, and the signing-key
schedule. Nothing is swept: every record carries its own TTL.

With a shared State, a browser that starts `/authorize` on one pod, comes back from the directory at a second, and
redeems the code at a third works as if there were one process. So does a CLI polling a device code.

Without it, each of those depends only on which pod a request lands on: a redirect back may reach a pod that never opened
the request, a refresh may reach a pod that never heard of the session, and each replica decides for itself when it first
saw a signing key. None of this corrupts anything. It reads as an intermittent failure that gets worse with the replica
count, which is why `issuer.State`'s own comment calls it "a coin toss that looks like an intermittent failure".

## When the shared State is unreachable

Readiness and liveness are deliberately different (`internal/health`). **Liveness follows nothing outside the process**,
so an outage never restarts every replica at once. **Readiness follows the State** (`health.Follow("the session store",
...)`), so a replica that cannot reach it leaves rotation at the gateway: fast refusals instead of a hang, and nothing
restarted. The split was written for a store that moved address while two services kept dialling the old one and stayed
"ready" for half an hour.

- **The issuer refuses; it does not degrade.** Every session and storage operation that touches the store returns the
  store's error (`internal/issuer/session.go`, `storage.go`): new sign-ins, refreshes and code exchanges fail. There is no
  in-memory fallback once a shared State was configured.
- **Access tokens already issued keep working.** They are JWTs verified offline against the published JWKS. The exception
  is `/userinfo`, which looks up the token's own record to catch a token whose session was revoked; it fails closed, so it
  stops answering while the State is down. This is [failure semantics](failure-semantics.md): no new sign-ins anywhere,
  existing sessions and tokens live to expiry.
- **The key ring degrades on purpose.** `KeyRing.Observe` never returns a state error: a replica that already reads the
  active key from its own file keeps signing with it, logs the failure and retries the write on every poll. What it cannot
  do meanwhile is learn of a key rotated on another replica.
- **Sizing.** Every record is bounded by the count of concurrent sessions and in-flight logins, not by the size of the
  directory, and every one expires on its own. The snapshot cache is the directories' size
  ([scaling and cache](../../guides/sluis/operate/scaling-and-cache.md)).

## Signing keys across replicas

Every replica mounts the **same** key (the Secret `signingKey.existingSecret`, or the one the chart's `Certificate`
produces; on AWS the KMS-wrapped key) and polls it every `config.signingKey.pollInterval` (default `30s`). Rotation is
live: whatever manages the Secret replaces the key, the kubelet projects it to each pod on its own schedule, and every
replica notices on its next poll. What keeps replicas from disagreeing about *when* to use a rotated key is the schedule in
`internal/issuer/keyring.go`, shared through the same State:

- **Publish before sign.** A newly observed key is published in the JWKS at once but not signed with until
  `activationDelay` has passed (default `15m`): the slowest kubelet projecting the Secret (about a minute) plus the longest
  verifier JWKS cache (Envoy's `jwt_authn` default is 10 minutes and does not refetch on an unknown `kid`).
- **Overlap on retirement.** A key that stopped signing stays published for `overlap` past being superseded (the
  deployment's token lifetime plus 5 minutes of skew), so a token minted a moment before rotation verifies for its whole
  life.
- **First-writer-wins.** Whichever replica records a key id first in the shared State decides its `ActivateAt`, once
  (`KeyRing.record`); every other replica, this one after a restart included, reads that value back. That makes "every
  replica starts signing with a key at the same moment" true and not an approximation.
- **A replica never signs with a key it cannot read.** `KeyRing.Active` falls back to the newest key this replica read
  from disk, logged as an anomaly.

Each algorithm in `signingKey.additional` has its own ring, secret, mount path and keys in the State, so rotating one never
touches another's schedule. Two keys for the same algorithm at once is refused, by the chart and by `NewKeyRings`.

**`/keys` must reach verifiers with no caching header added in front of it.** The issuer sets none on `/keys` or
discovery, on purpose: a go-oidc verifier (a Kubernetes API server's OIDC authenticator, Kargo) refetches on an unknown
`kid` only once its cache expired, and derives the expiry from `Cache-Control` / `Expires`. With neither it refetches on
the next request, so a rotated `kid` verifies at once. A proxy or CDN that adds its own hint reintroduces the stale-JWKS
window `activationDelay` and `overlap` exist to avoid.

## The client-metadata-document cache is per replica

For clients identified by an HTTPS URL (`policy.ClientDocuments`), the fetched document is cached in process memory for 10
minutes (`documentCacheFor`, `internal/issuer/clientdoc.go`), never in the State. A document that cannot be fetched has no
stale fallback, so the flow fails rather than honouring redirect URIs a client retired. With several replicas, one may see
the old values and another the new for up to 10 minutes. It is not a correctness bug, since every fetch re-checks that the
document's `client_id` matches its URL.

## The controllers in the one process

Since v1.63 the GitHub and Slack controllers run inside the one `sluis serve` process, so the Deployment `<release>`
carries them and rolls with Kubernetes' default `RollingUpdate`: a new pod starts beside the old ones and an old one is
removed only when the new one is Ready, meaning the whole process, each controller included, finished starting (the policy
loaded, the stores open, the audit catalogue accepted: `/readyz`, `probes.address`, default `:7070`). A release whose pods
crash at start leaves the running pods alone.

A controller runs in every replica, so each target (an organisation, a workspace, the GitHub link check) is ticked under
a lease taken from the State port ([0029](../../decisions/0029-ticks-per-target-under-a-lease.md)). **A lease keeps another
pod off only when the State is shared**, which is the whole condition for a second replica:

| `ports.adapter` | Leases | `replicaCount` above 1 |
|---|---|---|
| `dynamodb` | in the shared State: one holder per target across every pod | safe |
| `legacy` (the default), `memory` | in each pod's own memory | **refused at render** with a controller named: every replica would act on every target and make each change twice |

The evidence, in the code:

- **The lease.** `rails.Leases.Do` takes `lease.<kind>:<target>` with `State.Create` (a Create with a lifetime, failing
  with `ErrExists` while held), renews it by compare-and-swap every third of its two-minute lifetime, and cancels the
  tick's context when it is lost, so the tick stops before its next write. GitHub ticks under `github-tick:<org>` and
  `github-links:all`, Slack under `slack-tick:<workspace>`. The sweep, the credential watch and the trigger reach a target
  only through `RunTarget`, which takes the lease.
- **"Run a pass now".** The console's Refresh notifies the target on the Trigger port, and each replica also watches the
  records for a new request (every 30 seconds). The DynamoDB trigger is a watch on `notify.<target>`, so a notification
  reaches every replica; each calls `RunTarget`, one takes the lease and the rest log "leased to another runner". A
  notification is a hint that may be duplicated or lost, and the sweep is its backstop.
- **What is per replica.** The installation-token cache, the profile-miss cache, the held-row ledger and the last-report
  memory. The last two decide which rows are recorded to the audit trail as *new*, so a row can be recorded once per
  replica when ticks alternate: a duplicate audit record of a held row, never a duplicate change to GitHub or Slack.
- **Costs.** Each replica sweeps every interval, so a target is ticked about twice as often (more API calls);
  `access_roster.leases.contended` counts the sweeps that found a target taken, which is normal. `AccessRosterLeaseLost` should
  stay quiet.
- **Leader-only side effects: none** outside a lease. Every pod registers the same audit catalogue at start, which is a
  registration of what is already there.

A rollout or a node loss of the only replica pauses reconciling for the time the new pod needs to start; every pass
recomputes from the console and the target system, so nothing is missed, and a pass that meets a console on another policy
is retried within seconds (5s doubling to a minute, six times). Removals need the directory to vouch, so a console outage
never empties a channel or a team.

## Upgrades

The chart sets no `strategy`, so a rollout is Kubernetes' rolling update. That is safe because of the key ring: a new pod
mid-rollout reads the same key, computes the same schedule from the same State, and either signs with the key already
active or waits out the same `activationDelay`, so it never mints a `kid` the pods beside it cannot verify. Sessions and
pending logins do not depend on which pod is up, provided the State is shared; on a process-local store a rollout loses
what that replica held.
