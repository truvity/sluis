# The store

Everything that is not a blob sits behind one **State port**, dynamic secrets behind a **Secrets port**, and reports
and snapshots behind a **Blob port**. The contracts are in [ports](ports.md) and the decisions in
[ADR 0027](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md) (State: DynamoDB; the other adapter it named was
removed on 2026-10-04), [ADR 0028](../../decisions/0028-nothing-writes-configmaps-or-secrets.md) and
[ADR 0031](../../decisions/0031-a-generic-migration-tool.md). Which adapter an installation runs is a preset:

| Platform | State | Secrets | Blobs |
|---|---|---|---|
| AWS (Lambda or Kubernetes) | DynamoDB, one table with per-item TTL | SSM | S3 |
| Kubernetes, until an estate's cutover | the `legacy` adapter: ConfigMaps and, optionally, Valkey | Kubernetes Secrets the service writes | ConfigMaps or Valkey |
| tests and the demonstration | memory | memory | memory |

The tables of adapters are generated in [adapters](../../reference/sluis/adapters.md); the DynamoDB and SSM layouts are in
[storage layout](../../reference/sluis/storage-layout.md); the legacy object names are in
[Kubernetes objects](../../reference/sluis/kubernetes-objects.md). With DynamoDB, replicas coordinate through the State port's
leases and compare-and-swap, so sessions, the refresh lease and the signing-key schedule are shared without Valkey
([freshness](freshness.md#the-refresh-lease)). The rest of this page is the shape of the records, which holds on every
adapter.

## Record and credential are two things

A workspace is two objects because the record and the credential have different readers: the record is what the
console shows, the credential is written once and read once, at the next start. Splitting them keeps a secret out of
the type the console handles, and makes the failure modes independent: a record whose credential has gone is a
workspace with no reader, which is reported as unhealthy, rather than a process that refuses to start. A connected
GitHub organisation or Slack workspace is two objects for the same reason.

Start-up has three cases. A workspace the service document declares is opened from what the deployment mounts. A
workspace whose stored record says it was declared, and which the document no longer mentions, has been taken out of
the deployment: its record is deleted, because leaving it would be a directory nobody could disconnect. Everything
else was connected in the console and is opened from the credential stored beside it, and if that credential is
missing or refused, the process says so and carries on, because refusing to start would take every other directory
down with it.

Each credential carries a copy of its record, without its health, so the credentials alone restore an installation:
start-up puts back every record that is missing beside a credential. A deployment makes the backup with an export or
a `PushSecret` per bundle; nothing in the service depends on the copy. Without one, the recovery for a lost workspace
credential is **Reconnect**, and a declared secret is re-delivered by whatever declared it. The console never returns
secret material, the logs never print it, and **Disconnect** revokes the token at the backend before the credential
is deleted. Backing up and restoring: [back up and restore](../../guides/sluis/operate/back-up-and-restore.md).

On the legacy Kubernetes adapter the controllers' credentials are mounted as volumes, not read through the API, so the
controller holds no permission to read them. The service, not the controller, creates each controller's report
object, and the controller only ever replaces its data: Kubernetes RBAC cannot narrow `create` to a name, so a
controller that created its own report would need to be allowed to create any ConfigMap in the namespace, including
one that reads as a workspace record. The chart does not render the report either, because a ConfigMap whose data a
controller rewrites is one a GitOps sync reverts.

## Signing keys

The signing key is a file (or a KMS key), never read through the Kubernetes API, so a compromise of this process
cannot become a read of every credential in its namespace. Confidential clients' secrets are files for the same
reason, one per client id.

The file is polled, not read once at start: cert-manager's `rotationPolicy: Always` means a renewal is a new key, and
a process that only reads it at boot would keep signing with the old one until it next restarts, which for a
certificate renewed every year could be a long time. A key ring keeps every key this replica has published, live or
retiring: the new one is in the JWKS the moment it is seen, signing starts only once every other replica has had time
to notice and publish it too, and the previous key stays published for as long as a token it signed can still be
presented. The schedule for a key is decided once and shared through the State port, so a replica that restarts
mid-rotation does not forget a key still inside its overlap.
