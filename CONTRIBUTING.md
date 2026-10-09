# Contributing

## Layout

Two products, **sluis** and **audit**, plus the shared **storage** module,
in one repository with one tag. Every deliverable is installable or importable
alone; [artifacts](docs/reference/artifacts.md) lists them all.

```
cmd/sluis                 the one binary and image: `serve` (the
                          directory, the policy, the OpenID provider,
                          the login page, the console, what it records
                          to the audit trail, and the GitHub and Slack
                          controllers as loops inside it, ADR 0037 and
                          docs/explanation/one-process.md), and `migrate`
                          (copies the State between storages, ADR 0031)
cmd/resource-proxy        the sidecar that fronts a stock MCP server
                          with a resource server's front door
cmd/sluisctl              the CLI, for laptops and CI jobs
cmd/acceptance            the acceptance runner against a kind cluster
charts/sluis              the chart: the service, with its controllers
deploy/pulumi             the AWS infrastructure as a Pulumi Go library, a
                          Go module of its own
                          (github.com/truvity/sluis/deploy/pulumi,
                          tagged `deploy/pulumi/vX.Y.Z` by the release):
                          storage, the DynamoDB State table and the Pod
                          Identity roles; `just pulumi-test`
action.yml                the GitHub Action, at the root so
                          `uses: truvity/sluis@<tag>` works
identity/ tokens/ policy/ backend/
                          the Go module's public packages: the two
                          verifiers, the net/http middleware and
                          identity/resource (an MCP server's own side), the
                          exchange and credential encoders, the
                          policy with its ten top-level keys, the directory backend
                          interface and its fake
internal/                 hub (snapshots, routing, authority), issuer
                          (the OpenID surface, sessions, exchange),
                          access (roles, sessions, Explain), server
                          (ConnectRPC handlers, HTTP), verify (the
                          proofs), kube (what the console writes),
                          githubroster (the GitHub controller),
                          slackroster (the Slack controller: reconcile,
                          apply, status, connection, controller, app),
                          slackapp (the Slack client, its catalogue Apps
                          and slackfake), githubapp, rails (what the
                          reconcilers share), audit (the catalogue, one
                          constructor per action, and the emitter), demo
                          (fixtures). app and
                          issuerapp assemble the two halves; rosterapp
                          is the wiring that makes them one process
hack/                     the scripts the recipes call
frontend/                 the console: Vite + React + MUI, its built
                          dist/ embedded into the binary by go:embed
ts/                       the TypeScript package; dist/ is built and
                          published to GitHub Packages by the release
proto/  gen/              contracts and committed generated code
audit/                    the audit product, a Go module of its own
                          (see "Audit" below)
storage/                  the state and keys module both products use
docs/                     see "Writing documentation" below
```

Public Go packages stay free of Kubernetes and framework specifics
except in the adapters and the store implementations; anything a product
might import lives behind a storage interface.

## This repository is public

**Mechanism only.** Nothing here names a real organisation, account, zone,
hostname, cluster, issuer, team, person, incident, internal ticket or secret
path of any installation: every such thing is a value with a neutral example
(`example.com`, `acme`, `globex`), and the installation supplies it from its
own repository. The rule covers code, docs, the CHANGELOG, tests, commit
messages and pull request text. [`hack/leak-canary.sh`](hack/leak-canary.sh)
enforces it in `just check` and in CI. This repository follows the shared
[component contract](https://github.com/truvity/policy/blob/master/docs/contracts/component.md)
and the [documentation contract](https://github.com/truvity/policy/blob/master/docs/contracts/docs.md).

## Writing documentation

The reader's map is the documentation home, [docs/README.md](docs/README.md).
This section is for the writer. A page belongs to one directory of its
product's tree (`docs/` for sluis, `docs/audit/` for audit), by what the
reader is doing:

| directory | holds |
|---|---|
| `getting-started/` | one tutorial per deployment shape, from nothing to working |
| `how-to/` | one task per page; a runbook uses one template (purpose, preconditions, before you start, steps with command and expected output, verify and rollback, afterwards); migration steps in `how-to/upgrade/vX.Y.md`, linked from the CHANGELOG |
| `reference/` | configuration keys, chart values, API, catalogue, bucket contract |
| `explanation/` | design and the why |
| `docs/decisions/` | the ADRs, one series for both products; the template and index are `docs/decisions/README.md`; a decision is never edited after acceptance, it is superseded by a new one that links back |

- Keep each fact in one place and link to it from the others.
- Prefer a page under about 400 lines; split by audience, not by length.
- Where reference can be produced from code, a schema or a release file, mark it
  `<!-- generated: name -->` ... `<!-- /generated -->`; `just docs-generate` rewrites it and
  `just docs-check` fails when it is stale. Do not edit inside the markers.
- Documentation is held to the code: every command shown is one the binary takes, every
  chart value named exists, and a relative link or anchor that does not resolve fails the gate.
  When you rename a flag, a value or a heading, search the docs for it in the same change.
- Where the documentation runs ahead of the code, say so where the name is used (`# not built yet`).
- Mermaid diagrams: a `;` inside a sequence diagram message splits it.

## Toolchain

Everything comes from [devbox](https://www.jetify.com/devbox): `devbox shell`
(or direnv with the shipped `.envrc`) gives you Go, buf, golangci-lint,
helm, just and lefthook at the pinned versions. Never install the tools by
hand next to it.

`just check` runs what CI runs; `just --list` names the parts. The pre-push
hook (installed by devbox's init hook) runs the same. `vuln` is deliberately not part of
`check`: a newly published CVE must not turn a PR red that never touched
the dependency; run it on its own with `just vuln`, the same way
`.github/workflows/security.yaml` does.

## Conventions

- **Conventional commits** (`feat:`, `fix:`, `docs:`, `chore:` …). The
  history is the source of each GitHub Release's generated notes.
- **`CHANGELOG.md` is written by hand, for the consumer.** The pull
  request that makes a change someone using a release would notice adds
  its bullet under `## vX.Y.Z`, the version it will be tagged as, creating
  the heading if it is the first. A breaking bullet starts with
  **Breaking:** and says what to do first. A patch tag with a user-visible
  change gets its heading in the pull request, the same as a minor;
  nothing adds it afterwards. A patch cut only for dependency bumps has no heading. A change
  to `internal/audit/catalogue/roster.yaml` needs a new catalogue `version`
  and its `testdata/released/roster-<version>.yaml` fixture in the same pull
  request ([extending.md](docs/how-to/extend.md#7-an-audit-action)).
- **Rebase-merge only.** Branch from `master`, never stack pull requests.
- **Generated code is committed.** `just generate` rebuilds `gen/` from
  `proto/`; CI does not run buf. A contract change and its generated code
  land in the same commit.
- **Contracts are additive.** `buf breaking` guards `proto/`; a field is
  added, never renumbered or removed, so every existing client stays valid.
- **Logging uses typed slog attributes, and untrusted values go through
  `Safe*`.** Every `slog` call passes `slog.String`, `slog.Int`,
  `slog.Duration`, `slog.Bool`, `slog.Time`, `slog.Group` (`slog.Any` only
  where no typed constructor fits), never alternating `"key", value` pairs;
  the message is a constant, the variable part is an attribute, keys are
  `snake_case`, and the `*Context` variants are used. `sloglint` enforces
  this (`.golangci.yaml`, `audit/.golangci.yaml`). It cannot tell a trusted
  value from an untrusted one, so that part is a **review rule**: a value
  that came from a request, a token claim, a caller-supplied name or an
  external API error (error text often quotes its input) is logged with
  `logattr.SafeString`, `logattr.SafeStrings` or `logattr.SafeError` from
  [`storage/logattr`](storage/logattr/logattr.go), which strip line breaks,
  control and bidirectional-control characters and cap the length. Values we
  own (configuration, constants, ids we generated) stay plain `slog.String`.
- **The chart's `version` stays `0.0.0`.** The git tag is the version
  authority; the release workflow stamps it at package time.

## Working on the console

The console is a SPA embedded in the service's binary, so three builds happen
in order and skipping one is the usual mistake:

```
just generate              # proto → gen/ (Go) and frontend/src/gen (TS)
just console               # ts/dist first, then frontend/dist — built, never committed
go build ./cmd/sluis             # embeds frontend/dist
```

A running `go run` keeps the bundle it started with; restart it after a
frontend build. To see every mechanic without a credential:

```
cat > /tmp/demo.yaml <<'EOF'
demo: true
store: memory
allowInsecure: true
issuerURL: http://localhost:8099
publicURL: http://localhost:8099/console
listen: {address: ":8099"}
probes: {address: ":7099"}
EOF
go run ./cmd/sluis serve --config /tmp/demo.yaml
```

Then open `http://localhost:8099/console/` and take *Continue with the
demonstration directory*. Two demonstration tenants are adopted with no
credential and no network; they live in `internal/demo`, and one account
is suspended because a leaver is the case the whole design turns on.

Discovery and the key set answer at `http://localhost:8099/`, because the
issuer owns the origin root and the console takes a path beside it. What
a demonstration run cannot do is sign anybody in **at the issuer**: that
needs a real corporate OAuth client, so the code flow and token exchange
are exercised by the tests rather than by hand.

The console's rules are in
[docs/explanation/design.md](docs/explanation/design.md), under "The
console": two
mirrored sides, every name a link, one meaning per visual form (a name is
a link, a chip is a state and nothing else, facts are a label over a
value, two-column data is a list), list pages explain concepts and object
pages state facts, and no page needs a second call to finish a sentence —
when a page does, change the contract, not the page. The vocabulary is
`frontend/src/ui.tsx`; write new pages against it rather than beside it.

Screenshots for review: headless Chrome with a fresh `--user-data-dir`
every time (it caches the previous bundle otherwise), and read the DOM
rather than the pixels for anything animated.

## While the store and runtime move

The store and runtime are being refactored to use stable ports (see
[0026](docs/decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md)–[0032](docs/decisions/0032-one-configuration-file-one-binary-one-chart.md)
and [docs/explanation/ports.md](docs/explanation/ports.md)). During this migration:

- New features must read and write state only through the ports in
  [docs/explanation/ports.md](docs/explanation/ports.md), never through new
  ConfigMap/Secret writes or new Valkey keys.
- New configuration goes into the configuration file (ADR 0032), not new
  environment variables.
- A change that cannot follow this needs an ADR first.

## Start here, for the next phase

The documents are the authority, and decisions and their dates are
recorded in the repository — in the design documents themselves, and in
the pull request that made the change; when a document and a pull
request disagree, the document wins and the pull request gets a comment.
Read in this order: `docs/explanation/trust.md`
(the rule under everything — two trust anchors chosen by scope, `groups`
as the one vocabulary), `docs/how-to/connect/service-to-service.md` (the how-to
that rule produces), `docs/explanation/integrations.md` (every case with its
anchor), then the design of whatever you touch. `docs/reference/*` says
exactly what each battery exposes; `CHANGELOG.md` says what exists today.

The service is one process. By now: several directories connected, the
policy rendered from the installation's access matrix, clusters, AWS
accounts and CI on the issuer, resources and client-described clients in
the policy, the GitHub and Slack controllers acting in real organisations and workspaces, runner
Apps from the console, the audit trail kept by an audit installation, and
the console's state restorable from five Secrets and the Slack state. The conformance run at
1.0 is in [docs/explanation/conformance-findings.md](docs/explanation/conformance-findings.md).
[CHANGELOG.md](CHANGELOG.md) is the record of what exists at each
version; read the newest entries before the design documents, which
describe the shape rather than the latest release.

Things that are not fixes and must not be reached for, each because it
was the first idea and the wrong one: raising the gateway's route
timeout; asking for a fifth Google scope; answering a browser with a 5xx
it will never see; clearing the console's own cookie to sign somebody out of
a session the proxy holds; putting an exchange in front of a same-cluster
call; verifying another cluster's key set directly; minting a structured
roles claim beside `groups`; re-mapping group names in a library; a
ConfigMap watch instead of a `checksum/policy` rollout. Naming a group is
its own set of anti-patterns, out of this file's scope — see
[docs/reference/taxonomy.md](docs/reference/taxonomy.md).

## Releasing

To cut a release `vX.Y.Z` (or a pre-release `vX.Y.Z-rc.1`):

1. On an up-to-date master, `just release-pin vX.Y.Z` sets EVERY `go.mod`'s
   requires of the repository's own modules, in every module, to the release and
   keeps the `replace` lines, so the commit builds and tests as it stands (pinning
   one module alone breaks the others: Go selects the higher version through
   the one and the other's `go.mod` needs updating).
2. Commit that (`chore: release vX.Y.Z`), merge it, and tag that commit: `git tag vX.Y.Z`,
   push the tag. The tag must be on a commit whose every `go.mod` is pinned: the
   `gate` job runs `hack/modules.py check vX.Y.Z --release` and fails the release
   otherwise, before anything is published.
3. The workflow does the rest, below. `just release-check vX.Y.Z` rehearses it.

The release workflow, on a `v*` tag, publishes everything listed in
[artifacts](docs/reference/artifacts.md), all stamped with the tag: sluis (through
the shared `release-public` workflow), then the `audit` job (`audit/.goreleaser.yaml`,
`just audit-release`) onto the same GitHub release, the TypeScript packages, and
last the `modules` job, which tags every other Go module (`hack/modules.py list`
names them) as `<dir>/vX.Y.Z`. One tag, every artifact: a consumer pins one version
of this repository.

`just docs-check` also holds the documentation's links and names
(`hack/check-docs-hygiene.py`): every relative link in a Markdown file or
`Chart.yaml` must resolve, and the retired names (the product's two old
names and the first state adapter, [ADR 0035](docs/decisions/0035-renamed-to-sluis.md))
may appear outside `docs/decisions/` and
`CHANGELOG.md` only where `hack/docs-hygiene-allow.tsv` lists them with a
reason. An identifier that deliberately keeps its old name gets an `allow` row;
prose still to be rewritten is a `baseline` count that only goes down.

Two gates run before the artifacts exist, and `just release-check vX.Y.Z`
runs the first locally (and both goreleaser configs):

- **The modules' requires.** Every module is tagged at the release commit, in
  which `just release-pin` has already set every require of a module of this
  repository to the tag (`hack/modules.py`; `hack/test-release-pin.sh` runs the
  pin on a copy and loads every module). The `gate` job runs
  `hack/modules.py check --release` first, so an unpinned commit stops the
  release before anything is published. The modules are tagged dependencies
  first (`hack/tag-modules.sh`), and each is built as a consumer would build it
  (the `replace` dropped, the other modules fetched at their tags with
  `GOPROXY=direct`, `go build` and `go vet`, `hack/build-as-consumer.sh`) before
  its `<dir>/vX.Y.Z` ref exists. A new `go.mod` joins the chain with no workflow
  edit. The checkout keeps no credential (`persist-credentials: false`).

  The tags are pushed with an installation token of the catalogue App
  `truvity-ci-automation` (`ci-actions/token-exchange` with
  `vars.ACCESS_ROSTER_ISSUER`; no key or secret), narrowed to this repository and
  `contents: write`, because a tag ruleset can name an App as its bypass but not
  GitHub Actions. The issuer must grant `release.yaml` a token of that App for
  this repository (`cfg/access.yaml` in the estate's gitops); without
  the variable the job falls back to `GITHUB_TOKEN`. Root `v*` tags stay pushed by
  hand, under the team-gated `release-tags` ruleset.

  **A module tag pushed by hand is refused by design:** the job accepts an
  existing tag only when it is the release commit, and fails otherwise, because a
  tag that a proxy has fetched cannot be taken back. The recovery is to delete the
  wrong tag before any proxy fetches it, or, once one has, to cut the next
  version. A tag ruleset restricting the module tags to the App makes the
  hand-push impossible instead of refused (an owner step).
- **No breaking patch.** Do not cut a patch while the CHANGELOG entries after
  the newest release contain `**Breaking:` (`hack/check-no-breaking-patch.sh
  CHANGELOG.md` says). A breaking change is tagged as the next minor.

The release also carries two checksummed bundles beside the binaries:
`sluis-config-schemas_<version>.tar.gz` (the JSON Schemas of the configuration
documents, `schemas/config/*.schema.json`) and
`sluis-audit-catalogue_<version>.tar.gz` (`roster.yaml` and every schema it
references, side by side: the audit writer refuses to start without one).
`internal/releasecheck` holds both to their sources, so a new schema or catalogue
action cannot be left out of a bundle.

There are no automatic releases: renovate security bumps no longer auto-release.
Cut a patch release by hand (`just release-pin`, commit, tag), and a minor or major
after its CHANGELOG heading has landed.

## Audit

audit lives in this repository. Its recipes are the root Justfile's `audit-*`
recipes (`just audit-check` is the gate; where a page says `just X` for audit,
read `just audit-X`), its tools come from the root `devbox.json`, and its CI is
the `audit*` jobs of the root `ci.yaml`. It never imports sluis; the one thing
both share is the `storage` module, which imports nothing of sluis.

- A compliance bundle is a **profile**; the files in `audit/profiles/` are *framework
  profiles*. They cite the clause they implement and carry the disclaimer that they are an
  engineering reading, not legal advice.
- A pull request that changes a contract (`audit/proto/`, `audit/schemas/`,
  `audit/profiles/`) updates the matching reference page and, if the change is
  not additive, adds a decision record. A decision that applies to one
  deployment only is not recorded here.
- `just audit-check` needs nothing but this checkout. The checks that need more
  are separate recipes, run by CI as their own jobs:
  - `just audit-race` needs a C toolchain. Run it before changing anything that
    hands a record to a background goroutine: a race there is a lost record,
    not a crash.
  - `just audit-drift-ts` regenerates TypeScript, whose plugin comes from a
    remote schema registry that rate limits; `just audit-drift` (in the gate)
    checks Go and the JSON Schema, which local plugins produce.
  - `just audit-conformance` starts PostgreSQL, S3 with object locking
    (LocalStack) and an OpenBao dev server and runs every test that skips without
    its service. It needs Docker.
  - `just audit-ts` installs the TypeScript package's dependencies, typechecks,
    tests, builds, and checks what a publish would ship.
  - Against real S3, on demand: `AUDIT_S3_REAL_BUCKET=<bucket> go test
    ./internal/s3test -run RealBucket` (from `audit/`) checks that a lock can be
    lengthened and that compliance mode refuses to shorten it, which no emulator
    implements. Point it at an Object-Locked sandbox bucket: each run leaves one
    small object locked for two days.
