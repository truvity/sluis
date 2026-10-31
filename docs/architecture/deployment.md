---
title: Deployment
description: "L3, enforcement per runtime: how IAM, key-holder, per-actor stores, systemd, nftables and Landlock hold each actor to its manifest."
status: target
sidebar:
  order: 4
---

The [permission manifest](/architecture/#the-permission-manifest-is-the-spine) says what each actor may do. This page says what enforces it on each [runtime](/runtimes/): [bare-metal](/runtimes/bare-metal/), [serverless](/runtimes/serverless/) and [Kubernetes](/runtimes/kubernetes/). The four [tested installations](/runtimes/installations/) are installations of those runtimes and serve as the worked examples below. The goal on every runtime is the baseline that one Lambda function per actor gives for free: one actor is one identity that the resources can see, and nothing else can act as it.

Two axes are enforced separately:

- **The runtime axis** decides who the process is and how the platform proves it. A runtime is only the kind: how processes are started and what they can rely on. How many nodes an installation has, and which outside services it uses, are not part of it (see [Ports and drivers](/concepts/drivers/#how-sluis-config-check-validates-an-installation)).
- **The backend axis** decides what the resource can refuse on its own. A backend that can tell its callers apart is used with per-actor credentials and no mediator. Only a card on a node cannot, and `key-holder` owns it ([Defence in depth](/concepts/drivers/#defence-in-depth)).

## Enforcement per runtime

Every mechanism is generated from the manifests; none is written by hand.

| Runtime | Identity to cloud resources | Shared-driver ACLs | systemd sandboxing | nftables per uid | Landlock |
| ----------- | --------------------------- | ------------------ | ------------------ | ---------------- | -------- |
| **bare-metal** (a board, a virtual machine such as EC2, or local Docker) | A workload JWT per actor, issued by the node agent from the actor's uid; on a cloud VM the instance role holds almost nothing | `key-holder` for a card on the node; an rqlite cluster with a user per actor for state; cloud IAM and KMS key policies where they apply | Every unit (not in a container) | Default-deny egress with per-uid allow-lists, IMDS closed on a cloud VM, the Raft port between peers on N nodes | Per actor where the kernel has it (5.13 or later) |
| **serverless** (AWS Lambda) | The function's execution role, one per actor. Calls between functions are authorized by a resource policy on an alias | None: IAM does the job | The platform's sandbox | Not available. A VPC route can cut `policy` off entirely | Optional, in process |
| **kubernetes** (proposed) | A projected ServiceAccount token per container, one audience per actor, exchanged for a cloud role. Pod Identity only for a single-actor pod | None where the cloud does it. The socket volume mounted into the permitted callers is the first lock | Pod security context per container | NetworkPolicy per pod kind. FQDN egress needs Cilium (optional). Egress is the union over the containers of a pod | Optional |

The number of nodes changes only the details inside a row: on N bare-metal nodes the Raft port of each rqlite cluster is opened between peers, and one hardware key per node is used.

### Tested installations as examples

The same mechanisms, read for each of the four [tested installations](/runtimes/installations/). An installation is a runtime plus a node count, the available dependencies and one driver per port.

| Installation | Identity to cloud resources | Shared-driver ACLs | systemd sandboxing | nftables per uid | Landlock |
| ------------ | --------------------------- | ------------------ | ------------------ | ---------------- | -------- |
| **local** (bare-metal, Docker on a workstation, development) | None. A software key or the host's YubiKey; nothing is production | `key-holder` optional | Not used in a container | Not used | Not used |
| **boards** (bare-metal, three single-board computers, a YubiKey per node) | Workload JWT per actor; no cloud credential is needed for the board-only parts | `key-holder` for each node's YubiKey; one rqlite cluster per owning actor, with rqlite users and the Raft ports over mTLS between peers | Every unit | Default-deny egress with per-uid allow-lists, plus the Raft port between peers only | Not on the current board image (kernel 5.10); the other layers carry the load |
| **lambda** (serverless) | The function's execution role, one per actor. Calls between functions are authorized by resource policy on an alias | None: IAM does the job | The platform's sandbox | Not available. A VPC route can cut `policy` off entirely | Optional, in process |
| **kubernetes** (kubernetes, EKS, proposed) | A projected ServiceAccount token per container, one audience per actor, exchanged for a cloud role. Pod Identity only for a single-actor pod | None where the cloud does it. The socket volume mounted into the permitted callers is the first lock | Pod security context per container | NetworkPolicy per pod kind. FQDN egress needs Cilium (optional). Egress is the union over the containers of a pod | Optional |

A bare-metal node on a cloud VM (a possible installation, not a tested one) reads like boards with two differences: the node agent's workload JWT is exchanged for an IAM role per actor, so KMS key policies replace `key-holder` and DynamoDB IAM conditions guard the state; and the metadata service is closed to every uid but the node agent.

### Workload JWT and per-cloud exchange

:::note[Proposed]
On bare-metal nodes, the node agent (`sluis-node`) issues a short-lived **workload JWT per actor**. The actor's identity comes from its uid, read from the Unix socket's peer credentials. Each cloud driver (AWS, GCP, Azure) exchanges the workload JWT for that cloud's credentials:

| Cloud | Mechanism | Signing algorithm |
| ----- | --------- | ----------------- |
| AWS | `AssumeRoleWithWebIdentity` against an IAM OIDC provider | ES384 (P-384) |
| GCP | Workload Identity Federation | ES256 |
| Azure | A federated credential | RS256 |

The node's non-extractable hardware key signs the workload JWT, about once an hour per actor (about 120 ms per signature). Platform-native identity is used where it exists: a Lambda function role. On Kubernetes the platform's own token is proposed to be used (see the kubernetes row above and [Identity](/concepts/identity/)).

Discovery is a static document plus the public keys at an HTTPS URL. There is **one issuer per boundary** by default, and its key set holds one key per node card. Per-node issuers, `https://<issuer>/nodes/<node>/`, are optional for a fleet whose nodes run different actors; there each cloud role trusts only the issuers of the nodes that run that actor ([ADR 0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). AWS allows at most 100 EC keys in a JWKS and 100 OIDC providers per account, and requires https with no port and no query.

IAM Roles Anywhere stays an AWS-only alternative. `aws_signing_helper serve` is not used, because it exposes credentials on a local port to every process.
:::

### IAM per actor

Each actor has its own IAM role (or Lambda function role; on Kubernetes, proposed, an IAM role that trusts the cluster's issuer and the actor's token audience), generated from its manifest. The trust relationship differs by runtime; the permissions policy is the same in all of them.

KMS key policies name one actor's role each. On AWS there is one symmetric key (`ENCRYPT_DECRYPT`) with encryption contexts for every sealing need, and one `ECC_NIST_P384` `SIGN_VERIFY` key per authority purpose (token signing, CA, audit log, grant), because KMS allows one usage per key and `Sign` takes no encryption context. The audit log has one key per log, and a log has exactly one writer, so a cluster of nodes on AWS would have one key per node, whose peers are its witnesses; a shared key cannot be restricted that way, because IAM cannot restrict which message gets signed. A generated key policy for the audit log key looks like this (an example; `<account-id>` and `<install>` are placeholders):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "UseByWriterRoleOnly", "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::<account-id>:role/sluis-<install>-writer" },
      "Action": ["kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"], "Resource": "*" },
    { "Sid": "DenyUseByAnyoneElse", "Effect": "Deny", "Principal": { "AWS": "*" },
      "Action": ["kms:Sign", "kms:Verify", "kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey*"],
      "Resource": "*",
      "Condition": { "ArnNotEquals": { "aws:PrincipalArn": "arn:aws:iam::<account-id>:role/sluis-<install>-writer" } } },
    { "Sid": "DenyNodeRoleAnyUse", "Effect": "Deny", "Principal": { "AWS": "*" },
      "Action": "kms:*", "Resource": "*",
      "Condition": { "ArnEquals": { "aws:PrincipalArn": "arn:aws:iam::<account-id>:role/sluis-<install>-node" } } }
  ]
}
```

On a cloud VM such as EC2 the instance role holds only what the node agent needs: the permission to hand out the per-actor identities, to read signed configuration and binaries, and to complete lifecycle hooks. It holds no KMS, no secrets and no state access, and every key policy denies it.

### Who enforces per backend

A backend that separates callers itself is used with per-actor credentials. Where it cannot, `key-holder` owns it and applies an ACL generated from the manifests.

| Backend | Separates callers itself? | Who enforces |
| ------- | ------------------------- | ------------ |
| AWS KMS | Yes: key policy and IAM per principal | The platform |
| PKCS#11 token or YubiKey PIV | No: one PIN per device, and a logged-in session is usable by any code in that process | `key-holder`, with one socket per caller, a peer-uid check, `(actor, purpose, operation)` and content-aware signing per purpose. One device per trust domain |
| DynamoDB | Yes: IAM with leading-key conditions | The platform |
| rqlite | Per user and operation, not per record, so each owning actor gets a **cluster of its own** | rqlite itself: the owner's user may `execute`, readers' users may only `query` |
| Embedded store | A file | The file's owner: a file per uid, per actor |
| PostgreSQL | Yes: roles, grants and row-level security | The database ([PG-1](/concepts/drivers/#postgres-drivers)) |

`key-holder` emits the audit record first, then acts.

### systemd sandboxing

Every unit gets a generated sandbox profile. An example drop-in for an actor with no network need (illustrative; the real values come from the manifest):

```ini
# /etc/systemd/system/sluis-policy@prod.service.d/10-sandbox.conf
[Service]
User=sluis-policy-prod
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectProc=invisible
ProcSubset=pid
PrivateNetwork=yes
RestrictAddressFamilies=AF_UNIX
SystemCallFilter=@system-service
CapabilityBoundingSet=
MemoryDenyWriteExecute=yes
RuntimeDirectory=sluis/policy/prod
LoadCredential=policy.cedar:/etc/sluis/policy
```

The packages ship **template units** only, `sluis-<actor>@.service` and `sluis-<actor>@.socket`, with `User=sluis-<actor>-%i`, `RuntimeDirectory=sluis/<actor>/%i`, `LoadCredentialEncrypted=` per instance and `ListenStream=/run/sluis/<actor>/%i.sock`. A **systemd generator** shipped in the package reads `/etc/sluis/install.yaml` and creates the instances and their `sysusers.d` entries (agreed, SY-1). Every unit gets `ProtectSystem=strict`, `NoNewPrivileges=`, an empty `CapabilityBoundingSet=` and `SystemCallFilter=@system-service`; every actor that needs no network, `entry` included, gets `PrivateNetwork=yes` and `RestrictAddressFamilies=AF_UNIX`.

Integrations, the actors that need an outbound connection, get egress through the proxy and `IPAddressAllow=` for it, instead of `PrivateNetwork=yes`. Secrets arrive through `LoadCredential=`. The systemd floor is version 252 (Amazon Linux 2023). On the board images that run Ubuntu 22.04 (systemd 249) units use plain `LoadCredential=`, because `LoadCredentialEncrypted=` is not available there.

### nftables per uid

The firewall is default-closed and keyed on the owner of the socket:

- **IMDS** (`169.254.169.254` and its IPv6 address) is dropped for every uid but the node agent, so a compromised actor cannot reach the instance role.
- **Egress** is an allow-list per uid (`meta skuid`): an integration reaches its provider's hosts and nothing else. `entry`, `policy`, `signer`, `key-holder` and every other actor without a provider have no network at all.
- **Inbound** is closed except for the Raft ports of the rqlite clusters, which accept only the other nodes' addresses and are the only TCP ports between nodes. `entry`'s public API is a Unix socket and takes no inbound connection from the network; the edge connector reaches it from the same host.

The per-unit `IPAddressAllow=` and `IPAddressDeny=` directives are a second layer, tested so that no unit lacks them.

### Landlock

Each actor restricts itself with Landlock after start-up: it may read its own configuration and credentials directory and read and write its own runtime directory, and nothing else. This holds even if a bug lets code run inside the actor. It needs a kernel with Landlock enabled (5.13 or later). The vendor kernel 5.10 on the current board images lacks it, so there `sluis doctor` warns and the other layers carry the load.

### The deny matrix

The manifests are data, so the test that proves isolation is generated, not written. For every pair of actors, actor X is denied actor Y's records, secrets, keys, sockets and transport. A failing cell is a policy that is too wide, and the build fails.

## Residual risk

Some guarantees cannot be matched without hardware isolation per process. They are written down here rather than hidden.

| Risk | Why it remains | What reduces it |
| ---- | -------------- | --------------- |
| **Root on the node** | The identity of an actor on a bare-metal node is asserted by the host (uid, cgroup). A root attacker acts as every actor on that node. This matches Kubernetes: node root is all pods' actors on that node | A small node agent with no egress but the cloud's token endpoint; short-lived credentials; a separate node per trust domain where it matters |
| **Memory reads** | Processes are long-lived, and secrets and a decrypted signing ring stay in memory. A same-uid or root attacker reading memory, or a core dump, sees them | `ProtectProc=invisible`, `PrivateTmp`, no core dumps, no ptrace, restarts on rotation. Authority keys never enter process memory: they stay in KMS or on the token |
| **Kernel or sandbox escape** | The actors share one kernel | Minimal syscalls, `NoNewPrivileges`, Landlock; accept |
| **The node agent is a trusted base** | It can obtain credentials for every actor on its node | Keep it tiny and audited; no parsing of caller input beyond a fixed protocol |
| **The mediating driver is one door** | A compromised `key-holder` can use every key on its card while it runs. It cannot extract the keys | One small process that holds the card exclusively (Go with `piv-go`), no network, one socket per caller, content-aware signing, a rate limit, fuzzed decoding, an audit record before every action. State has no mediator: each owning actor has its own store |
| **One PIN per YubiKey** | A PIV device has no per-slot caller separation. Unattended signing needs a touch policy of never, so physical theft of a powered host is use | One device per trust domain; the root pair offline, each root with its own independent key, PIN always and touch always |
| **A compromised writer with no witness** | With no witness and no notary, a compromised audit `writer` can get a rewritten checkpoint signed through `key-holder` or its KMS key. A compliance lock still stops overwriting, but not a split view | A witness is optional, and `sluis config check` warns when there is none. A `peer` witness on a cluster, or an `external` one, exposes a split view. See [Audit](/concepts/audit/#the-witness-is-optional) |
| **A cluster is one trust domain** | A compromised node reads replicated state | The blast radius is the install, not the estate |
| **Shared egress address** | One NAT means no per-actor source identity at the network layer. A proxy is a software control | Make the proxy mandatory by firewall |
| **Token-signing key in a signer's memory** | If a software ring is in use, a compromised `signer` can mint tokens until the key rotates | Direct signing with a non-extractable key on the AWS installations; rotation |
| **Account or key-admin compromise** on AWS | KMS key policy cannot hide use from the account root or a key administrator | The control-plane actions are denied to every actor; break-glass use is audited |

Serverless removes the first three rows: the platform attests the function and runs each in its own micro-VM. That is why serverless needs no node agent.

