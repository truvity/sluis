# 0069 — Schema identifiers move to the sluis site, the audit Pages base kept as an alias

**Status:** Accepted (2026-10-08). A consequence of [0042](0042-one-repository-one-release-train.md): audit's repository and its
Pages site are retired. Amends [0057](0057-schema-ids-on-github-pages.md).
**Date:** 2026-10-08

## Context

[0057](0057-schema-ids-on-github-pages.md) put audit's schema identifiers under `https://truvity.github.io/audit/`, the
Pages site of the audit repository. audit now lives in this repository ([0042](0042-one-repository-one-release-train.md))
and its repository is archived, so that site has no publisher. sluis's own schemas already name themselves under
`https://truvity.github.io/sluis/schemas/`, and one site for the documentation and every schema is simpler to run than
two.

## Decision

- audit's schemas are published and named under `https://truvity.github.io/sluis/schemas/audit/`, with the same paths
  below that base as before (`v1/record.schema.json`, `v1/common/*.json`, `v2/config/*.schema.json`, and so on). sluis's
  schemas stay where their `$id` already says.
- `catalogue.CanonicalID` maps an id under either earlier base (`https://schemas.truvity.com/audit/v1/`, and
  `https://truvity.github.io/audit/schemas/v1/`) onto the same path under the new one. As in 0057, nothing archived is
  rewritten: a catalogue and schemas written under any of the three forms load, and a mixture of forms loads too.
- `.github/workflows/pages.yaml` builds `docs/` with search and the schemas into one site and deploys it on a push to
  master; `hack/pages-site.sh` fails when a schema's `$id` is not the URL it would be served at, and runs in CI.
- The alias is kept for as long as 0057's is: while any archive written under the old identifiers can be read.

## Consequences

- A validator outside Go fetches a schema from the same site that serves the documentation.
- The old audit Pages site stops being updated. Identifiers that were published under it stay valid as names, because
  the loader maps them; they stop resolving as URLs unless the archived repository's site is left up.
- Publishing needs GitHub Pages enabled for this repository with the source set to GitHub Actions. That is a repository
  setting, not something the workflow can do.
- The site's URL still depends on the repository's name and owner; moving the repository would be a new decision and
  another alias.
