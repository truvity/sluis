## Scope

A named layer of shared, non-core building blocks: the Cloudflare, AWS and GitHub clients and minters that add-ons and drivers reuse. Today each driver or add-on that talks to one of these services would carry its own client, its own retry and error rules, and its own way of holding a credential.

- A code-organisation rule: the shared clients and minters live in one place, outside the core actors. A core actor never imports them. An add-on or a driver imports the layer and not another add-on.
- An amendment to ADR 0044 that names the layer beside the core, the building blocks and the add-ons.
- The per-driver work that exists (the Cloudflare credential driver in #1800, the AWS drivers, the GitHub add-on) reuses the layer instead of carrying its own client.
- The layer holds no credential at rest. A minter asks the credential holder at the time of use.
- Done when the rule is in ADR 0044 and in the contributing rules, a check in CI refuses an import of the layer from a core actor, and the Cloudflare credential driver (#1800) uses the shared Cloudflare client.

Decision: ADR 0044 (amended by this issue). Related: #1800.

## Decision codes

Decision A1 A, 2026-11-18.

## Milestone

Core: features

Linear: mirrored.
