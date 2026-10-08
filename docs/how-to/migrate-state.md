# Move the State with `sluis migrate`

## Purpose

Copy an installation's State, its secrets and the controllers' reports from one storage to another with
`sluis migrate`, and undo it with the same command.

The decision is [0031](../decisions/0031-a-generic-migration-tool.md): one generic command, and **data and runtime
never move in the same step**. The move order today is **Kubernetes objects (ConfigMaps, Secrets, and Valkey where one
holds the sessions) to DynamoDB**; the intermediate key-value step the decision named was removed with its adapter on
2026-10-04 ([0027](../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md)). Moving a whole installation to AWS, with the
freeze, the switch and the rollback, is [the cutover](cutover.md); this page is the command.

## Preconditions

- A `serve` configuration file for each side, the one the Deployment reads (`ports.*`, and for the legacy storage
  `store`, `release` and `valkey`). The file is validated against the same schema; there is no second configuration.
- The destination names a **working Secrets adapter** (`ssm` or `openbao`): credentials are written to the destination's
  Secrets, never into State, and the start is refused without one. For DynamoDB: `ports.adapter: dynamodb`,
  `ports.dynamodb` (table, region; the workload's own identity is the credential), and `ports.blob` (S3) if the reports
  move. **Remove `valkey.address` from the destination file**: DynamoDB holds the shared state and the start is refused
  with both. Keep `store`, `release` and `inCluster`: the console's cluster-only objects still use them.
- From a workstation: `--kubeconfig`, `--kube-context` and `--namespace` (default: the pod's ServiceAccount) read the
  source namespace, and the source's Valkey is reached through the address its file names, a port-forward from there.
- A destination layout older than v3 for SSM config parameters is moved first with `sluis migrate ssm-layout --to-root
  /sluis/<instance>` (it copies, deletes nothing, and takes `--dry-run`). A destination whose installation document says
  `secrets.layout: v4` receives the secrets in layout v4 directly; v3 as a destination is deprecated
  ([move the secrets to layout v4](migrate-secrets-layout.md)).

## Before you start

Each of these has bitten an installation.

- **Preview before every apply, and read the preview.** `--dry-run` reads both sides and writes nothing; run it against
  the live installation first, with no freeze. Anything it lists under `refused` (a record over 256 KiB, a credential
  over 8 KiB, an empty or invalid key) ends a real run non-zero before a write, and `--overwrite` does not change that.
- **The source must be quiet, and the tool cannot check.** A copy is a snapshot: a session opened while it runs is a copy
  of neither state. A run that writes needs `--i-have-stopped-writers`, a statement you make. In the one process the
  writers are the issuer's sign-ins, the console and both controllers together, so the freeze is the one Deployment at
  0. A GitOps controller that owns the Deployment scales it back, and the chart refuses `replicaCount: 0`
  (`minimum: 1` in `values.schema.json`): freeze with the controller's own pause, for Argo CD
  `argocd.argoproj.io/skip-reconcile: "true"` on the Application, then `kubectl scale --replicas=0`
  ([cutover](cutover.md#before-you-start)).
- **A destination that is not empty can already hold a different value.** The run stops before writing and names the key:
  a Lambda's first smoke start creates `console/session-key`, so a real run after one needs `--overwrite`. Use
  `--overwrite` only when the source's value is the one to keep.
- **Sessions are left behind by default.** The issuer's sessions, refresh tokens, codes in flight and Index sets are not
  copied: people sign in again. `--with-sessions` keeps them, each with the lifetime it has left on the source (the
  DynamoDB to DynamoDB case, say). The key ring's schedule (`issuer:keyring:*`, retired-key tombstones included) is
  always copied so a token issued before the move keeps verifying; `issuer:kms:*` (the fingerprint of a KMS-signed
  installation's state secret) never is.
- **Left out on purpose:** leases (transient), the hub's snapshots (a cache), and the controllers' hand-offs and caches
  (`share.`, `cache.`, `dedupe.`). `--skip` names domains to leave out. A backup to a file (`--backup`) does not exist;
  a backup today is a copy to a second storage, the same command with a second `--to`.

What moves, and how each lifetime is kept:

| Domain | What | Lifetime |
|---|---|---|
| `directory` | workspaces and their credentials | permanent |
| `github` | organisations with the App key, the link App, runner and catalogue Apps, people's links (token pairs and the `Revision` counter exactly as they were), confirmations and requests for a pass | permanent; a confirmation or request keeps its own timestamp |
| `slack` | workspaces with their secrets, catalogue Apps, Slack Connect and console channel records, confirmations and requests for a pass | the same |
| `console` | the console's session-signing key | permanent |
| `issuer` | the signing keys' schedule always; with `--with-sessions` also sessions with their indices, refresh tokens and spent markers, the browser SSO, minted tokens' records, authorization requests and codes | each record is written with the lifetime it has left on the source |
| `blobs` | the controllers' last reports | none. `--blobs auto` (default) copies only when the two sides keep them in different places; `copy` and `skip` force either |

It copies **through the business interfaces, never as bytes**, so a secret read from the source's store is written to the
destination's Secrets (`credentials/<kind>/<id>/<ref>`, storage layout v2) and a record lands in the layout its adapter
keeps. It prints no value: the report names keys, and an issuer key by kind and a short hash
(`issuer:session:#3fa9c1d2`).

## Steps

### 1. Preview

**Run**

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> --dry-run
```

**Expect** a JSON report on stdout and a summary on stderr, with items and bytes per concern (state, secrets, blobs) and
a `REFUSED` line per item the destination would refuse. Exit 0 only when `ok` is true.
**Verify** `conflicts` is empty (the destination should be empty), `unreadable` is empty, `refused` is empty. Fix what it
names (or, for a record that is truly dead, delete it) and run again until it exits 0.
**Rollback**: none, because a dry run writes nothing, not even the objects a legacy destination would need created.

### 2. Freeze the writers

**Run** scale the Deployment to 0 (with the pause above under a GitOps controller) and wait until the pods are gone.
Sign-in is down from here until the new installation is up; a relying party holding a valid access token keeps working
until it expires.
**Expect** no pod of the release.
**Verify** `kubectl -n <namespace> get pods -l app.kubernetes.io/instance=<release>` is empty.
**Rollback**: scale back up and remove the pause.

### 3. Run

**Run** from the workstation:

```sh
sluis migrate --from old.yaml --to new.yaml \
  --kubeconfig ~/.kube/config --kube-context <context> --namespace <namespace> \
  --i-have-stopped-writers
```

or, in the cluster, as a one-off Job from the chart's image with the chart's ServiceAccount (it needs the Role the
Deployment has) and both files mounted. The identity that reaches DynamoDB and the secrets store must be the same one:

```yaml
apiVersion: batch/v1
kind: Job
metadata: {name: sluis-migrate}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: <the chart's ServiceAccount>
      containers:
        - name: migrate
          image: <the chart's image, the same tag>
          args: [migrate, --from=/etc/migrate/old.yaml, --to=/etc/migrate/new.yaml,
                 --i-have-stopped-writers, --report-blob=reports/migrate-<date>.json]
          # env, volumeMounts and volumes as the Deployment's, plus the two files (a ConfigMap).
```

`kubectl logs job/sluis-migrate` is the report on stdout and the log on stderr. `--report-blob <name>` also writes the
report to a Blob name on the destination.
**Expect** it to plan first (every item new, equal, or different), copy, then read both sides again.
**Verify** read the report in step 4.
**Rollback**: nothing on the source was written; see [Rollback](#rollback).

The run is idempotent: it creates what is absent, leaves what is equal alone, and after a failure the same command
completes the copy.

### 4. Read the report

**Run** read the JSON.
**Expect**

```json
{ "ok": true, "dryRun": false, "overwrite": false,
  "steps": [{"domain": "github", "kind": "organisations", "source": 3, "new": 3, "present": 0, "copied": 3, "verified": 3}],
  "totals": {"source": 415, "copied": 415, "verified": 415},
  "conflicts": [], "unreadable": [], "mismatches": [], "notes": [] }
```

`source` is the readable items; `new`, `present`, `conflicts` are the plan; `copied` is what was written; `verified` and
`mismatched` are the second read. `unreadable` lists what the source's store cannot present (a record that does not
decode): reported, not copied, everything else is still copied and verified, then the run ends non-zero.
**Verify** `ok: true`, `mismatches`, `conflicts` and `unreadable` empty, `totals.verified` equal to `totals.source`. A
source that changed during the copy shows as a mismatch, so a missed writer is found, not hidden.
**Rollback**: see [Rollback](#rollback).

### 5. Switch and check

**Run** put the new `serve` file into the chart's values (or follow [the cutover](cutover.md)) and bring the replicas
back.
**Expect** the log says `keeping state in DynamoDB` and the domain records in the state port, credentials in Secrets.
**Verify** the console shows the same workspaces, organisations and Slack workspaces; with `--with-sessions`, a person
signed in before the window is still signed in and a refresh token from before it refreshes; the controllers tick
(`access_roster.ticks`, [telemetry](../reference/telemetry.md)).
**Rollback**: see [Rollback](#rollback).

## Rollback

**Before the switch**, nothing on the source was written: scale the Deployment back up with the old values.

**After the switch**, while the old objects are still there, freeze again and copy back with the files swapped:

```sh
sluis migrate --from new.yaml --to old.yaml --overwrite --i-have-stopped-writers
```

`--overwrite` is needed because the old objects hold older values. It writes the ConfigMaps, Secrets and Valkey (creating
any object that is gone), carrying every record, secret and session with the lifetimes left. Then put `ports.adapter`
(and `valkey.address`) back and roll.

## When it says no

| It says | Meaning and what to do |
|---|---|
| `refusing to copy while the source can still change` | `--i-have-stopped-writers` is missing: freeze, then pass it. |
| `the destination already holds a different value`, naming `<domain>/<kind> <key>` | Nothing was written. The destination is not empty or is the wrong one. Look, then `--overwrite` if the destination's value is the stale one. |
| `the destination does not match the source after the copy` | A key whose second read differs: a writer was still running, or an adapter lost a write. `mismatches` names them; stop what writes and run again. |
| `the source holds items its store cannot read` | A record that does not decode. The rest is copied and verified; fix or delete the named record and run again. |
| `ports.adapter dynamodb: ... secrets adapter` | The destination has no working Secrets adapter. |
| `store unavailable` while copying | A side went away. Run the same command again. |

## Afterwards

- **Leave the old store in place** (ADR 0031): do not delete the ConfigMaps, the Secrets or the Valkey until the new
  installation has been healthy for as long as you want a rollback to stay cheap.
- Data and runtime do not move together: this step moves only where the State is. The runtime move is a later step,
  released and proven separately.
