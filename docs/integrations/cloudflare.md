---
title: Cloudflare
description: How sluis mints short-lived Cloudflare API tokens and R2 credentials by cloning a disabled prototype token, and why the minting token is the sharpest edge.
status: target
sidebar:
  order: 5
---

Cloudflare is a credential minter. sluis holds one account-level token that can create other tokens, and uses it to hand out expiring tokens, each cloned from a **prototype** token that an operator reviewed.

## What it's for

- **A release job updates DNS for one zone.** It exchanges its CI identity for a token that can edit DNS on that zone only, and the token expires within the hour.
- **A job writes to an object-store bucket.** It receives R2 credentials scoped to one bucket and, if you want, one prefix, delivered in the form an S3 client expects.
- **An engineer does an ad hoc change.** They sign in, ask for a preset, and get a token that is gone by the end of the day, in place of a long-lived token in a password manager.
- **Infrastructure-as-code runs mint on demand.** A preset marked on-demand stores nothing; each run mints its own token and the token is swept after it expires.
- **sluis's own release pipeline uploads to its distribution bucket** with a credential minted for the job, with no static key (see [Release signing](/distribution/release-signing/)).

## Integration roles

| Role | What it does for Cloudflare |
|---|---|
| `CredentialMinter` | Receives a mint grant signed by the `policy` role, checks the prototype, creates a token by cloning the prototype with an expiry, and sweeps expired tokens it created. |

That is the only role. Cloudflare supplies no facts and takes no desired state.

## What sluis asks the vendor for

Two kinds of Cloudflare token, with very different weight.

| Token | Held by | Permissions |
|---|---|---|
| **The minter token** | The Cloudflare integration, as a credential. | The account's API-token management permission, read and write, and nothing else. |
| **A prototype token, one per preset** | Cloudflare only; its value is never read. | The rights a minted token should have, such as *DNS write* on one zone, or *Bucket item read or write* on one bucket. Created, then **disabled**, so that it is a template. |

**The minter is the sharpest credential in the integration.** A token that can create tokens can, in practice, create anything the account owner can, and Cloudflare does not let you cap that on the token itself (this was last checked against the live API in October 2026). The protection is therefore on sluis's side:

- sluis refuses a prototype that is active, missing, or that grants token administration, billing, account settings, memberships or access-identity rights.
- A preset can list the permission groups its prototype may hold (`allowed_permission_groups`); anything else is refused. Prefer setting it.
- The minter credential is encrypted at rest, readable only by the integration's own process, and never leaves it. See [Cloudflare in the setup patterns](/integrations/connecting/#cloudflare-the-minting-token).

Cloudflare's own limits apply: about 1,200 API requests per five minutes per user, and 500 tokens per account.

## What policy controls

- **Who may mint which preset.** A grant maps a group to a list of presets. Pin a CI job by the verified attributes of its identity (repository, ref, workflow file), not by a workflow name.
- **How much.** The caps (live tokens per caller and per preset, mints per caller per hour) are enforced by `policy` and `quota` **before** the minter is called. In v1 the minter enforced them itself; in v2 the minter enforces only Cloudflare's own limits.
- **The lifetime**, per preset, and whether the token is stored and rotated for the preset or minted on demand.
- **Refusal list.** The forbidden permission groups are a floor in code. Configuration can add names; it cannot remove them.

## What is audited

- Each mint, with the preset, the caller, the grant and the expiry. The token value is never recorded.
- Each refusal, with its reason: not granted, prototype refused, cap exceeded, rate limited.
- Each sweep of expired tokens, and each deletion of a live token that someone made outside sluis.
- Drift: a token that exists at Cloudflare under sluis's prefix but is not in its record, or the reverse.

## Setup

An example; see [the setup pattern](/integrations/connecting/#cloudflare-the-minting-token).

1. Create the minter token in the Cloudflare dashboard with token-management permission only.
2. Store it as a credential: `sluis credentials put cloudflare.main.minter` reads it from standard input, so it never appears in a shell history.
3. Create each prototype token with the rights a minted token should have, then disable it.
4. Run `sluis integrations connect cloudflare --account main` to record the account, then declare the presets.
5. Write the grants in the policy.

```yaml
# /etc/sluis/install.yaml (example)
integrations:
  cloudflare:
    main:
      roles: [minter]
      account_id: <account-id>
      minter: cred:sluis-int-cloudflare.main.minter
      presets:
        dns-example:
          prototype: <prototype-token-id>
          allowed_permission_groups: ["DNS Write"]
          lifetime: 1h
          mode: on-demand
        r2-archive:
          prototype: <r2-prototype-token-id>
          endpoint: https://<account-id>.r2.cloudflarestorage.com
          lifetime: 1h
          mode: stored
          rotation: 20m
```

A person mints a token like this:

```text
$ sluis cloudflare token dns-example      # example command shape
CLOUDFLARE_API_TOKEN=...
```

## v1 → v2 notes

- v1 does all of this. The minter, the prototype rules and the preset keys are in [Mint short-lived Cloudflare tokens and R2 credentials](/v1/guides/sluis/cloudflare-tokens/); the person's command is `sluisctl cloudflare token <preset>`, and `sluisctl cloudflare r2 <preset>` prints R2 credentials in the format an AWS client's credential process expects.
- The caps and rates move from the minter into core (`policy` and `quota`). The token request is the standard token exchange with `audience=cloudflare:<preset>`.
- v1's separate R2 credential broker is deprecated and has no v2 counterpart.
- Parity issues: [#816](https://github.com/truvity/sluis/issues/816) API tokens, [#817](https://github.com/truvity/sluis/issues/817) R2 credentials, [#829](https://github.com/truvity/sluis/issues/829) the command-line wrappers.

:::note[Proposed]
The commands `sluis credentials put`, `sluis integrations connect` and `sluis cloudflare token` show a shape. The command tree is not yet decided, and every key in the configuration example is illustrative.
:::
