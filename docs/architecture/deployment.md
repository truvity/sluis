---
title: Deployment
description: "L3, enforcement per shape: how IAM, mediators, systemd, nftables and Landlock hold each role to its permission manifest, and what stays residual risk."
status: target
sidebar:
  order: 4
---

The [permission manifest](/architecture/#the-permission-manifest-is-the-spine) says what each role may do. This page says what enforces it on each [shape](/shapes/). The goal on every shape is the baseline that one Lambda function per role gives for free: one role is one identity that the resources can see, and nothing else can act as it.

Two axes are enforced separately:

- **The shape axis** decides who the process is and how the platform proves it.
- **The backend axis** decides what the resource can refuse on its own. Where a backend cannot tell its callers apart, a mediator owns the resource.

## Enforcement per shape

The table reads shape by mechanism. Every mechanism is generated from the manifests; none is written by hand.

| Shape | Identity to cloud resources | Mediator ACLs | systemd sandboxing | nftables per uid | Landlock |
| ----- | --------------------------- | ------------- | ------------------ | ---------------- | -------- |
| **T0 Local** (development) | None. Software keys; nothing is production | `state-owner` and `key-holder` optional | Not used in a container | Not used | Not used |
| **T1 Single node** (one single-board computer, YubiKey) | Workload JWT per role; cloud-free needs no cloud credential | `key-holder` for the YubiKey; `state-owner` in front of the single-node store | Every unit | Default-deny egress with per-uid allow-lists | Not on the current board image (kernel 5.10); the other layers carry the load |
| **T2 Cluster** (three boards) | As T1, one hardware key per node | As T1, plus the Raft port over mTLS between `state-owner` processes | Every unit | As T1, plus the Raft port between peers only | As T1 |
| **T3 AWS EC2 single** | IAM role per process role, through the node agent's workload JWT; the instance role holds almost nothing | KMS key policies replace `key-holder`. DynamoDB IAM replaces `state-owner` | Every unit | IMDS closed to every uid but the node agent; egress allow-lists | Per role |
| **T4 AWS EC2 cluster** (ASG of three) | As T3 | KMS replaces `key-holder`; `state-owner` in front of Raft | Every unit | As T3, plus the Raft port between nodes | Per role |
| **T5 AWS Lambda** | The function's execution role, one per role. Calls between functions are authorized by resource policy on an alias | None: IAM does the job | The platform's sandbox | Not available. A VPC route can cut `policy` off entirely | Optional, in process |
| **Kubernetes** | EKS Pod Identity: a ServiceAccount per role bound to an IAM role | None where the cloud does it; same as T4 with Raft | Pod security context | NetworkPolicy, with FQDN egress rules | Optional |

### Workload JWT and per-cloud exchange

:::note[Proposed]
On self-hosted nodes (T1 to T4), the node agent issues a short-lived **workload JWT per role**. The role's identity comes from its uid, read from the Unix socket's peer credentials. Each cloud driver (AWS, GCP, Azure) exchanges the workload JWT for that cloud's credentials:

| Cloud | Mechanism | Signing algorithm |
| ----- | --------- | ----------------- |
| AWS | `AssumeRoleWithWebIdentity` against an IAM OIDC provider | ES384 (P-384) |
| GCP | Workload Identity Federation | ES256 |
| Azure | A federated credential | RS256 |

The node's non-extractable hardware key signs the workload JWT, about once an hour per role (about 120 ms per signature). Platform-native identity is used where it exists: a Lambda function role, EKS Pod Identity.

Discovery is a static document plus the public keys at an HTTPS URL served from the distribution bucket. There is **one issuer per node**, `https://<issuer>/nodes/<node>/`. Each cloud role trusts only the issuers of the nodes that run that role, so a compromised node can mint only its own roles. AWS allows at most 100 EC keys in a JWKS and 100 OIDC providers per account, and requires https with no port and no query.

IAM Roles Anywhere stays an AWS-only alternative. `aws_signing_helper serve` is not used, because it exposes credentials on a local port to every process.
:::

### IAM per role

Each role has its own IAM role (or Lambda function role, or Pod Identity association), generated from its manifest. The trust relationship differs by shape; the permissions policy is the same on all of them.

KMS key policies name one role each. On AWS there is one symmetric key (`ENCRYPT_DECRYPT`) with encryption contexts for every sealing need, and one `ECC_NIST_P384` `SIGN_VERIFY` key per authority purpose (token signing, CA, notary, grant), because KMS allows one usage per key and `Sign` takes no encryption context. A generated key policy for the notary key looks like this (an example; `<account-id>` and `<install>` are placeholders):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "UseByNotaryRoleOnly", "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::<account-id>:role/sluis-<install>-notary" },
      "Action": ["kms:Sign", "kms:GetPublicKey", "kms:DescribeKey"], "Resource": "*" },
    { "Sid": "DenyUseByAnyoneElse", "Effect": "Deny", "Principal": { "AWS": "*" },
      "Action": ["kms:Sign", "kms:Verify", "kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey*"],
      "Resource": "*",
      "Condition": { "ArnNotEquals": { "aws:PrincipalArn": "arn:aws:iam::<account-id>:role/sluis-<install>-notary" } } },
    { "Sid": "DenyNodeRoleAnyUse", "Effect": "Deny", "Principal": { "AWS": "*" },
      "Action": "kms:*", "Resource": "*",
      "Condition": { "ArnEquals": { "aws:PrincipalArn": "arn:aws:iam::<account-id>:role/sluis-<install>-node" } } }
  ]
}
```

On EC2 the instance role holds only what the node agent needs: the permission to hand out the per-role identities, to read signed configuration and binaries, and to complete lifecycle hooks. It holds no KMS, no secrets and no state access, and every key policy denies it.

### Mediator ACLs

Where the backend cannot separate callers, a mediator owns it and applies an ACL generated from the manifests.

| Backend | Separates callers itself? | Who enforces |
| ------- | ------------------------- | ------------ |
| AWS KMS | Yes: key policy and IAM per principal | The platform |
| PKCS#11 token or YubiKey PIV | No: one PIN per device, and a logged-in session is usable by any code in that process | `key-holder`, with `(role, purpose, operation)`. One device per trust domain |
| DynamoDB | Yes: IAM with leading-key conditions | The platform |
| rqlite | No: permissions are per operation, not per table or record | `state-owner`, with `(role, record kind, operation)` and one writer per kind |
| Embedded Raft | No: it is a library | `state-owner` is the Raft node; the state machine applies the ACL on every command |

Both mediators emit the audit record first, then act.

### systemd sandboxing

Every unit gets a generated sandbox profile. An example drop-in for a role with no network need (illustrative; the real values come from the manifest):

```ini
# /etc/systemd/system/sluis-policy.service.d/10-sandbox.conf
[Service]
User=sluis-policy
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
RuntimeDirectory=sluis/policy
LoadCredential=policy.cedar:/etc/sluis/policy
```

Roles that need an outbound connection get `IPAddressAllow=` for their provider instead of `PrivateNetwork=yes`. Secrets arrive through `LoadCredential=`. The systemd floor is version 252 (Amazon Linux 2023). On the board images that run Ubuntu 22.04 (systemd 249) units use plain `LoadCredential=`, because `LoadCredentialEncrypted=` is not available there.

### nftables per uid

The firewall is default-closed and keyed on the owner of the socket:

- **IMDS** (`169.254.169.254` and its IPv6 address) is dropped for every uid but the node agent, so a compromised role cannot reach the instance role.
- **Egress** is an allow-list per uid (`meta skuid`): an integration reaches its provider's hosts and nothing else. `policy`, `signer`, `key-holder` and `state-owner` have no egress at all.
- **The Raft port** accepts only the other nodes' addresses.

The per-unit `IPAddressAllow=` and `IPAddressDeny=` directives are a second layer, tested so that no unit lacks them.

### Landlock

Each role restricts itself with Landlock after start-up: it may read its own configuration and credentials directory and read and write its own runtime directory, and nothing else. This holds even if a bug lets code run inside the role. It needs a kernel with Landlock enabled (5.13 or later). The vendor kernel 5.10 on the current board images lacks it, so there `sluis doctor` warns and the other layers carry the load.

### The deny matrix

The manifests are data, so the test that proves isolation is generated, not written. For every pair of roles, role X is denied role Y's records, secrets, keys, sockets and queue. A failing cell is a policy that is too wide, and the build fails.

## Residual risk

Some guarantees cannot be matched without hardware isolation per process. They are written down here rather than hidden.

| Risk | Why it remains | What reduces it |
| ---- | -------------- | --------------- |
| **Root on the node** | The identity of a role on a self-hosted node is asserted by the host (uid, cgroup). A root attacker acts as every role on that node. This matches Kubernetes: node root is all pods' roles on that node | A small node agent with no egress but the cloud's token endpoint; short-lived credentials; a separate node per trust domain where it matters |
| **Memory reads** | Processes are long-lived, and secrets and a decrypted signing ring stay in memory. A same-uid or root attacker reading memory, or a core dump, sees them | `ProtectProc=invisible`, `PrivateTmp`, no core dumps, no ptrace, restarts on rotation. Authority keys never enter process memory: they stay in KMS or on the token |
| **Kernel or sandbox escape** | The roles share one kernel | Minimal syscalls, `NoNewPrivileges`, Landlock; accept |
| **The node agent is a trusted base** | It can obtain credentials for every role on its node | Keep it tiny and audited; no parsing of caller input beyond a fixed protocol |
| **A mediator is one door** | A compromised `key-holder` can use every key it serves while it runs; a compromised `state-owner` can read and write every record. Neither can extract the keys | One small process, no egress, fuzzed decoding, an audit record before every action |
| **One PIN per YubiKey** | A PIV device has no per-slot caller separation. Unattended signing needs a touch policy of never, so physical theft of a powered host is use | One device per trust domain; the root pair offline |
| **A cluster is one trust domain** | A compromised node reads replicated state | The blast radius is the install, not the estate |
| **Shared egress address** | One NAT means no per-role source identity at the network layer. A proxy is a software control | Make the proxy mandatory by firewall |
| **Token-signing key in a signer's memory** | If a software ring is in use, a compromised `signer` can mint tokens until the key rotates | Direct signing with a non-extractable key on KMS shapes; rotation |
| **Account or key-admin compromise** on AWS | KMS key policy cannot hide use from the account root or a key administrator | The control-plane actions are denied to every role; break-glass use is audited |

Lambda removes the first three rows: the platform attests the function and runs each in its own micro-VM. That is why a shape on Lambda needs no node agent.

:::caution[Open decision OD-4: Which TCP ports exist between nodes]
Raft peer traffic is the state driver's own TCP port over mTLS. Whether any other inter-node port exists (a role port) is open. The lean is that only the Raft port does.
:::
