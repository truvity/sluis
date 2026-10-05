# Contributing

## Layout

One repository, one tag, several deliverables, each installable or
importable alone:

```
cmd/sluis                 the one binary and image: `serve` (the
                          directory, the policy, the OpenID provider,
                          the login page, the console, and what it
                          records to the audit trail), `controller
                          github` and `controller slack` (the two
                          controllers, a second and third process from
                          the same chart), and `migrate` (copies the State
                          between storages, ADR 0031)
cmd/resource-proxy        the sidecar that fronts a stock MCP server
                          with a resource server's front door
cmd/sluisctl              the CLI, for laptops and CI jobs
cmd/acceptance            the acceptance runner against a kind cluster
charts/sluis              the chart: the service and both controllers
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
                          reconcilers share), logsafe (every request- or
                          Slack-derived value goes through it before it
                          is logged), audit (the catalogue, one
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
docs/                     getting-started, how-to (with upgrade/),
                          reference, explanation, decisions (ADRs),
                          plus operations/telemetry.md
```

Public Go packages stay free of Kubernetes and framework specifics
except in the adapters and the store implementations; anything a product
might import lives behind a storage interface.

## Toolchain

Everything comes from [devbox](https://www.jetify.com/devbox): `devbox shell`
(or direnv with the shipped `.envrc`) gives you Go, buf, golangci-lint,
helm, just and lefthook at the pinned versions. Never install the tools by
hand next to it.

`just check` runs what CI runs — build, test, lint, chart-lint, telemetry,
archive-check, docs-check, leak-canary, audit-catalogue, ts (`console` runs
inside `build`). The pre-push hook (installed by
devbox's init hook) runs the same. `vuln` is deliberately not part of
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
  `changelog-heading: never` in `auto-release.yaml` means nothing adds it
  afterwards. A patch cut only for dependency bumps has no heading. A change
  to `internal/audit/catalogue/roster.yaml` needs a new catalogue `version`
  and its `testdata/released/roster-<version>.yaml` fixture in the same pull
  request ([extending.md](docs/how-to/extend.md#7-an-audit-action)).
- **Rebase-merge only.** Branch from `master`, never stack pull requests.
- **Generated code is committed.** `just generate` rebuilds `gen/` from
  `proto/`; CI does not run buf. A contract change and its generated code
  land in the same commit.
- **Contracts are additive.** `buf breaking` guards `proto/`; a field is
  added, never renumbered or removed, so every existing client stays valid.
- **The docs are generic.** This repository describes an installation, not
  a company: no tenant names, hostnames or account names in the docs.
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

Push a `v*` tag. The release workflow builds the binaries, the images
(`ghcr.io/truvity/sluis/sluis`, and the sidecar
`/resource-proxy`),
the chart (`oci://ghcr.io/truvity/charts/sluis`), `sluisctl`'s
archives and its Nix flake, and publishes the TypeScript package to GitHub
Packages, all stamped with the tag.
The Go module and the GitHub Action are the same tag; the Pulumi library, a
module of its own, is tagged `deploy/pulumi/vX.Y.Z` at the same commit by the
release's `pulumi-tag` job. One tag, every artifact: a consumer pins one
version of this repository.

`just docs-check` also holds the documentation's links and names
(`hack/check-docs-hygiene.py`): every relative link in a Markdown file or
`Chart.yaml` must resolve, and the retired names (the product's two old
names and the first state adapter, [ADR 0035](docs/decisions/0035-renamed-to-sluis.md))
may appear outside `docs/decisions/` and
`CHANGELOG.md` only where `hack/docs-hygiene-allow.tsv` lists them with a
reason. An identifier that deliberately keeps its old name gets an `allow` row;
prose still to be rewritten is a `baseline` count that only goes down.

Two gates run before the artifacts exist, and `just release-check vX.Y.Z`
runs the pin locally:

- **The Pulumi library's require.** `deploy/pulumi/go.mod` requires
  `github.com/truvity/sluis`, and the release workflow tags the library at a
  child of the release commit whose require is the tag
  (`hack/pin-pulumi-require.sh`; the `pulumi-tag` job), so nothing is bumped by
  hand. The `gate` job runs the pin first, so a `go.mod` it cannot handle stops
  the release before anything is published, and `pulumi-tag` then builds the
  pinned library as a consumer would (the `replace` dropped, the root module
  fetched at the release tag with `GOPROXY=direct`, `go build` and `go vet`,
  `hack/build-as-consumer.sh`) before the `deploy/pulumi/vX.Y.Z` ref exists.
  The `checkout` step keeps no credential (`persist-credentials: false`); the
  token is in the API calls only.

  **A `deploy/pulumi/vX.Y.Z` tag pushed by hand at the release commit is refused
  by design:** the job accepts an existing tag only when it is a child of the
  release commit with the pinned `go.mod`, and fails otherwise, because a tag
  that a proxy has fetched cannot be taken back. The recovery is to delete the
  wrong tag before any proxy fetches it, or, once one has, to cut the next
  version, and to re-run the job for the right one. An owner step, not done
  here: a tag ruleset restricting `deploy/pulumi/v*` to the release workflow
  makes the hand-push impossible instead of refused.
- **No breaking patch.** Auto-release refuses to cut a patch while the
  CHANGELOG entries after the newest release contain `**Breaking:`
  (`hack/check-no-breaking-patch.sh`; the `guard` job of `auto-release.yaml`).
  A breaking change is tagged by hand as the next minor.

The release also carries two checksummed bundles beside the binaries:
`sluis-config-schemas_<version>.tar.gz` (the JSON Schemas of the configuration
documents, `schemas/config/*.schema.json`) and
`sluis-audit-catalogue_<version>.tar.gz` (`roster.yaml` and every schema it
references, side by side: the audit writer refuses to start without one).
`internal/releasecheck` holds both to their sources, so a new schema or catalogue
action cannot be left out of a bundle.

Auto-release is armed (`vars.AUTO_RELEASE`) and cuts **patch** tags when
changes merge: at once for a merged `security`-labelled pull request, weekly
for dependency bumps. Minors and majors are always manual — tag them when the
change merges, after its CHANGELOG heading has landed, because an armed
weekly run would otherwise ship an untagged feature as a patch.

This repository follows the shared
[component contract](https://github.com/truvity/policy/blob/master/docs/contracts/component.md).
