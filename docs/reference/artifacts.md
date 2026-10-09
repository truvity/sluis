# Artifacts

Every artifact of this repository is stamped by one tag, `vX.Y.Z`: pin one version of the repository. The tables below
are generated from the release configuration (`.goreleaser.yaml`, `audit/.goreleaser.yaml`, the release workflow and
`hack/modules.py list`), so they cannot drift from what a release publishes.

<!-- generated: artifacts -->

Release assets (the GitHub release of tag `vX.Y.Z`):

| Product | Asset | Contents |
|---|---|---|
| sluis | `sluisctl_<version>_<os>_<arch>.tar.gz` | sluisctl |
| sluis | `accessctl_<version>_<os>_<arch>.tar.gz` | accessctl |
| sluis | `sluis_<version>_<os>_<arch>.tar.gz` | sluis, resource-proxy, acceptance |
| sluis | `sluis-lambda_<version>_<os>_<arch>.zip` | sluis-lambda |
| sluis | `sluis-config-schemas_<version>.tar.gz` | config-schemas (bundled files) |
| sluis | `sluis-audit-catalogue_<version>.tar.gz` | audit-catalogue (bundled files) |
| audit | `audit_<version>_<os>_<arch>.tar.gz` | audit |
| audit | `audit-writer-lambda_<version>_<os>_<arch>.zip` | audit-writer-lambda |
| audit | `audit-notary-lambda_<version>_<os>_<arch>.zip` | audit-notary-lambda |

Container images:

| Product | Image |
|---|---|
| sluis | `ghcr.io/truvity/sluis/sluis` |
| sluis | `ghcr.io/truvity/sluis/resource-proxy` |
| audit | `ghcr.io/truvity/audit/audit-writer` |
| audit | `ghcr.io/truvity/audit/audit-query` |
| audit | `ghcr.io/truvity/audit/audit-notary` |
| audit | `ghcr.io/truvity/audit/audit-observe` |
| audit | `ghcr.io/truvity/audit/audit` |

Helm charts and Nix flakes:

| Kind | Published at |
|---|---|
| chart | `oci://ghcr.io/truvity/charts/sluis` |
| chart | `oci://ghcr.io/truvity/charts/audit` |
| Nix flake | `sluisctl_<version>_nix-flake.tar.gz` |
| Nix flake | `accessctl_<version>_nix-flake.tar.gz` |
| Nix flake | `audit_<version>_nix-flake.tar.gz` |

Go modules, each tagged `<dir>/vX.Y.Z` beside the root tag `vX.Y.Z`:

| Directory | Import path |
|---|---|
| `.` | `github.com/truvity/sluis` |
| `audit/sdk` | `github.com/truvity/sluis/audit/sdk` |
| `storage` | `github.com/truvity/sluis/storage` |
| `audit` | `github.com/truvity/sluis/audit` |
| `audit/deploy/pulumi` | `github.com/truvity/sluis/audit/deploy/pulumi` |
| `deploy/pulumi` | `github.com/truvity/sluis/deploy/pulumi` |
| `deploy/pulumi/edge/cloudflare` | `github.com/truvity/sluis/deploy/pulumi/edge/cloudflare` |

Packages and actions published by the release workflow:

| Kind | Name | Where |
|---|---|---|
| npm package | `@truvity/sluis` | GitHub Packages |
| npm package | `@truvity/audit` | GitHub Packages |
| GitHub Action | `truvity/sluis@<commit>` | the repository root, `action.yml` |
<!-- /generated -->

What each deliverable is for: sluis is in [the documentation home](../README.md), audit in [the audit docs](../audit/README.md).
SDKs today are Go and TypeScript; Kotlin and Python are planned ([ADR 0042](../decisions/0042-one-repository-one-release-train.md)).
