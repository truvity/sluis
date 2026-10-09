# SDKs

Libraries a program imports to verify a caller, call the next service, record actions and read the audit trail. A release `vX.Y.Z` publishes every package at `X.Y.Z`. Go and TypeScript are built. Kotlin and Python are planned ([ADR 0042](../decisions/0042-one-repository-one-release-train.md) §10) and have no code.

## Matrix

| SDK | Go | TypeScript | Kotlin | Python |
|---|---|---|---|---|
| **sluis client**: verify a caller, exchange a token, evaluate policy | [Go module](go/sluis.md) | [`@truvity/sluis`](typescript/sluis.md) | planned | planned |
| **audit emitter**: record what a program does | [Go audit emitter](go/audit-emitter.md) | planned | planned | planned |
| **audit query**: read the trail | [Go audit query](go/audit-query.md) | [`@truvity/audit`](typescript/audit.md) | planned | planned |
| **audit React view**: hooks and a default view | not applicable | [`@truvity/audit-react`](typescript/audit-react.md) | not applicable | not applicable |

| Language | Channel | Install |
|---|---|---|
| Go | the module proxy, by tag | `go get github.com/truvity/sluis@vX.Y.Z` (sluis client); `go get github.com/truvity/sluis/audit/sdk@vX.Y.Z` (audit emitter and query) |
| TypeScript | GitHub Packages (npm registry) | `yarn add @truvity/sluis @truvity/audit @truvity/audit-react` after [the scope block](#installing-from-github-packages) |
| Kotlin | planned: Maven on GitHub Packages | planned |
| Python | planned: wheels on the GitHub release | planned |

## Other ways to use sluis

| What | Where | Reference |
|---|---|---|
| The GitHub Action | `uses: truvity/sluis@<tag>` in a job | [GitHub Actions](../guides/sluis/connect/github-actions.md) |
| `sluisctl` | the command line for sluis | [sluisctl](../reference/sluis/sluisctl.md) |
| `audit` | the command line for audit, including `audit verify` | [verify command](../reference/audit/verify-command.md) |

## Installing from GitHub Packages

GitHub's npm registry needs a token with `read:packages`, even for a public package. In a job, use `GITHUB_TOKEN` with `packages: read`. On a laptop, run `gh auth refresh -s read:packages`, then `gh auth token`. Point the `@truvity` scope at it once:

```yaml
# .yarnrc.yml (yarn 4)
npmScopes:
  truvity:
    npmRegistryServer: "https://npm.pkg.github.com"
    npmAuthToken: "${GITHUB_PACKAGES_TOKEN}"
```

```ini
# .npmrc (npm)
@truvity:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

A git install (`github:truvity/sluis#<tag>`) does not work: the built files are not committed.
