---
title: AWS Lambda (T5)
description: The Lambda shape runs each sluis role as its own function, zip-packaged with layers and extensions, using KMS and DynamoDB and the vendored Lambda Web Adapter.
status: target
sidebar:
  order: 6
---

T5 runs sluis as one AWS Lambda function per process role. Keys are AWS KMS keys and state is DynamoDB. v1 already runs as many Lambda functions, and T5 keeps that lineage while moving to the v2 role split.

## Topology

```mermaid
flowchart LR
  user["Browser, CLI,<br/>relying parties"] --> ep["Function URL or<br/>API Gateway"]
  subgraph cn["central function"]
    lwa["Lambda Web Adapter"] -- "loopback port" --> app["central process"]
  end
  ep --> lwa
  app -- "invoke" --> signer["signer"]
  app -- "invoke" --> policy["policy"]
  app -- "invoke" --> others["ca, notary, audit-writer,<br/>audit-read, quota"]
  signer --> kms["AWS KMS"]
  others --> kms
  app --> ddb["DynamoDB"]
  others --> ddb
  others -. "outbox" .-> q["Queue"]
  q --> writer["audit-writer"] --> blob["Blob store"]
  others --> idx["postgres index"]
```

## What runs where

- Each role is its own Lambda function. Only `central` takes requests from outside; every other function is invoke-only, and its resource policy names the caller's alias.
- **Zip only.** A function is a zip archive, so layers and extensions combine. A container image is not used on Lambda. Functions, layers and extensions are published as zips for `x86_64` and `arm64`; see [Packages](/distribution/packages/).
- **Drivers are Lambda extensions.** A driver is a platform-started service that speaks ConnectRPC over a Unix socket. On Lambda the platform that starts it is the extension mechanism, and the role calls it over the socket inside the sandbox.
- **Lambda Web Adapter.** `central` speaks HTTP, so the Lambda Web Adapter, pinned at v1.1.0, translates Lambda events into HTTP requests. The adapter is vendored into sluis's own layer, so the function does not reference the public AWS layer.
- **Limits.** A function has at most five layers and 250 MB unzipped, counting the function code, layers and extensions together. The zips are built to fit that.

## Keys

- **KMS:** one symmetric key with encryption contexts for sealing, and one `ECC_NIST_P384` signing key per authority purpose. Each key policy names the one function role allowed to use it.
- A function's execution role is its identity to KMS. Only the `signer` function can use the token-signing key, and only `ca` the CA key.
- Tokens are signed directly with the non-extractable key. There is no software key on Lambda.

## State and audit

- **State:** DynamoDB, with conditional writes and one writer per record kind.
- **Audit outbox:** a queue, because a function's local disk does not outlive its sandbox. The `audit-writer` consumes the queue, seals batches and writes them to the blob store; the `notary` signs batch heads with its KMS key. A dedicated notary is optional.
- **Index:** `postgres`, which is proposed as Aurora Serverless v2.

:::caution[Open decision OD-12: Index host on Lambda]
Options: Aurora Serverless v2, or DynamoDB only. The lean is Aurora, to be verified.
:::

:::caution[Open decision OD-5: Audit outbox per shape]
Options: spool, queue, blocking. The lean is a queue on Lambda.
:::

## Identity and edge

- The function's IAM role is its identity. The platform injects the credentials only into that function's execution environment. A function calls another by `lambda:Invoke`, and the callee reads the invoked alias, which tells it the caller class, and a verified token, which tells it the caller role.
- **Listener exception.** The rule that roles listen only on Unix sockets has a Lambda exception. The Lambda Web Adapter forwards to a loopback TCP port inside the sandbox, which holds only sluis's own processes. The port is never reachable from outside the sandbox. Only `central`'s public API has a TCP listener, and no key-holding role does. See [Listeners](/architecture/listeners/).

:::note[Proposed]
The edge in front of `central` is a Lambda function URL or API Gateway. The choice between them, and whether an edge proxy sits in front of either, is not yet decided.
:::

## Limits and residual risk

- Five layers and 250 MB unzipped bound what a function can carry. Rust or large SDKs push against it; link only the SDK packages a role uses.
- Lambda has cold starts. The adapter and extensions add start-up work, and a larger zip starts slower.
- Outside a VPC, egress is unrestricted. In a VPC, egress rules work by IP, not by name, so a per-function allow-list by hostname needs a proxy or firewall.
- Per-function role granularity needs one zip and one IAM role per role. It raises the number of artifacts and resources to keep in step; the generator derives them all from the manifest.
- The vendored adapter is part of the supply chain. It is pinned to a version, rebuilt into sluis's layer, and signed with the rest of the release.
- The Aurora index adds a standing cost and a network dependency that no other shape has until OD-12 is settled.
