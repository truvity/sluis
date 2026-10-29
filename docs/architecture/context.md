---
title: System context
description: "L1, sluis as a black box: who uses it, the external API it serves, and which resources each use case touches."
status: target
sidebar:
  order: 1
---

At the first zoom level sluis is one box. This page says who talks to it, what they can call, and which resources it touches to answer. How the box is split into components is [L2](/architecture/components/); how it is split into processes is [L3](/architecture/processes/).

## Who uses sluis

```mermaid
flowchart LR
  person["People"]
  robot["Robots"]
  rp["Relying parties"]
  op["Operators"]
  aud["Auditors"]

  subgraph sluis_boundary["sluis"]
    sluis["identity and<br/>access broker"]
  end

  up["Upstream identity<br/>providers"]
  facts["Directory and<br/>team systems"]
  vendors["Downstream<br/>systems"]
  platform["Platform"]

  person -->|"sign in, sign out"| sluis
  robot -->|"exchange a token"| sluis
  rp -->|"discover, verify"| sluis
  op -->|"manage, rotate"| sluis
  aud -->|"query, verify"| sluis

  sluis -->|"federates sign-in"| up
  sluis -->|"reads facts"| facts
  sluis -->|"mints, provisions"| vendors
  sluis -->|"keys, records"| platform
```

What each box stands for:

| Box | Contents |
| --- | -------- |
| People | A browser or a CLI. They sign in, refresh and sign out |
| Robots | CI jobs, workloads and services. They present a workload token and exchange it |
| Relying parties | Apps and cloud APIs that trust sluis tokens. They discover the issuer, fetch keys and verify tokens |
| Operators | The console and the CLI. They connect, manage, rotate and back up |
| Auditors | Auditors and independent verifiers. They query and verify the audit trail |
| sluis | The identity and access broker: OIDC, token exchange, mint, directory, audit |
| Upstream identity providers | Workspace directories and generic OIDC |
| Directory and team systems | Facts about people and groups; sluis reads and reconciles them |
| Downstream systems | Cloud APIs, GitHub, chat workspaces; sluis mints credentials and provisions desired state |
| Platform | KMS or YubiKey, blob, transport, state: keys, records, objects and messages |

| Party | What they do with sluis |
| ----- | ----------------------- |
| **Person** | Signs in through an upstream identity provider, from a browser or from a CLI on a machine with a browser; signs in on a headless machine with device login; refreshes, lists and revokes their sessions; signs out |
| **Robot** | A CI job or a workload presents a platform token (a GitHub Actions token, a Kubernetes ServiceAccount token, an AWS web-identity token) and exchanges it for an access token, a downstream credential, or a certificate |
| **Relying party** | Reads discovery and the key set, then verifies tokens offline with any standard JOSE library. sluis needs no client library |
| **Operator** | Connects and reconnects integrations, manages clients and secrets, reads the console, runs backup, restore and migration, and holds the key ceremonies |
| **Auditor** | Queries the audit trail and verifies it independently, without trusting sluis's index or state |
| **Upstream provider** | Authenticates people. sluis federates sign-in and never stores a password for a person |
| **Directory and team systems** | The source of facts: who is in which group or team. Policy maps those facts to grants |
| **Downstream system** | Receives credentials sluis mints and desired state sluis provisions |

## The external API surface

Four kinds of caller identity reach the box, and these are all it has to verify: a person's session cookie, a person's bearer (the access token of a live sign-in), a workload's platform token, and a confidential client's secret. Only discovery and the key set are public.

### OIDC and OAuth endpoints

Served on the origin root of the public host (for example `https://sluis.example.com`).

| Endpoint | Method and path | Caller |
| -------- | --------------- | ------ |
| Discovery | `GET /.well-known/openid-configuration`, `GET /.well-known/oauth-authorization-server` | Anyone. Public, CORS-open, no credentials |
| Key set | `GET /keys` | Anyone. Public |
| Authorization | `GET /authorize` (PKCE `S256` only, `response_mode=query`, `resource`, `prompt`) | A browser |
| Sign-in | `GET /login`, `GET /login/<provider>/start`, `GET /authorize/callback` | A browser, bound to the request by a `__Host-` cookie |
| Token | `POST /token` with `authorization_code` or `refresh_token` | A client, public with PKCE or confidential with a secret. Refresh tokens are single use |
| UserInfo | `GET\|POST /userinfo` | A bearer of a live sign-in |
| Revocation | `POST /revoke` (RFC 7009) | The client the token was issued to |
| Sign-out | `GET\|POST /end_session`, `GET /signed-out`; back-channel logout to relying parties | A browser; the relying party |
| Recovery | `POST /login/recovery`, and the SRP-6a password flow under `/login/srp/` | An operator, when no upstream can vouch. The proof needs no Kubernetes |
| Agent consent | `POST /login/agent-consent` | A signed-in person authorizing an agent-class session |

### Token exchange and mint

Both are `POST /token` with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange` (RFC 8693). The `subject_token` is a GitHub Actions token, a Kubernetes ServiceAccount token from any known cluster, an AWS web-identity token, a token from a peer sluis, or the access token of a live sign-in presented by a client that is allowed to re-audience it. The `actor_token` is refused.

| Result | Selected by | Notes |
| ------ | ----------- | ----- |
| An access token for another audience | `audience` | Never an ID token. A refusal looks like an unknown audience; the reason is only in the audit record |
| A GitHub installation token | `requested_token_type=urn:sluis:params:oauth:token-type:github-installation-token`, `audience=github-app:<id>`, `repositories`, `scope` | The first grant that covers the whole request wins. Grants are never added together |
| A cloud API token or object-store credential | `requested_token_type=urn:sluis:params:oauth:token-type:cloudflare-token`, `audience=cloudflare:<preset>`, `lifetime` | Caps and rate limits are checked in core before the minter is asked |
| Cloud credentials for a person or robot | `audience` naming a cloud role | The cloud accepts a short-lived token that sluis signed, and returns its own credentials |

`GET /.access/grants` answers "what do I hold" for a bearer of a live sign-in; `Explain` answers "why".

### Console API

Connect services (JSON or protobuf over HTTP) under the console mount, package `sluis.v1`. Console calls use the same-origin session cookie; a workload may call them with its own platform token; some services also accept a CLI bearer. Roles are viewer and operator, optionally scoped to one directory.

| Service | What it manages |
| ------- | --------------- |
| `SessionService` | List and revoke sessions, own or anyone's |
| `AccessService` | `WhoAmI`, `Explain`, the effective policy, recovery status, group search |
| `WorkspaceService` and one service per integration | Connect, reconnect, probe, refresh and disconnect directories, GitHub organizations and Apps, chat workspaces |
| `CloudflareService` | List, rotate and revoke minted credential presets |
| `BackupService` | Backup status and history, run a backup, preview a restore |

Browser redirect routes under `/connect/<integration>/` complete the provider flows.

### Audit read

`audit.v1.QueryService` is reached only through the console, which forwards the call with a five-minute token minted for the signed-in person and addressed to the audit install. `401` means signed out, `403` means no grant, `502` means unreachable. Independent verification does not call sluis: an auditor fetches the sealed batches and the notary's public key and checks the chain.

### Operator and automation

| Surface | Crosses the boundary as |
| ------- | ----------------------- |
| `sluis` CLI, login, token, exchange, credential helpers for kubeconfig and cloud SDKs | The endpoints above, with results cached under the user's config directory |
| `sluis config check`, `sluis config explain` | Offline. No call to sluis |
| `sluis backup`, `restore`, `migrate`, `doctor` | On the host, against state and secrets directly |
| Scheduled work (directory refresh, reconcile ticks, rotation, backup) | Events from a timer, a queue, or a platform scheduler, not user API |

### Second factor

sluis relies on the upstream identity provider's multi-factor authentication for now. A person signs in at the upstream provider, and sluis takes the provider's result as a fact. Passkeys, including a step-up inside sluis for high-privilege roles, are a later roadmap feature.

### Added in v2

:::note[Proposed]
The operations below are new in v2. Their paths and RPC names are proposed and may change before they are built.

| Operation | Shape |
| --------- | ----- |
| Device login (RFC 8628) | `POST /device_authorization`, `GET /device`, approve and deny calls, and `grant_type=urn:ietf:params:oauth:grant-type:device_code` on `/token`. The verification page requires a session that the upstream provider authenticated with MFA, and shows the client, host, IP address and location |
| Certificates for workloads, people and hosts | A Connect call for cert-manager's external-issuer library, and `sluis pki` and `sluis ssh` for people and robots |
| Static publication | `GET` of CA certificates, CRLs, bundles, SSH CA keys and the key revocation list, public |
| Audit receiver | `SinkService.Send` served by sluis, so other installs can send records |
| Quota | Admit, emit, cancel and top-up calls for metered operations |
| Audit witness protocol | A checkpoint goes out and a cosignature comes back |
:::

## Resources each use case touches

A use case touches resources of eight classes: a **signing key**, **state** (records), a **blob**, a **transport**, the **audit sink**, **secrets**, **config** (policy and clients), and **external** systems. The matrix lists the operations. `r` marks an operation on the request path (the caller waits); `b` marks one in the background.

Operations: `sign` and `verify` (public key only), `wrap` and `unwrap`, `read`, `list`, `create` (if absent), `cas` (compare and swap on a revision), `put`, `append`, `delete`, `put` on the transport and `receive`, `fetch` (external read), `call` (external write).

| Use case | Key | State | Blob | Transport | Audit | Secret | Config | External |
| -------- | --- | ----- | ---- | ----- | ----- | ------ | ------ | -------- |
| Sign in through an upstream | sign r | cas, create, read r | | | put b | read r | read r | fetch r |
| Device login | sign r | cas, create, read r and b | | | put b | | read r | call b, fetch r |
| Refresh, UserInfo, revoke, sign out, sessions | sign, verify r | cas, create, delete, list, read r | | | put b | | read r | call b |
| Agent sessions and resources | sign r | create r | | | put b | | read r | fetch r |
| Recovery sign-in | sign r | cas, create, read r | | | put r, spooled locally | read r | read r | call r |
| Token exchange from a platform token | sign, verify r | create, read r | | | put b | | read r | fetch r |
| Mint a GitHub installation token | sign r | append, read r | | | put b | | read r | call r |
| Mint a cloud API token on demand | | cas, read r | | | put b | read r | read r | call r, fetch r |
| Rotate stored credentials on a schedule | | cas, create, delete, read b | | receive b | put b | put, read b | read b | call b |
| Issue certificates | sign r | read r | put b | | put b | | read r | |
| Refresh directory facts, reconcile teams and channels | sign b | cas, create, list, read b | put b, read r | receive b | put b | read b | read b | call b, fetch b and r |
| Connect an integration | put r | cas, create r | | | put b | put r | | call r |
| Resolve grants, answer "what do I hold" | verify r | read r | | | put b | | read r | |
| Deliver policy and configuration | | | | | | | read b | |
| Rotate a client secret | verify r | | | receive b | put b | cas, create, read r | read r | |
| Emit, seal and witness audit batches | sign b | append, cas, read b | put b | put, receive b | call b | | | call b |
| Query and verify the audit trail | | read r | read r | | put b | | read r | |
| Console reads and actions | | cas, read r | read r | | put b | | read r | |
| Back up, restore, migrate | | cas, create, list, put b | list, put, read b | receive b | call, put b | cas, list, put, read b | | |
| Rotate signing keys, key ceremonies | sign, wrap, unwrap b | create, list b | | | | | | |
| Discovery and offline verification | | list r | | | | | read r | |
| Install, bootstrap, health | | create, read b | | | call b | read b | read b | call b |
| Admit a metered operation | | cas r | | | put b | | | |

Four things follow from the matrix, and they shape L2 and L3:

- **Every sign-in-side use case is on the request path of state and of a signing key.** Nothing in that group touches a blob; the transport is used only to emit the audit record. The strongest consistency need is state: a single-use code, a refresh token and a revocation must each be linearizable.
- **Downstream minting puts one to three external round trips on the caller's request path.** The decision and the credential-holding call are different credential holders, so they are different actors.
- **Controllers, refreshes and backups are background only.** They hold the broadest credentials and never need a listener.
- **Recovery is the one request-path use case that must not wait on audit.** It spools its audit record locally and delivers it later, so a down audit install does not shut the emergency door.

## Two sequences

sluis is a single participant in both flows. Resources are named by class. Each flow is drawn in two steps.

### A CI job exchanges its token

Step 1: sluis verifies the job's token and checks the policy.

```mermaid
sequenceDiagram
  autonumber
  participant J as CI job
  participant S as sluis
  participant X as platform key set
  participant C as policy config
  J->>S: POST /token (exchange)
  S->>X: fetch key set, verify
  S->>C: match groups, check limits
```

The request is a token exchange with a `subject_token` and an `audience`. sluis fetches and caches the platform's key set, then verifies the signature, the expiry and the audience. It matches the owner, repository and ref to groups and checks the exchange limits.

Step 2: sluis records the exchange, signs the token and answers.

```mermaid
sequenceDiagram
  autonumber 4
  participant J as CI job
  participant S as sluis
  participant ST as session state
  participant K as token signing key
  participant A as audit sink
  S->>ST: create session record
  S->>K: sign access token
  S--)A: put the exchange record on the transport
  S-->>J: token or refusal
```

The session record is the exchange session. The audit record covers success and refusal. The answer is an access token, or a refusal in fixed words plus a request id.

No client secret is involved: the proof is the job's own token. A refused exchange answers like an unknown audience, so the caller learns nothing; the reason is only in the audit record.

### A downstream credential is minted

Step 1: sluis verifies the proof and checks the grant and the caps.

```mermaid
sequenceDiagram
  participant J as Caller
  participant S as sluis
  participant C as policy config
  participant ST as minted state
  participant A as audit sink
  J->>S: POST /token exchange
  S->>S: verify the proof
  S->>C: does a grant cover it
  S->>ST: read live credentials
  alt grant missing or cap reached
    S--)A: put the refusal record on the transport
    S-->>J: access_denied
  end
```

The request is a token exchange with a `requested_token_type`, an `audience` and a `scope`. The proof is verified as above. The grant check is by group, audience and scope. The read of the caller's live credentials fails closed.

Step 2: within the grant and the caps, sluis mints the credential.

```mermaid
sequenceDiagram
  participant J as Caller
  participant S as sluis
  participant SE as secret
  participant V as vendor
  participant ST as state
  participant A as audit
  S->>SE: read the credential
  S->>V: create credential
  S->>ST: record id
  S--)A: put the mint record on the transport
  S-->>J: credential, once
```

The secret is the minter credential, and the vendor is the downstream API. The credential is created narrowed to the grant. The id is recorded with a compare-and-swap (cas). The audit record never holds the value. The answer carries the credential and its expiry, shown once.

At L2 this one participant becomes several: the decision is made by the `policy` actor, and the call that holds the vendor credential is made by an integration that only carries out what core has authorized. See [Components](/architecture/components/).

Next: [Components](/architecture/components/) opens the box. [Environments](/environments/) shows the same box running in each environment.
