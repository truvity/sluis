# 0035 — Renamed to sluis

**Status:** Accepted; its NATS-bucket and `sluis:binding` rows are moot since [0027](0027-the-state-port-nats-jetstream-and-dynamodb.md) and [0028](0028-nothing-writes-configmaps-or-secrets.md) removed NATS and sealing (2026-10-04); the kept contract names still stand
**Date:** 2026-10-03

## Context

The product was called access-roster, its CLI `accessctl`, and its service
`access-issuer` in the estate's deployments. The owner decided on 2026-10-03 to
call it **sluis** (a lock on a canal: what a caller goes through, one level at a
time). The repository is renamed `truvity/access-roster` to `truvity/sluis` on
GitHub; this change is the code side of it. It is a breaking change inside 1.x
by [ADR 0007](0007-breaking-changes-inside-1x.md): the next release is
**v1.57.0**, not a 2.0.0, and the Go module path carries **no `/v2`** (the path
changes by a rename, not by a major version).

The rule for what moved and what did not is one question: **does something
outside this repository, or something already stored, name it?** What a person
reads moved. What a relying party, a token, a metric, a Kubernetes object or a
laptop's sign-in cache holds stayed, because changing it breaks or silently
orphans that holder, and each of those has its own migration (the observability
names change with the dashboards and alerts, at the B5 step of the plan).

## Decision

### What changed

| Was | Is |
|---|---|
| Go module `github.com/truvity/access-roster` | `github.com/truvity/sluis` (the `deploy/pulumi` module already was `github.com/truvity/sluis/deploy/pulumi`) |
| Images `ghcr.io/truvity/access-roster/access-roster` and `.../resource-proxy` | `ghcr.io/truvity/sluis/sluis` and `ghcr.io/truvity/sluis/resource-proxy` |
| Chart `charts/access-roster`, `oci://ghcr.io/truvity/charts/access-roster` | `charts/sluis`, `oci://ghcr.io/truvity/charts/sluis`; the helpers are `sluis.*` |
| Server binary `access-roster` (`cmd/access-roster`) | `sluis` (`cmd/sluis`); the in-pod config paths are `/etc/sluis/*.yaml` |
| `accessctl`, release assets `accessctl_*` | `sluisctl`, `sluisctl_*`; **`accessctl` is kept as an alias** (below) |
| Release assets `access-roster_<v>_checksums.txt`, `access-roster_<v>_<os>_<arch>` | `sluis_<v>_checksums.txt`, `sluis_...`; the Lambda layer is `sluis-lambda-layer_<v>_linux_<arch>.zip` |
| Acceptance runner `access-roster-acceptance` | `sluis-acceptance` |
| KMS sealing encryption-context key `access-roster:binding` | `sluis:binding` (nothing is sealed anywhere yet, so there is nothing to re-wrap) |
| The default `release` name (the Kubernetes object-name prefix, the Valkey key prefix, the NATS bucket) `access-roster` | `sluis` |
| `$id` of the configuration schemas under `truvity.github.io/access-roster/schemas/...` | `truvity.github.io/sluis/schemas/...` |
| OpenBao names: the export paths `access-roster-backup/<bundle>`, the role `access-roster-writer` and any access-roster-named policy or ESO store | `sluis-backup/<bundle>`, `sluis-writer`, `sluis-*`; neutral paths (`slack-apps/*`, `slack-state/*`, `arc/*`, `github-apps/*`) are unchanged. The estate sets the paths explicitly and renames them in its own change |
| npm package `@truvity/access-roster` | `@truvity/sluis`; the old name is published too for one or two releases (below) |
| The `SLUISCTL_*`/`ACCESSCTL_*` and the Lambda extension's `ACCESS_ROSTER_*` environment | `SLUISCTL_*` and `SLUIS_*`; the old names are read as the fallback, and the new one wins |

The **audit producer identity** needed no change in this repository: records
carry the catalogue source `roster` (which stays), and the installation knows
the workload by its projected ServiceAccount token, not by a name this code
sets. The audit side adds a catalogue alias for `access-roster` in its own
change.

### What deliberately did not change (the contract names)

- The issuer URL and every OIDC client id, `accessctl` included. A client id is
  something a relying party has registered and a person's cached login holds.
- `ACCESS_ROSTER_ISSUER` as a name in the contract, `token-source: access-roster`
  on the GitHub Action, the `access-roster-issuer` input of the shared release
  workflow, and the grants preset `access-roster`.
- The group names `all:access-roster:*`, `<scope>:access-roster:operator|viewer`:
  the *thing* segment of a group (`policy.ThingSelf`, `groups: [access-roster]`)
  is read by every relying party's RBAC.
- The Kubernetes label and annotation keys `access-roster.truvity.github.io/*`.
- The audit source `roster`, and the released catalogue documents (their `$id`
  under `schemas.example/access-roster/...` is part of a document an installation
  already holds, and TestAReleasedCatalogueVersionIsNeverChanged keeps it so).
- Token-exchange URNs (`urn:access-roster:params:oauth:token-type:...`,
  `urn:truvity:access-roster:acr:...`): protocol constants in clients' configs.
- Metric, alert and dashboard names, the instrumentation scope names
  (`github.com/truvity/access-roster/...`) and OTEL `service.name`. They change
  together with the dashboards and alerts, at B5.
- The GitHub App names and ids the product creates (`<org>-access-roster`,
  `...-link`): an installed App is found by them.
- The Lambda extension's file name `extensions/access-roster-otlp`, its log
  prefix and the AWS role-session-name fallback.
- On a laptop: the `accessctl` configuration and cache directory, the
  `accessctl:<cluster>` kubeconfig user names and the `known_hosts.d/accessctl`
  file. Renaming them would sign every person out for no gain.
- `ACCESS_ROSTER_*` variables that only the tests read (`..._REQUIRE_HELM`,
  `..._DYNAMODB_URL`, ...) and the `ACCESS_ROSTER_ISSUER` GitHub variable.

### What a deployment must do

The chart keeps honouring `nameOverride` and `fullnameOverride`, and an
installation that sets them (the estate sets both to `access-issuer`) keeps every
object name and every Deployment selector: rendering the old and the new chart
with the estate's values gives the same objects, differing only in the image
paths, the `# Source:` comments and the in-pod config file path. An installation
that relied on the chart's *defaults* must now say what it relied on:
`fullnameOverride`/`nameOverride` for the object names, and
`config.release: access-roster` for the store prefix, or it starts on an empty
store under the new default.

### The `accessctl` alias

`accessctl` is built from the same source as `sluisctl`, under its old name, in
its own `accessctl_*` archive and Nix flake. It looks at the name it was started
as and, when that is `accessctl`, prints a deprecation notice on **stderr** (the
stdout of a credential helper is parsed by its caller). The alias ships for one
or two releases and is then removed: delete the `accessctl` build, archive and
`nix-flakes` entry in `.goreleaser.yaml` and `release.yaml`.

### The npm package

The package is renamed to **`@truvity/sluis`**. GitHub Packages cannot
`npm deprecate`, so the release workflow publishes the same build a second time
as `@truvity/access-roster` with a description that says it is deprecated, for
one or two releases. A consumer that has not moved keeps working; one that
reads its lockfile sees where to go. Consumers change their dependency and
imports to `@truvity/sluis` (`/server` and `/react` entry points unchanged), and
the second publish is deleted from `release.yaml` when the old name is retired.

## Consequences

Every consumer changes an import path, an image or chart reference, or a
command name, once; the list is in the CHANGELOG entry. Until the GitHub rename
is done the module path `github.com/truvity/sluis` does not resolve, so this
change merges only after the repository is renamed and after the estate's
deployment change that points at the new chart and image paths.

The names that stayed are a debt with a known size: one place per name above.
Retiring any of them is its own change with its own migration, not a find and
replace.

## Alternatives considered

- **A 2.0.0 and a `/v2` module path.** Not chosen: the path changes because the
  repository is renamed, not because the API is incompatible, and
  [ADR 0007](0007-breaking-changes-inside-1x.md) already says a breaking change
  ships in a minor. A `/v2` would make every importer change twice.
- **Keeping the `accessctl` name for good.** Not chosen: a CLI named for the
  old product after the rename is the one inconsistency every new user meets
  first. The alias exists so that nothing already written breaks in the same
  release, not to keep the name.
- **Renaming the persisted names too (client id, groups, labels, metrics).**
  Not chosen for this change: each is held by something outside this
  repository, and each rename is a migration of its own. The observability
  names move with the dashboards and alerts, at B5.
- **Keeping the old chart default `release: access-roster`.** Not chosen: the
  chart requires `config.release` to equal the release's full name, which for a
  chart named `sluis` installed as `sluis` is `sluis`, so an unset value would
  fail the render. An installation that depends on the old default pins it.
