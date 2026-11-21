---
title: AWS and Cloudflare
description: Two clouds, two shapes. AWS federates and needs no minter; Cloudflare cannot accept an external issuer, so sluis mints for it. Each has its own page.
status: target
sidebar:
  order: 5
  hidden: true
---

Two clouds, two shapes. **AWS** federates: it trusts sluis as an OpenID Connect issuer and takes its tokens, so sluis stores nothing of AWS and mints nothing for it. **Cloudflare** cannot accept an external issuer for API tokens, so sluis mints for it, from a tenant's own connection, for declared targets only ([0025](/decisions/0025-integrations/#credential-minters)).

| | AWS | Cloudflare |
|---|---|---|
| Kind | `WorkloadVerifier`, plus accounts that trust sluis | `CredentialMinter` |
| Stored in sluis | Nothing | A `MinterConnection` of the tenant (the minting credential, sealed) and its `MinterTarget` items |
| Bounded by | The roles each account's trust policy allows | `MinterBounds` (config) and the grant that `policy` sealed |

## AWS

Accounts trust sluis as a web identity provider, AWS roles prove themselves to sluis, and sluis's own actors assume roles. The page is [AWS](/integrations/aws/).

## Cloudflare

A minting token and reviewed prototype tokens let sluis hand out expiring DNS and R2 credentials. The page is [Cloudflare](/integrations/cloudflare/).
