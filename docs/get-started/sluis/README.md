# Getting started: choose a deployment

sluis is ports and adapters: each concern (state, secrets, blobs, signing, trigger, schedule, audit) is served by an
adapter chosen **by name**. A *preset* names one adapter per concern, so you answer a few questions about your platform
instead of choosing seven adapters. One tutorial per shape follows from the answers.

## The decision tree

```text
AWS? ── no ──► Kubernetes? ── no ──► server              (unavailable)
 │                 └─ yes ─► OpenBao? ── yes ─► k8s-openbao   (unavailable)
 │                                      └ no ─► k8s-minimal   (unavailable)
 └─ yes ─► Kubernetes? ── no ──► aws-serverless
               └─ yes ─► sluis on Lambda? ── yes ─► aws-hybrid
                                            └ no ─► k8s-aws
```

The answers are the `platform` block of the service document (`aws`, `kubernetes`, `openbao`, `runtime`); `preset`
names a preset outright, and an installation's shape picks one when it names none
([the installation document](../../reference/sluis/installation-document.md)). The same tree is
`port.PresetFor` in `internal/port/resolve.go`. Which adapter each preset names for each concern is in the
[generated matrix](../../reference/sluis/adapters.md#presets).

## The tutorials

| You have | Preset | Tutorial |
|---|---|---|
| AWS, and you want sluis on Lambda (with or without Kubernetes beside it) | `aws-hybrid`, `aws-serverless` | [sluis on AWS Lambda](aws-lambda.md) |
| Kubernetes on EKS, and you want sluis as a pod with DynamoDB, S3 and KMS | `k8s-aws` | [Kubernetes with AWS storage](kubernetes-aws.md) |
| An installation that already runs on Kubernetes objects and Valkey | `legacy` adapters, no preset | [An existing installation on the legacy store](kubernetes-legacy-store.md) |

`aws-hybrid` is the path the maintainers' estates run. A single concern can be changed on top of a preset with
`adapters.<concern>` in the configuration; the adapters that exist, what each needs and the runtimes it works on are in
[reference/adapters.md](../../reference/sluis/adapters.md), generated from the registry so that it cannot drift from the code.
An adapter that does not exist is added in a fork: [adding an adapter](../../guides/sluis/add-an-adapter.md). The design behind
the registry, resolution order and start-up validation is in
[ports](../../concepts/sluis/ports.md#adapters-presets-and-the-platform).

## Availability

A preset that names an adapter which is not built is **unavailable**: loading it fails with a message that names the
preset and the missing adapters, unless `adapters` replaces every one of them. There is no tutorial for these:

- `server`: unavailable. Its state, secrets, blobs, signing and trigger adapters are planned, not built.
- `k8s-minimal`: unavailable. Its state, secrets, blobs and trigger adapters are planned, not built.
- `k8s-openbao`: unavailable. Its state, blobs, signing and trigger adapters are planned, not built.

Available: `aws-serverless`, `aws-hybrid`, `k8s-aws`. `aws-eks` is the deprecated name of `k8s-aws`: it resolves to it
and start warns. The list is generated from the registry:
[adapters, availability](../../reference/sluis/adapters.md#availability).
