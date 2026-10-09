# How does sluis store its records?

Everything that is not a blob sits behind one State port. Dynamic secrets sit behind a Secrets port, and reports and
snapshots behind a Blob port. The contracts are in [ports](ports.md). The adapter an installation runs is a preset:

| Platform | State | Secrets | Blobs |
|---|---|---|---|
| AWS (Lambda or Kubernetes) | DynamoDB, one table with per-item TTL | SSM | S3 |
| Kubernetes, until an estate's cutover | the `legacy` adapter: ConfigMaps and, optionally, Valkey | Kubernetes Secrets the service writes | ConfigMaps or Valkey |
| tests and the demonstration | memory | memory | memory |

The adapter tables are generated in [adapters](../../reference/sluis/adapters.md). The DynamoDB and SSM layouts are in
[storage layout](../../reference/sluis/storage-layout.md). The legacy object names are in
[Kubernetes objects](../../reference/sluis/kubernetes-objects.md).

With DynamoDB, replicas coordinate through the State port's leases and compare-and-swap. Sessions, the refresh lease and
the signing-key schedule are shared without Valkey ([freshness](freshness.md#the-refresh-lease)).

## Why are a record and its credential two objects?

The record is what the console shows. The credential is written once and read once, at the next start. A GitHub
organisation or Slack workspace is two objects for this reason. A secret stays out of the type the console handles. The failures stay independent. A record whose credential has gone is
reported unhealthy and does not stop the process.

Start-up has three cases. A workspace the service document declares opens from what the deployment mounts. A declared
workspace that the document no longer mentions has its record deleted. Everything else was connected in the console and
opens from the credential stored beside it.

If that credential is missing or refused, the process says so and carries on, so one directory never takes the others down.

Each credential carries a copy of its record without its health, so the credentials alone restore an installation.
Start-up puts back every record missing beside a credential. A deployment makes the backup with an export or a
`PushSecret` per bundle. Nothing in the service depends on the copy.

Without a backup, **Reconnect** recovers a lost workspace credential, and whatever declared a secret re-delivers it. The
console never returns secret material and the logs never print it. **Disconnect** revokes the token at the backend before
it deletes the credential. See [back up and restore](../../guides/sluis/operate/back-up-and-restore.md).

## Who may create what on the legacy adapter?

Controllers' credentials are mounted as volumes, so a controller holds no permission to read them through the API. The
service creates each controller's report object, and the controller only replaces its data.

Kubernetes RBAC cannot narrow `create` to a name. A controller that created its own report would be allowed to create any
ConfigMap in the namespace, including one that reads as a workspace record. The chart does not render the report either,
because a GitOps sync reverts a ConfigMap whose data a controller rewrites.

## How are signing keys stored?

The signing key is a file or a KMS key, never read through the Kubernetes API. A compromise of this process then cannot
become a read of every credential in its namespace. Confidential clients' secrets are files too, one per client id.

The file is polled, not read once. With cert-manager's `rotationPolicy: Always`, a renewal is a new key, and a process
that read it only at boot would keep signing with the old one.

A key ring keeps every key this replica has published, live or retiring. A new key is in the JWKS the moment it is seen.
Signing starts once every other replica has had time to publish it. The previous key stays published while a token it signed
can still be presented. The State port shares each key's schedule, so a restart mid-rotation forgets nothing.

## Decided in

- [ADR 0027](../../decisions/0027-the-state-port-nats-jetstream-and-dynamodb.md): the State port, DynamoDB
- [ADR 0028](../../decisions/0028-nothing-writes-configmaps-or-secrets.md): nothing writes ConfigMaps or Secrets
- [ADR 0031](../../decisions/0031-a-generic-migration-tool.md): a generic migration tool
