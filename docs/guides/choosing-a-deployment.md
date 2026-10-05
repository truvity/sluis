# Choosing a deployment

sluis is ports and adapters: each concern (state, secrets, blobs, signing,
trigger, schedule, audit) is served by an adapter chosen **by name**. A *preset*
names one adapter per concern, so you answer a few questions about your platform
instead of choosing seven adapters.

## The decision tree

```text
AWS? ── no ──► Kubernetes? ── no ──► server
 │                 └─ yes ─► OpenBao? ── yes ─► k8s-openbao
 │                                      └ no ─► k8s-minimal
 └─ yes ─► Kubernetes? ── no ──► aws-serverless
               └─ yes ─► sluis on Lambda? ── yes ─► aws-hybrid
                                            └ no ─► k8s-aws
```

The answers are the `platform` block of the serve configuration (`aws`,
`kubernetes`, `openbao`, `runtime`, `replicas`); `preset` names a preset outright.
The same tree is `port.PresetFor` in `internal/port/resolve.go`.

### Modifiers

A preset is a starting point. Modifiers change one concern, with
`adapters.<concern>` in the configuration:

| Modifier | Meaning |
|---|---|
| `+valkey` | sessions on Valkey, hosted by ElastiCache, MemoryDB, in the cluster or externally |
| `+postgres` | state in PostgreSQL: CloudNativePG, RDS, Aurora or external |
| `+s3` | blobs in any S3-compatible store |
| audit level `full\|lite\|log` | how much of the audit trail is kept; see [audit's deployment levels](https://github.com/truvity/audit/blob/master/docs/deployment/levels.md) |
| signing `kms\|transit\|generated\|file` | where the token-signing key lives: AWS KMS, an OpenBao transit key, one generated at start and shared through state, or a file the platform mounts |

**Hosting is deploy-level, not code.** One adapter covers several providers:
`valkey` is the same adapter whether the server is ElastiCache, MemoryDB, a pod in
your cluster or something you run yourself. You change the endpoint, not the
code.

## The presets

| Preset | For |
|---|---|
| `aws-hybrid` | AWS and Kubernetes, with sluis itself on Lambda. **The implemented, maintained path.** |
| `k8s-aws` | AWS and Kubernetes, with sluis as a pod on EKS: DynamoDB, S3, KMS-wrapped signing; SSM secrets, or OpenBao. (`aws-eks` is its deprecated name) |
| `aws-serverless` | AWS with no Kubernetes: Lambda only |
| `k8s-openbao` | Kubernetes with OpenBao, off AWS |
| `k8s-minimal` | Kubernetes alone, off AWS |
| `server` | one host: no AWS, no Kubernetes |

Which adapter each preset names for each concern is in the
[generated matrix](../reference/adapters.md#presets).

## Support levels

- **Implemented.** Built, registered and run by the maintainers' estates.
  `aws-hybrid` is the implemented, maintained path: it is what Truvity and hive
  run.
- **On request.** Designed and listed in the matrix, not built. Start refuses an
  adapter that is only planned, naming it. It is built when a user asks for it.
- **DIY.** Fork, add an adapter through a fixed checklist, run it yourself:
  [adding an adapter in a fork](diy-adapter.md). It is an extension, not a rewrite.

## The matrix

[reference/adapters.md](../reference/adapters.md) lists every adapter per concern
with what it needs (AWS, Kubernetes, OpenBao), the runtimes it works on and
whether it is implemented or on request. It is generated from the registry
(`just adapters-doc`), so it cannot drift from the code.

For the design behind the registry, resolution order and start-up validation, see
[design/ports.md](../design/ports.md#adapters-presets-and-the-platform).
