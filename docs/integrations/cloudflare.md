---
title: Cloudflare
description: "How sluis mints short-lived Cloudflare API tokens and R2 credentials from a tenant's connection, for declared targets only."
status: target
sidebar:
  order: 6
---

Cloudflare is a **credential minter**. It cannot accept an external issuer for API tokens, so sluis mints for it, from a tenant's own connection, for declared targets only ([0025](/decisions/0025-integrations/#credential-minters)). A tenant's Cloudflare account is a connection that holds one account-level token able to create other tokens, sealed. The Cloudflare adapter opens it at key@ for a mint, and hands out expiring tokens, each cloned from a **prototype** token that a person reviewed. What it hands out is never recorded. AWS, which federates and needs no minter, is on [its own page](/integrations/aws/).

The adapter belongs to the Cloudflare add-on, built on the shared Cloudflare client, and the core holds no Cloudflare credential ([0044](/decisions/0044-sluis-is-a-core-and-add-ons/#the-add-ons)). The add-on is planned.

## What Cloudflare is for

- **A release job updates DNS for one zone.** It exchanges its CI identity for a token that can edit DNS on that zone only and expires within the hour.
- **A job writes to an object-store bucket.** It receives R2 credentials scoped to one bucket and, if you want, one prefix, in the form an S3 client expects.
- **An engineer makes an ad hoc change.** They sign in, ask for a target, and get a token that is gone by the end of the day, instead of a long-lived token in a password manager.
- **Infrastructure-as-code runs mint on demand.** Each run asks for its own token; nothing is kept. The adapter sweeps the tokens it minted by listing them at Cloudflare: those under the name prefix that sluis gives its tokens and past their expiry are removed, so the sweep needs no record of a token.

## Kinds of Cloudflare integration

| Kind | What it does for Cloudflare |
|---|---|
| `CredentialMinter` | Opens a mint grant sealed by `policy`, checks the target, creates a token by cloning the prototype with an expiry, and sweeps expired tokens by their name prefix and expiry at the vendor. |

That is the only kind. The adapter is separate from the exporter's Cloudflare bucket driver, which mints R2 temporary credentials for sluis's own buckets with the boundary's parent token; the two share a library but not a credential ([0013](/decisions/0013-release-train-packaging-and-distribution/#packaging-per-runtime), [0009](/decisions/0009-export/#destinations)).

## Connections and targets

| Kind | Class | Holds |
|---|---|---|
| `MinterBounds` | Config | The target types an installation allows a minter to mint (a cloud role, a prototype token, a DNS zone) and the longest lifetime of each |
| `MinterConnection` | Durable, per tenant, sealed | The tenant's Cloudflare account and the minting token |
| `MinterTarget` | Durable, tenant-adjustable, plain | One thing the tenant may mint: a prototype token or a DNS zone, with the lifetime asked for |

- **A tenant declares its targets inside the bounds.** The store refuses a target outside `MinterBounds`, and a request for a target that is not declared is refused with `NOT_FOUND`.
- **An IaC tool that creates a target through the API** makes durable data of the tenant, not config.
- **A minter whose "tenant" is not a sluis tenant,** such as the NATS operator, keeps its credential as the secret kind `MinterSecret`, bounded by `MinterBounds` ([0025](/decisions/0025-integrations/#credential-minters)).

## What sluis asks Cloudflare for

| Token | Held by | Permissions |
|---|---|---|
| **The minter token** | The tenant's `MinterConnection`, sealed | The account's API-token management permission, read and write, nothing else. |
| **A prototype token, one per target** | Cloudflare only; its value is never read | The rights a minted token should have, such as *DNS write* on one zone or *Bucket item write* on one bucket. Created, then **disabled**, so it is a template. |

**The minter is the sharpest credential in the integration.** A token that can create tokens can create anything the account owner can, and Cloudflare does not let you cap that on the token (checked against the live API in October 2026). The protection is on sluis's side:

- The adapter refuses a prototype that is active, missing, or grants token administration, billing, account settings, memberships or access-identity rights. This refusal list is a floor in code: configuration can add names, not remove them.
- A target can list the permission groups its prototype may hold (`allowed_permission_groups`); anything else is refused. Prefer setting it.
- The minter credential is sealed at rest, opened only by the adapter for one call, and never leaves it.

Cloudflare's own limits apply: about 1,200 API requests per five minutes per user, and 500 tokens per account.

## What policy controls

- **Who may mint which target.** A grant maps a group to targets. Pin a CI job by the verified attributes of its identity (repository, ref, workflow file), not by a workflow name.
- **How much.** Live tokens per caller and per target, and mints per caller per hour, are enforced by `policy` and the limit check on the request path **before** the adapter is called ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)). The adapter enforces only Cloudflare's limits.
- **The caps are counted from sluis's own record of its mints, never from Cloudflare's list** ([REQ-EXCH-022](/requirements/exch/#REQ-EXCH-022)). Listing every token of an account on each mint made a mint cost several seconds in v1. The record holds, for each mint, the token's identifier, a digest of the caller, the target and the expiry, and **nothing else**: never the credential or its value, the account or a token name ([REQ-EXCH-026](/requirements/exch/#REQ-EXCH-026), [REQ-EXCH-016](/requirements/exch/#REQ-EXCH-016)). It is one item for each mint, which the store expires on its own, with an index on the caller's digest. A mint whose record cannot be read fails with `UNAVAILABLE` ([REQ-EXCH-023](/requirements/exch/#REQ-EXCH-023)).
- **The record is checked against Cloudflare at a fixed interval.** A crash between creating a token and writing its record leaves a live token that no cap counts. A periodic pass lists the account once, records what the record lacks, forgets an entry for a token that Cloudflare confirms is gone, and counts each correction in a metric ([REQ-EXCH-024](/requirements/exch/#REQ-EXCH-024), [REQ-EXCH-025](/requirements/exch/#REQ-EXCH-025)). The caps are soft between two passes and across replicas, and the pass is where that error ends.
- **A kind with a bound counts every field it writes** ([REQ-DATA-111](/requirements/data/#REQ-DATA-111)).
- **The lifetime** per target, under the longest lifetime that `MinterBounds` allows.

A token request is a standard token exchange with `audience=cloudflare:<target>`.

## What is audited

- Each mint: the target, the caller, the grant and the expiry. **The token value is never recorded.**
- Each refusal and its reason: not granted, target refused, cap exceeded, rate limited.
- Each connection made, changed or disconnected, without its credential.

## Setup

The minting token and the prototype tokens are made by hand ([the setup pattern](/integrations/connecting/#cloudflare-the-minting-token)). Names and keys are [illustrative](/integrations/#examples-are-illustrative).

1. Create the minter token with token-management permission only, and enter it through the console or the API, or from standard input: `sluis integrations connect cloudflare --account main`.
2. Create each prototype token with the rights a minted token should have, then disable it.
3. Declare the targets, and write the grants in the policy.

```yaml
# MinterTarget items of a tenant, written through the API (example)
targets:
  - id: dns-example
    prototype: <prototype-token-id>
    allowed_permission_groups: ["DNS Write"]
    lifetime: 1h
  - id: r2-archive
    prototype: <r2-prototype-token-id>
    endpoint: https://<account-id>.r2.cloudflarestorage.com
    lifetime: 1h
```

```text
$ sluis cloudflare token dns-example
CLOUDFLARE_API_TOKEN=...
```
