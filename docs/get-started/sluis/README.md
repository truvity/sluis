# Getting started: choose a deployment

Each concern (state, secrets, blobs, signing, trigger, schedule, audit) is served by an adapter chosen by name. A preset names one adapter per concern, so you answer a few questions about your platform instead of choosing seven adapters.

## Decision tree

```text
AWS? ── no ──► Kubernetes? ── no ──► server              (unavailable)
 │                 └─ yes ─► OpenBao? ── yes ─► k8s-openbao   (unavailable)
 │                                      └ no ─► k8s-minimal   (unavailable)
 └─ yes ─► Kubernetes? ── no ──► aws-serverless
               └─ yes ─► sluis on Lambda? ── yes ─► aws-hybrid
                                            └ no ─► k8s-aws
```

The answers are the `platform` block of the service document. `preset` names a preset outright; see [the installation document](../../reference/sluis/installation-document.md). The same tree is `port.PresetFor` in `internal/port/resolve.go`.

## Tutorials

| You have | Preset | Tutorial |
|---|---|---|
| AWS, and you want sluis on Lambda | `aws-hybrid`, `aws-serverless` | [sluis on AWS Lambda](aws-lambda.md) |
| EKS, and you want sluis as a pod with DynamoDB, S3 and KMS | `k8s-aws` | [Kubernetes with AWS storage](kubernetes-aws.md) |
| An installation on Kubernetes objects and Valkey | `legacy` adapters, no preset | [Legacy store](kubernetes-legacy-store.md) |

Shapes beyond the tutorials, including Cloudflare in front of Lambda, are in [deployment shapes](deployment/README.md).

## Availability

A preset that names an adapter that is not built is unavailable. Loading it fails, naming the preset and the missing adapters, unless `adapters` replaces every one of them.

- `aws-serverless`, `aws-hybrid` and `k8s-aws` are available. `aws-eks` is the deprecated name of `k8s-aws`: it resolves to it and start warns.

- `server`, `k8s-minimal` and `k8s-openbao` are unavailable.

To change one concern on top of a preset, set `adapters.<concern>`. The [adapter reference](../../reference/sluis/adapters.md) lists the presets, adapters and availability. To add an adapter, see [adding an adapter](../../guides/sluis/add-an-adapter.md). Design: [ports](../../concepts/sluis/ports.md#adapters-presets-and-the-platform).
