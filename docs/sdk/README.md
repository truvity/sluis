# SDKs

The libraries a program imports to use sluis and audit: verify who is calling, call the next service as yourself,
record what the program does, and read the audit trail. One version for everything: a release `vX.Y.Z` publishes every
package below at `X.Y.Z`.

**Built today: Go and TypeScript.** Kotlin and Python are planned
([ADR 0042](../decisions/0042-one-repository-one-release-train.md) §10) and have no code and no pages.

## The matrix

| SDK | Go | TypeScript | Kotlin | Python |
|---|---|---|---|---|
| **sluis client**: verify a caller, exchange a token, evaluate policy | built: [Go module](go/sluis.md) | built: [`@truvity/sluis`](typescript/sluis.md) | planned (ADR 0042 §10) | planned (ADR 0042 §10) |
| **audit emitter**: record what a program does | built: [Go audit emitter](go/audit-emitter.md) | planned (ADR 0042 §10) | planned | planned |
| **audit query**: read the trail | built: [Go audit query](go/audit-query.md) | built: [`@truvity/audit`](typescript/audit.md) | planned | planned |
| **audit React view**: hooks and a default view | not applicable | built: [`@truvity/audit-react`](typescript/audit-react.md) | not applicable | not applicable |

| Language | Channel | Install |
|---|---|---|
| Go | the module proxy, by tag | `go get github.com/truvity/sluis@vX.Y.Z` (sluis client); `go get github.com/truvity/sluis/audit/sdk@vX.Y.Z` (audit emitter and query) |
| TypeScript | GitHub Packages (npm registry) | `yarn add @truvity/sluis @truvity/audit @truvity/audit-react` after [the scope block](#installing-from-github-packages) |
| Kotlin | planned: Maven on GitHub Packages | planned, ADR 0042 §10 |
| Python | planned: wheels on the GitHub release | planned, ADR 0042 §10 |

## Not libraries, but how a program uses sluis

| What | Where | Reference |
|---|---|---|
| The GitHub Action | `uses: truvity/sluis@<tag>` in a job | [GitHub Actions](../guides/sluis/connect/github-actions.md) |
| `sluisctl` | the command line for sluis | [sluisctl](../reference/sluis/sluisctl.md) |
| `audit` | the command line for audit, including `audit verify` | [verify command](../reference/audit/verify-command.md) |

## Installing from GitHub Packages

GitHub's npm registry needs a token to install, even a public package: one with `read:packages` (a job's
`GITHUB_TOKEN` with `packages: read`, or `gh auth token` after `gh auth refresh -s read:packages` on a laptop). Point
the `@truvity` scope at it, once, for every TypeScript package on this page:

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

A git install (`github:truvity/sluis#<tag>`) does not work: the built files are not committed and nothing builds them
on install.

## Pick a page

- A service that must know who is calling: [Go module](go/sluis.md) or [`@truvity/sluis`](typescript/sluis.md).
- An application that must record what it does: [Go audit emitter](go/audit-emitter.md), walked through in
  [emit records from Go](../guides/audit/connect/emit-records.md).
- A program that reads the trail: [Go audit query](go/audit-query.md) or [`@truvity/audit`](typescript/audit.md); the
  service behind both is described in [read the trail](../guides/audit/connect/read-the-trail.md).
- A console that shows the trail: [`@truvity/audit-react`](typescript/audit-react.md).
