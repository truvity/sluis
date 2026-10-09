# Contributing

## Layout

The repository holds two products, **sluis** and **audit**, and the shared **storage** module under one tag.
[Artifacts](docs/reference/sluis/artifacts.md) lists every deliverable.

```
cmd/sluis           the one binary and image: `serve` and `migrate`
cmd/sluisctl        the CLI for laptops and CI jobs
cmd/resource-proxy  the sidecar that fronts a stock MCP server
cmd/acceptance      the acceptance runner against a kind cluster
charts/             the sluis and audit charts
deploy/pulumi       the AWS infrastructure as a Pulumi Go library, a module of its own
action.yml          the GitHub Action, at the root so `uses: truvity/sluis@<tag>` works
identity/ tokens/ policy/ backend/
                    the public Go packages: verifiers, middleware, encoders, policy, directory backend
internal/           hub, issuer, server, verify, the controllers, rails, the audit catalogue and emitter, demo fixtures
hack/               the scripts the recipes call
frontend/           the console: Vite, React and MUI, embedded into the binary by go:embed
ts/                 the TypeScript package, published to GitHub Packages by the release
proto/  gen/        contracts and committed generated code
audit/              the audit product, a Go module of its own
storage/            the state and keys module both products use
docs/               see docs/WRITING.md
```

`serve` runs the whole service in one process: [one process](docs/concepts/sluis/one-process.md), [ADR 0037](docs/decisions/0037-one-process-everywhere.md).
Public Go packages stay free of Kubernetes and framework specifics outside the adapters and store implementations.
Anything a product might import lives behind a storage interface.

## This repository is public

The repository holds mechanism only.
Nothing names a real organisation, account, zone, hostname, cluster, issuer, team, person, incident, ticket or secret path.
Every such value is a neutral example (`example.com`, `acme`, `globex`), and the installation supplies the real one from its own repository.
The rule covers code, docs, the CHANGELOG, tests, commit messages and pull request text.

[`hack/leak-canary.sh`](hack/leak-canary.sh) enforces it in `just check` and in CI.
The repository follows the shared [component contract](https://github.com/truvity/policy/blob/master/docs/contracts/component.md) and [documentation contract](https://github.com/truvity/policy/blob/master/docs/contracts/docs.md).

## Toolchain

Run `devbox shell`, or use direnv with the shipped `.envrc`. It pins Go, buf, golangci-lint, helm, just and lefthook.
Install no tool by hand beside it.

`just check` runs what CI runs, and `just --list` names the parts. The pre-push hook runs the same.
`just vuln` is not part of `check`, so a newly published CVE does not turn an unrelated PR red. Run it on its own.

## Documentation

Every page is a tutorial, a how-to, a reference or an explanation. [docs/WRITING.md](docs/WRITING.md) holds the standard, the skeletons and the limits.
`just docs-check` enforces them, the relative links and the retired product names.
The retired names stay out of the docs except where `hack/docs-hygiene-allow.tsv` lists an identifier that keeps its old name.

## Conventions

- Write conventional commits: `feat:`, `fix:`, `docs:`, `chore:`. The history feeds each GitHub Release's notes.

- Write `CHANGELOG.md` by hand for the consumer. A pull request that changes what a release user notices adds a bullet under `## vX.Y.Z`, the version it will be tagged as.

- Start a breaking bullet with **Breaking:** and say what to do first.

- Give a patch with a user-visible change its heading in the pull request. A patch that only bumps dependencies needs none.

- Merge by rebase only. Branch from `master` and do not stack pull requests.

- Commit generated code. `just generate` rebuilds `gen/` from `proto/`, CI does not run buf, and a contract change and its generated code land in one commit.

- Keep contracts additive. `buf breaking` guards `proto/`: add a field, never renumber or remove one.

- Keep the chart `version` at `0.0.0`. The release workflow stamps the tag at package time.

- Change the audit catalogue only with a new catalogue `version` and its `testdata/released/` fixture in the same pull request. See [extend](docs/guides/sluis/extend.md#7-an-audit-action).

### Logging

Pass typed attributes to every `slog` call: `slog.String`, `slog.Int`, `slog.Duration`, `slog.Bool`, `slog.Time` or `slog.Group`.
Use `slog.Any` only where no typed constructor fits, and never alternate `"key", value` pairs.
Keep the message constant, put the variable part in an attribute, use `snake_case` keys and the `*Context` variants.
`sloglint` enforces this through `.golangci.yaml` and `audit/.golangci.yaml`.

`sloglint` cannot tell a trusted value from an untrusted one, so review enforces that part.
Log a value from a request, a token claim, a caller-supplied name or an external API error with `logattr.SafeString`, `logattr.SafeStrings` or `logattr.SafeError` from [`storage/logattr`](storage/logattr/logattr.go).
Plain `slog.String` stays for values you own: configuration, constants and ids you generated.

Assert on log records, not printed text, with the capturing handler in [`storage/logtest`](storage/logtest/logtest.go).
Ship any custom handler with its own `slogtest.Run` test.

## Working on the console

The console is a single-page app embedded in the binary. Run three builds in order; skipping one is the usual mistake:

```sh
just generate        # proto to gen/ (Go) and frontend/src/gen (TS)
just console         # ts/dist first, then frontend/dist; built, never committed
go build ./cmd/sluis # embeds frontend/dist
```

A running `go run` keeps the bundle it started with, so restart it after a frontend build.
To see every mechanic without a credential, run the demonstration from the [README](README.md#try-it-without-a-credential).

The demonstration adopts two tenants from `internal/demo`, and one account is suspended to show a leaver.
It cannot sign anyone in at the issuer, because that needs a real corporate OAuth client.
The tests exercise the code flow and the token exchange instead.

Follow the console rules in [design](docs/concepts/sluis/design.md) under "The console".
Write new pages against the vocabulary in `frontend/src/ui.tsx`.
For screenshots, use headless Chrome with a fresh `--user-data-dir` each time, and read the DOM rather than the pixels for anything animated.

## Ports

The store and runtime use stable ports: [ports](docs/concepts/sluis/ports.md), [ADR 0026](docs/decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md) to [ADR 0032](docs/decisions/0032-one-configuration-file-one-binary-one-chart.md).

- Read and write state only through the ports. Add no ConfigMap or Secret writes and no Valkey keys.
- Add configuration to the configuration file, not to new environment variables.
- Write an ADR first when a change cannot follow these rules.

## Where to start reading

The documents are the authority. When a document and a pull request disagree, the document wins and the pull request gets a comment.
Read in this order:

1. [trust](docs/concepts/sluis/trust.md): two trust anchors chosen by scope, and `groups` as the one vocabulary.
2. [service to service](docs/guides/sluis/connect/service-to-service.md): the how-to that follows from trust.
3. [integrations](docs/concepts/sluis/integrations.md): every case with its anchor.
4. The design of whatever you touch.

Read the newest [CHANGELOG](CHANGELOG.md) entries before the design documents, because the documents describe the shape and not the latest release.
The [conformance findings](docs/concepts/sluis/conformance-findings.md) record the run at 1.0.

Do not reach for these. Each was the first idea and the wrong one:

- raising the gateway's route timeout;

- asking for a fifth Google scope;

- answering a browser with a 5xx it never sees;

- clearing the console's cookie to end a session the proxy holds;

- putting an exchange in front of a same-cluster call;

- verifying another cluster's key set directly;

- minting a structured roles claim beside `groups`;

- re-mapping group names in a library;

- a ConfigMap watch in place of a `checksum/policy` rollout.

Group naming has its own anti-patterns in [taxonomy](docs/reference/sluis/taxonomy.md).

## Releasing

Releases are cut by hand. To cut `vX.Y.Z` or a pre-release `vX.Y.Z-rc.1`:

1. On an up-to-date `master`, run `just release-pin vX.Y.Z`. It pins every `go.mod` require of this repository's modules to the release and keeps the `replace` lines.
2. Commit as `chore: release vX.Y.Z`, merge, and push the tag `git tag vX.Y.Z` on that commit.
3. The workflow publishes everything in [artifacts](docs/reference/sluis/artifacts.md). `just release-check vX.Y.Z` rehearses it.

The `gate` job runs `hack/modules.py check vX.Y.Z --release` first and fails the release when a `go.mod` is unpinned.
Pin all modules together: pinning one alone breaks the others.

The workflow builds sluis through the shared `release-public` workflow, then the audit job (`audit/.goreleaser.yaml`, `just audit-release`), the TypeScript packages, and last the `modules` job.
The `modules` job tags every other Go module as `<dir>/vX.Y.Z`, dependencies first (`hack/tag-modules.sh`).
It builds each module as a consumer would, with the `replace` dropped and `GOPROXY=direct`, before its tag exists (`hack/build-as-consumer.sh`).
`hack/modules.py list` names the modules, and a new `go.mod` joins the chain without a workflow edit.

The job pushes tags with an installation token of the catalogue App `truvity-ci-automation`, narrowed to this repository and `contents: write`.
The token comes from `ci-actions/token-exchange` and the repository variable `ACCESS_ROSTER_ISSUER` (legacy identifier, renamed in v1.75–v1.76), with no key or secret.
Without the variable the job falls back to `GITHUB_TOKEN`.
The issuer must grant `release.yaml` a token of that App for this repository.
A tag ruleset can name an App as bypass but not GitHub Actions. Root `v*` tags stay pushed by hand under the team-gated `release-tags` ruleset.

The job refuses a module tag pushed by hand unless it is on the release commit, because a proxy cannot take a fetched tag back.
Delete a wrong tag before any proxy fetches it. After a fetch, cut the next version.

Do not cut a patch while the CHANGELOG entries after the newest release contain `**Breaking:`. `hack/check-no-breaking-patch.sh CHANGELOG.md` checks it, and a breaking change ships as the next minor.
Nothing releases automatically, including security bumps. Cut a patch by hand with `just release-pin`, a commit and a tag.

The release carries two checksummed bundles beside the binaries:

- `sluis-config-schemas_<version>.tar.gz`: the JSON Schemas in `schemas/config/*.schema.json`.

- `sluis-audit-catalogue_<version>.tar.gz`: the catalogue and every schema it references. The audit writer refuses to start without them.

`internal/releasecheck` holds both bundles to their sources.

## Audit

audit lives in this repository. Its recipes are the root Justfile's `audit-*` recipes: `just audit-check` is the gate, and a page that says `just X` for audit means `just audit-X`.
Its tools come from the root `devbox.json`, and its CI is the `audit*` jobs of `ci.yaml`.
audit never imports sluis. Both share the `storage` module, which imports nothing of sluis.

- A compliance bundle is a **profile**. The files in `audit/profiles/` are *framework profiles*: they cite the clause they implement and state that they are an engineering reading, not legal advice.

- Update the matching reference page when you change `audit/proto/`, `audit/schemas/` or `audit/profiles/`. Add a decision record when the change is not additive. A decision for one deployment only is not recorded here.

- `just audit-check` needs only this checkout. CI runs the heavier checks as separate jobs:

  - `just audit-race` needs a C toolchain. Run it before changing code that hands a record to a background goroutine, because a race there loses a record.

  - `just audit-drift-ts` regenerates TypeScript from a rate-limited remote plugin. `just audit-drift` checks Go and JSON Schema with local plugins and runs in the gate.

  - `just audit-conformance` runs every test that skips without PostgreSQL, S3 with object locking (LocalStack) or an OpenBao dev server. It needs Docker.

  - `just audit-ts` installs dependencies, typechecks, tests, builds, and checks what a publish ships.

  - To test against real S3 on demand, run `AUDIT_S3_REAL_BUCKET=<bucket> go test ./internal/s3test -run RealBucket` from `audit/`. It checks that a lock lengthens and compliance mode refuses to shorten it, which no emulator implements. Use an Object-Locked sandbox bucket: each run leaves one small object locked for two days.
