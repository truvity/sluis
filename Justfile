# Development commands for sluis. Tools come from devbox
# (`devbox shell`, or direnv); CI runs each recipe as its own job.

# Disable go.work (a parent workspace interferes with standalone module builds)
export GOWORK := "off"

charts := "sluis"

# The tools `telemetry` fetches, pinned: dashboardlint is truvity/observability's
# own lint of its dashboard contract, vmalert-tool is VictoriaMetrics' rule
# unit-tester, the engine of the estate's own ruler.
observability_version := "v0.43.3"
vmutils_version := "v1.152.0"

# Format all Go files
fmt:
    golangci-lint fmt ./...

# Build (compile check)
build: fmt console cross
    go build ./...

# Compile sluisctl for every platform the release builds it for.
#
# `go build ./...` proves the host platform and nothing else, so a call
# that does not exist elsewhere — syscall.Flock, which Windows has no
# equivalent name for — compiles here, passes CI, merges, and is first
# refused by goreleaser, with the change already on master and a version
# already burned on the tag (2026-09-22, v1.25.1).
#
# Only sluisctl: it is the one binary .goreleaser.yaml builds for
# Windows, on the grounds that a laptop is a laptop. The rest are Linux
# and macOS, which `go build ./...` plus CI's own runner already cover.
cross:
    #!/usr/bin/env bash
    set -euo pipefail
    for target in windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
        GOOS="${target%%/*}" GOARCH="${target##*/}" go build -o /dev/null ./cmd/sluisctl
    done

# Run unit tests
# `console` first, and the same on every recipe that COMPILES Go: CI
# runs each recipe as its own job in a fresh checkout, so nothing else
# has built the bundle the binary embeds. Locally this is invisible --
# `check` runs `build` first and the bundle is already there -- and in CI
# it is `pattern all:dist: no matching files found`, four jobs at once.
test: console
    go test ./... -coverprofile=coverage.out

# The whole module under the race detector. Not part of `check`: -race
# needs cgo and recompiles every package instrumented, which makes the
# run several times slower than `test`, and a push should not pay that;
# CI runs it as its own job. devbox forces CGO_ENABLED=0 and the race
# detector refuses to run without cgo, so it is switched back on here
# (gcc is in devbox). `console` first for the same reason as `test`.
test-race: console
    env CGO_ENABLED=1 go test -race ./...

# The LocalStack the S3 Blob and DynamoDB State adapters are tested against. Pinned
# by digest, and the community 4.x line: LocalStack's `latest` and `stable` now
# resolve to a licensed build that exits without a token, which would fail every
# fork's CI with a message its author cannot fix. A moving tag also changes the
# test. Keep it equal to the image in .github/workflows/ci.yaml.
s3_image := "localstack/localstack@sha256:3ebc37595918b8accb852f8048fef2aff047d465167edd655528065b07bc364a"

# The S3 Blob and DynamoDB State adapters against LocalStack, started
# with `docker run` (no testcontainers) and removed afterwards. Not part of
# `check`, which needs nothing but the checkout; CI runs it as its own job. It
# fails if the conformance tests skipped (hack/s3-conformance.sh,
# hack/dynamodb-conformance.sh).
test-s3:
    #!/usr/bin/env bash
    set -euo pipefail
    # A name of its own: other checkouts run LocalStack too.
    docker rm -f sluis-s3-dynamodb >/dev/null 2>&1 || true
    docker run -d --name sluis-s3-dynamodb -p 4566:4566 -e SERVICES=s3,kms,sqs,dynamodb,ssm {{s3_image}} >/dev/null
    trap 'docker rm -f sluis-s3-dynamodb >/dev/null 2>&1 || true' EXIT
    for i in $(seq 1 40); do
        curl -sf -m 3 http://localhost:4566/_localstack/health >/dev/null 2>&1 && break
        sleep 3
    done
    ACCESS_ROSTER_S3_URL=http://localhost:4566 hack/s3-conformance.sh
    ACCESS_ROSTER_DYNAMODB_URL=http://localhost:4566 hack/dynamodb-conformance.sh
    STORAGE_LOCALSTACK_URL=http://localhost:4566 hack/storage-conformance.sh
    KEYS_KMS_URL=http://localhost:4566 hack/keys-conformance.sh
    STORAGE_LOCALSTACK_URL=http://localhost:4566 hack/secrets-layout-conformance.sh

# The OpenBao development server the storage module's KV and transit backends
# are tested against. Pinned by digest; keep it equal to the `openbao` service
# in .github/workflows/ci.yaml.
openbao_image := "ghcr.io/openbao/openbao@sha256:11fd73a2102cda9c55d5d881a8c3210303146a7ec1e8ac76f526e175c6d24641"

# The storage OpenBao backends against a dev server started with `docker run`
# (no testcontainers) and removed afterwards. Fails if a test skipped
# (hack/openbao-conformance.sh).
test-openbao:
    #!/usr/bin/env bash
    set -euo pipefail
    docker rm -f sluis-openbao >/dev/null 2>&1 || true
    docker run -d --name sluis-openbao -p 127.0.0.1:8200:8200 -e BAO_DEV_ROOT_TOKEN_ID=root {{openbao_image}} >/dev/null
    trap 'docker rm -f sluis-openbao >/dev/null 2>&1 || true' EXIT
    for i in $(seq 1 30); do
        curl -sf -m 3 http://127.0.0.1:8200/v1/sys/health >/dev/null 2>&1 && break
        sleep 1
    done
    STORAGE_OPENBAO_ADDR=http://127.0.0.1:8200 STORAGE_OPENBAO_ROOT_TOKEN=root hack/openbao-conformance.sh

# Run linters. `config verify` first: `run` accepts unknown top-level keys
# silently, so a settings block in the wrong place is otherwise invisible.
lint: console
    golangci-lint config verify
    golangci-lint run ./...
    cd deploy/pulumi && GOWORK=off golangci-lint run ./...
    cd deploy/pulumi/edge/cloudflare && GOWORK=off golangci-lint run ./...
    # A `;` inside a mermaid sequenceDiagram is a STATEMENT SEPARATOR, not
    # punctuation: it splits the message text in half, the second half
    # parses as a statement with no arrow, and GitHub renders "Unable to
    # render rich display" in place of the whole diagram. Nothing in the
    # normal build reads these files, so the first reader to notice is
    # somebody looking at the documentation.
    ! grep -rn --include=*.md -E '^[[:space:]]*[A-Za-z][A-Za-z0-9_]*[[:space:]]*-?->>?.*;' docs/

# The Pulumi library (deploy/pulumi) is a module of its own so that Pulumi is
# not in the root's dependency graph; the root has no go.work, and `./...` does
# not descend into a nested module, so the root build, test and lint never see
# it and this recipe is its only gate. Its tests use Pulumi's mocks, so they
# create nothing and need no credentials, and the rendered `ports:` block is
# validated against the schemas in schemas/config, so the library cannot drift
# from the binaries it configures.
#
# The edge modules (deploy/pulumi/edge/*) are modules of their own for the same
# reason, and build against the core beside them.
#
# It also tests hack/pin-pulumi-require.sh, which the release workflow runs to
# tag the library at a commit whose require is the release being cut.
pulumi-test:
    cd deploy/pulumi && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...
    cd deploy/pulumi/edge/cloudflare && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...
    hack/test-pin-pulumi-require.sh

# Test the storage module (storage/). Like deploy/pulumi it is a module of its
# own that the root build, test and lint never see, so this recipe is its gate.
storage-test:
    cd storage && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...

# Run Go vulnerability check. Deliberately not part of `check`: a newly
# published CVE in a dependency must not turn a PR that never touched it
# red. `.github/workflows/security.yaml` runs this as its own job.
vuln: console
    govulncheck ./...

# Regenerate gen/ from proto/ (buf + protoc-gen-go + protoc-gen-connect-go,
# all from devbox). Generated code is COMMITTED so the module is
# `go get`-able without buf installed.
generate:
    buf lint
    buf generate

# Acceptance against a real API server, in a throwaway kind cluster.
#
# Everything else runs against fakes, and the fakes are silent about the
# three things this checks: a real API server validates object names,
# refuses a create that raced another, and is the only thing that can
# answer a TokenReview — which is what recovery and the API listener's
# guard are built on.
acceptance: console
    kind create cluster --name sluis-acceptance
    kubectl --context kind-sluis-acceptance create namespace acceptance
    go run ./cmd/acceptance -namespace acceptance -kubeconfig ""
    kind delete cluster --name sluis-acceptance

# Check the release configuration without cutting one, and, given the tag
# about to be cut (`just release-check v1.64.0`), refuse a tag the Pulumi
# library does not agree with.
#
# The require gate: deploy/pulumi requires github.com/truvity/sluis, and a
# library tagged vX.Y.Z whose require names another version ships against
# the wrong root. The release workflow tags the library at a commit whose
# require is pinned to the release (hack/pin-pulumi-require.sh) and builds it
# as a consumer before tagging (hack/build-as-consumer.sh), so the require on
# master is never bumped by hand; this runs the pin, as the workflow's gate
# does.
#
# The release path, as far as it can be exercised without a tag.
#
# `goreleaser check` validates the config and `build --single-target`
# proves it compiles, but NEITHER reaches the archives stage — which is
# where v0.12.0 failed, four minutes into a tagged run, publishing
# nothing. So the archive shapes are checked here by reading the same
# file goreleaser reads.
release-check tag="": console
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -n "{{tag}}" ]; then
        ./hack/pin-pulumi-require.sh "{{tag}}" - < deploy/pulumi/go.mod > /dev/null
        ./hack/pin-pulumi-require.sh "{{tag}}" - < deploy/pulumi/edge/cloudflare/go.mod > /dev/null
    fi
    ./hack/check-archives.py
    goreleaser check
    goreleaser build --snapshot --clean --single-target

# The cheap half of release-check, for `check`: it reads a file and
# needs no compiler, so it costs nothing to run on every push.
archive-check:
    ./hack/check-archives.py

# The reason this repository can be public. Runs in CI as its own job.
leak-canary:
    hack/leak-canary.sh

# Every Go symbol the documentation names must exist. Nothing compiles a
# code block in a Markdown file, so a rename leaves the old name in the
# guide and the first person to notice is a stranger following it. Every
# proto service and RPC must also be named in docs/reference/contracts.md.
#
# And the documentation's hygiene (hack/check-docs-hygiene.py): every relative
# link in a Markdown file or Chart.yaml resolves, and the retired names
# (access-roster, access-issuer, NATS) appear only where
# hack/docs-hygiene-allow.tsv says, with a reason.
#
# The generated regions of the docs (`<!-- generated: name -->`) must be
# what `just docs-generate` writes, and a page over 400 lines is a warning.
docs-check:
    ./hack/check-docs-symbols.py
    ./hack/check-docs-hygiene.py
    ./hack/check-docs-length.py
    go run ./cmd/docsgen -check
    go test -count=1 ./internal/contractsdoc/ ./internal/port/matrixdoc/

# Rewrite the generated regions of the docs (config keys, chart values, policy
# keys, audit actions, alerts, the ADR index) from their sources: the JSON
# schemas, the audit catalogue, the chart's golden alert render and the ADRs.
# Run it after changing any of them; docs-check fails when a region is stale.
docs-generate:
    go run ./cmd/docsgen

# Regenerate docs/reference/adapters.md from the adapter registry. Run it
# after adding, removing or changing an adapter; docs-check fails when stale.
adapters-doc:
    go run ./internal/port/matrixdoc/gen

# Run go mod tidy
tidy:
    go mod tidy

# Clean build artifacts
clean:
    rm -rf dist/ frontend/dist/ ts/dist/ coverage.out

# Lint the chart, compare its golden renders, and render every
# negative fixture.
#
# The schema is part of the lint: an unknown key must fail the render,
# not be silently ignored. Everything a schema cannot express -- a value
# the service cannot start without, a posture that admits nobody, one
# route described twice -- is refused by the chart's own render-time
# validation. Every rule of either kind has a fixture under
# tests/invalid/<chart>/ that must FAIL, and fail for the reason on its
# second line (`# error: ...`): a fixture that fails for some other
# reason proves nothing about its own rule.
#
# The golden renders (tests/cases -> tests/golden, `just golden` to
# regenerate) pin every byte of output. The checks after them are the
# properties a regenerated golden could lose without anyone noticing in
# review.
chart-lint:
    #!/usr/bin/env bash
    set -euo pipefail
    for chart in {{ charts }}; do
      # Bare first: the shipped values must satisfy their own schema, or
      # anyone who lints the chart as published gets a failure.
      helm lint "charts/$chart"
      helm lint "charts/$chart" -f "tests/cases/$chart/minimal/values.yaml"
      # `if`, not `!`: under `set -e` a negated command that fails does
      # not stop the script, so `! cmd` would check nothing.
      if helm template sluis "charts/$chart" --set bogusKey=1 >/dev/null 2>&1; then
        echo "$chart: an unknown key rendered" >&2
        exit 1
      fi
      for values in tests/invalid/"$chart"/*.yaml; do
        if err="$(helm template sluis "charts/$chart" -f "$values" 2>&1 >/dev/null)"; then
          echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
          exit 1
        fi
        want="$(sed -n '2s/^# error: //p' "$values")"
        if [ -z "$want" ]; then
          echo "NO '# error:' LINE: $values" >&2
          exit 1
        fi
        if ! grep -qF -- "$want" <<<"$err"; then
          printf 'FAILED FOR ANOTHER REASON: %s\n  want: %s\n  got:  %s\n' "$values" "$want" "$err" >&2
          exit 1
        fi
      done
      # Every example the chart SHIPS must render on top of its own
      # minimal values. An example is something a reader copies, so one
      # that no longer renders is a broken instruction found by a
      # stranger; nothing else in this repository reads these files.
      for example in charts/"$chart"/examples/*.yaml; do
        [ -e "$example" ] || continue
        helm template sluis "charts/$chart" \
          -f "tests/cases/$chart/minimal/values.yaml" -f "$example" >/dev/null
      done
      echo "$chart: schema and $(ls tests/invalid/"$chart"/*.yaml | wc -l | tr -d ' ') negative fixtures OK"
    done
    hack/golden.sh
    # The schemas are generated, and the binaries and the chart are held to
    # the committed files: a diff here is a builder changed without
    # `just config-schemas`.
    just config-schemas
    git diff --exit-code -- schemas/config charts/sluis/values.schema.json
    # What the chart renders for each component's `config` is what the values
    # say, and is a file that component's binary accepts. Required rather than
    # skipped: a test that quietly does not run proves nothing.
    ACCESS_ROSTER_REQUIRE_HELM=1 go test -count=1 ./tests/chart/
    # `push` is a chart-side instruction to External Secrets and not part
    # of an App's declaration: the service's loader refuses a key it does
    # not know, so an entry's push block reaching the rendered catalogue
    # would stop the service at start.
    if grep -n '^      push:' tests/golden/sluis/*.yaml; then
      echo "a catalogue entry's push block reached the rendered catalogue" >&2
      exit 1
    fi
    # "/console" and "/console/" are the same place: both spellings must
    # render the same, and never a route to "/console//".
    diff tests/golden/sluis/route.yaml tests/golden/sluis/route-trailing-slash.yaml
    if grep -l 'console//' tests/golden/sluis/*.yaml; then exit 1; fi
    # Every backendRefs entry writes `weight` out. A desired/live
    # comparison normalises core-API defaults but not CRDs, so a field the
    # API server fills in is a permanent diff. Routes and policies alike.
    for golden in tests/golden/*/*.yaml; do
      test "$(yq ea '[.. | select(tag == "!!map" and has("backendRefs")) | .backendRefs[] | select(has("weight") | not)] | length' "$golden")" = "0"
    done
    # Every PushSecret dataTo entry carries its own storeRef. external-
    # secrets validates this with a CEL rule in the CRD, so nothing that
    # only RENDERS can see it: helm template, the values schema and the
    # golden diff all pass on a manifest the API server refuses outright
    # with "storeRef must specify either name or labelSelector". It
    # shipped that way once, and Argo retried the rejected task forever
    # while the resource simply never existed.
    for golden in tests/golden/*/*.yaml; do
      test "$(yq ea '[.. | select(tag == "!!map" and has("dataTo")) | .dataTo[] | select(has("storeRef") | not)] | length' "$golden")" = "0"
    done

# The chart's alert rules and dashboard, held to what the estate holds them to.
#
# The dashboard is generated (hack/dashboards/access-roster-overview.py) and the
# committed JSON is held to the generator. `dashboardlint`, from
# truvity/observability at the version pinned above, judges it against that
# repository's dashboard contract (docs/dashboards.md): a datasource variable
# every panel uses, a cluster variable, `$cluster` in the title and every
# query. The refusal is proved to be real, too: the same dashboard with a
# literal datasource must fail.
#
# The rules are unit-tested with vmalert-tool (a pinned, checksum-verified
# release) through tests/chart: every rule has a test that fires it and one
# that must not, and the test refuses a rule without both.
telemetry:
    #!/usr/bin/env bash
    set -euo pipefail
    python3 hack/dashboards/access-roster-overview.py | diff - charts/sluis/dashboards/access-roster-overview.json

    tools=$(mktemp -d); trap 'rm -rf "$tools"' EXIT
    GOBIN="$tools" go install github.com/truvity/observability/cmd/dashboardlint@{{observability_version}}
    "$tools/dashboardlint" charts/sluis/dashboards/*.json
    # The refusal is real: the same dashboard with a literal datasource must fail.
    sed 's/"uid": "${datasource}"/"uid": "a-literal-uid"/' charts/sluis/dashboards/access-roster-overview.json > "$tools/pinned.json"
    if "$tools/dashboardlint" "$tools/pinned.json" 2>"$tools/pinned.err"; then
        echo "dashboardlint accepted a dashboard pinned to one datasource" >&2; exit 1
    fi
    grep -q "literal datasource" "$tools/pinned.err"

    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "no vmutils for $(uname -m)" >&2; exit 1 ;; esac
    tarball="vmutils-$os-$arch-{{vmutils_version}}.tar.gz"
    base="https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/{{vmutils_version}}"
    cache="${XDG_CACHE_HOME:-$HOME/.cache}/sluis/vmutils-{{vmutils_version}}-$os-$arch"
    if [ ! -x "$cache/vmalert-tool-prod" ]; then
        mkdir -p "$cache"
        curl -fsSL "$base/$tarball" -o "$tools/$tarball"
        curl -fsSL "$base/${tarball%.tar.gz}_checksums.txt" -o "$tools/sums.txt"
        (cd "$tools" && grep " $tarball\$" sums.txt | sha256sum -c -)
        tar -xzf "$tools/$tarball" -C "$cache" vmalert-tool-prod
    fi
    ACCESS_ROSTER_REQUIRE_HELM=1 ACCESS_ROSTER_REQUIRE_VMALERT=1 ACCESS_ROSTER_VMALERT_TOOL="$cache/vmalert-tool-prod" go test -count=1 ./tests/chart/

# The JSON Schema of each binary's configuration file, written into
# schemas/config/ from internal/config/schema, and the chart's values schema,
# which embeds them under each component's `config`. The binaries embed the
# committed files, so a change to the builder that is not followed by this
# fails `go test` and the drift check in chart-lint.
config-schemas:
    go run ./internal/config/gen

# Regenerate the golden renders -- review the diff before committing.
golden:
    hack/golden.sh update

# Install every toolchain dependency, on both sides. Separate from the
# builds because `npm ci` is the slow part and it does not change
# between them.
#
# `tidy` first: downloading what go.mod asks for is not much use if
# go.mod is missing something the code imports, and the two together are
# what "my dependencies are in order" means.
#
# This does NOT touch gen/. Code generated from proto/ stays committed:
# it is Go source, small and diffable, and it is what makes the module
# `go get`-able without buf installed. That is a different argument from
# a minified bundle, which is neither small nor diffable.
deps: tidy
    go mod download
    cd ts && npm ci
    cd frontend && npm ci

# Build the TypeScript package into ts/dist.
#
# NOT committed, and FIRST: the console's package.json depends on it as
# `file:../ts`, and resolves through the root manifest's `main`, which
# points into ts/dist. Build the console before this and it resolves an
# import to a directory that is not there yet.
#
# The release builds it the same way and publishes the root package to
# GitHub Packages; nothing builds it on install.
ts-package: deps
    cd ts && npx tsc -p tsconfig.build.json

# Typecheck and test the TypeScript package.
ts: ts-package
    cd ts && npx tsc --noEmit && npx vitest run

# Build the console SPA into frontend/dist, which the Go binary embeds.
#
# NOT committed, and `build` depends on it so the embed always has
# something current to read. There is no drift check any more because
# drift is not possible: the bundle is produced from the lockfile every
# time, and if it is absent the compiler says so.
#
# The console's own unit tests run first: the pure models its pages read
# (which App needs you, what a page's first line says) and the router's
# moved addresses. They need no browser.
console: ts-package
    cd frontend && npm test
    cd frontend && npm run build

# The audit catalogue against the audit component's own toolchain, at the
# version go.mod pins: the document is valid, and every action the code
# emits is declared and every declared one emitted. A name built at run
# time is invisible to it, which is why every action is spelled once, in
# internal/audit/events.go.
audit-catalogue:
    #!/usr/bin/env bash
    set -euo pipefail
    version=$(go list -m -f '{{{{.Version}}' github.com/truvity/audit/sdk)
    audit="go run github.com/truvity/audit/cmd/audit@${version}"
    $audit validate internal/audit/catalogue/roster.yaml
    # The Go code only: the console carries every action name in its
    # generated sentences, which would pass for emitting them.
    $audit check-emitters internal --catalogue internal/audit/catalogue/roster.yaml
    # The console's copy of the sentences is generated from the same
    # document, and a diff here is a catalogue changed without it.
    just audit-sentences
    git diff --exit-code -- frontend/src/auditSentences.ts

# The catalogue's sentences for the console's Audit page, generated from
# internal/audit/catalogue/roster.yaml by the audit component's toolchain.
audit-sentences:
    #!/usr/bin/env bash
    set -euo pipefail
    version=$(go list -m -f '{{{{.Version}}' github.com/truvity/audit/sdk)
    {
        echo '// Code generated by `just audit-sentences` from internal/audit/catalogue/roster.yaml. DO NOT EDIT.'
        echo 'import type { Sentences } from "@truvity/audit";'
        echo
        printf 'export const roster: Sentences = '
        go run "github.com/truvity/audit/cmd/audit@${version}" messages internal/audit/catalogue/roster.yaml
    } > frontend/src/auditSentences.ts

# Run all checks (build + test + lint + chart-lint + telemetry + leak-canary)
# Everything CI runs, so that the pre-push hook catches what CI would.
# `vuln` is deliberately not here: run it on its own with `just vuln`,
# the same way `.github/workflows/security.yaml` does.
#
# `ts` is in here despite being slow: it typechecks and tests the
# published package, which nothing else does. `console` arrives through
# `build`, which needs it.
check: build test pulumi-test storage-test lint chart-lint telemetry archive-check docs-check leak-canary audit-catalogue ts

# ---------------------------------------------------------------------------
# audit/ — the audit trail component (moved here from truvity/audit).
#
# Its recipes carry the prefix `audit-` and run in audit/, a Go module of its
# own (with audit/sdk and audit/deploy/pulumi beside it); audit never imports
# sluis (a depguard rule and a test hold that). `audit-catalogue` and
# `audit-sentences` above are sluis's own use of audit's toolchain and have
# nothing to do with these. Toolchain, CI and canary are the root's: there is
# no second Justfile, devbox or workflow for audit.
# ---------------------------------------------------------------------------

# The OpenBAO the transit key provider and signer are tested against. Pinned
# by digest for the reason the S3 image is: a moving tag changes the test.
audit_openbao_image := "openbao/openbao@sha256:597f62847dd382382056a1d6704d50465908c2040038c4611832a23269a67112"

# Format all Go files
[working-directory: 'audit']
audit-fmt:
    golangci-lint fmt ./...
    cd sdk && golangci-lint fmt ./...

# Regenerate everything from the proto: Go, the published JSON Schema, and
# TypeScript. The TypeScript plugin is fetched from the schema registry, so this
# needs the network.
[working-directory: 'audit']
audit-generate:
    buf generate

# Regenerate only what local plugins produce: Go, and the record's JSON Schema.
[working-directory: 'audit']
audit-generate-local:
    buf generate --template buf.gen.local.yaml

# Generated code is committed. A change to the proto that is not followed by
# `just generate` leaves the tree dirty here.
#
# The gate checks only what local plugins produce, so that it needs nothing but
# this checkout: the TypeScript plugin comes from a remote registry that rate
# limits, and a gate that fails because somebody else was generating code is a
# gate people learn to ignore. `drift-ts` is the same check for TypeScript and
# belongs in CI, where a retry is cheap.
[working-directory: 'audit']
audit-drift: audit-generate-local audit-ts-sentences audit-config-schemas
    git diff --exit-code -- gen sdk/gen ts/src/catalogue schemas/config ../charts/audit/values.schema.json ../charts/audit/templates/_presets.tpl

# The JSON Schema of each binary's configuration file, written into
# schemas/config/ from internal/config/schema, and the chart's values schema,
# which embeds them under each component's `config`. The binaries embed the
# first and Helm reads the second; `drift` fails when either is not what this
# writes.
[working-directory: 'audit']
audit-config-schemas:
    go run ./internal/config/gen

# The common catalogue's sentences, for the viewer in @truvity/audit. Generated
# from sdk/catalogue/common.yaml so the two cannot drift: `drift` fails on a diff.
[working-directory: 'audit']
audit-ts-sentences:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p ts/src/catalogue
    {
        echo '// Code generated by `just sentences` from sdk/catalogue/common.yaml. DO NOT EDIT.'
        echo 'import type { Sentences } from "../sentences.js";'
        echo
        printf 'export const common: Sentences = '
        go run ./cmd/audit messages sdk/catalogue/common.yaml
    } > ts/src/catalogue/common.ts

[working-directory: 'audit']
audit-drift-ts: audit-generate
    git diff --exit-code -- gen sdk/gen ts/src/gen

# Lint proto and check that nothing released has changed incompatibly
[working-directory: 'audit']
audit-proto:
    buf lint
    # A first release has nothing to be incompatible with, and saying so beats
    # a recipe that always passes because its comparison silently failed.
    if tag=$(git describe --tags --abbrev=0 --match 'audit/v*' 2>/dev/null); then \
        buf breaking --against ".git#tag=$tag,subdir=audit"; \
    else \
        echo "no release tag yet: nothing to compare against"; \
    fi

# Build (compile check). The repository is two Go modules: the root, and the
# consumer SDK in sdk/. With the workspace on, `go build ./...` at the root does
# not cover sdk/, so each is built on its own; `installable` is the build that
# has no sight of the workspace.
[working-directory: 'audit']
audit-build: audit-fmt
    go build ./...
    cd sdk && go build ./...

# Run unit tests
[working-directory: 'audit']
audit-test:
    go test ./... -coverprofile=coverage.out
    cd sdk && go test ./... -coverprofile=coverage.out
    # One profile for the coverage report: the SDK's, without its header line.
    tail -n +2 sdk/coverage.out >> coverage.out && rm sdk/coverage.out

# Run the whole suite against a real Postgres.
#
# The index is the one part of this repository that cannot be tested without a
# database, and mocking it would prove nothing about the idempotency that is its
# whole contract. So those tests skip when AUDIT_POSTGRES_URL is unset: a
# contributor without Postgres still runs everything else, and `check` stays
# hermetic. This runs the whole suite rather than `./index/...`, because the
# writer's end-to-end tests need the database too.
# CI runs this as its own job with a service container.
#
# This starts a Postgres under .devbox, initialising it on first use.
[working-directory: 'audit']
audit-test-postgres:
    #!/usr/bin/env bash
    set -euo pipefail
    export PGDATA="{{justfile_directory()}}/.devbox/virtenv/postgresql/data"
    export PGHOST="{{justfile_directory()}}/.devbox/virtenv/postgresql"
    mkdir -p "$PGHOST"
    [ -d "$PGDATA/base" ] || initdb -U postgres --auth=trust >/dev/null
    pg_ctl status -D "$PGDATA" >/dev/null 2>&1 || \
        pg_ctl -D "$PGDATA" -o "-k $PGHOST -c listen_addresses=" -l "$PGHOST/log" start -w
    createdb -h "$PGHOST" -U postgres audit_test 2>/dev/null || true
    AUDIT_POSTGRES_URL="postgres://postgres@/audit_test?host=$PGHOST" go test ./...

# Stop the Postgres that `test-postgres` started
[working-directory: 'audit']
audit-stop-postgres:
    pg_ctl stop -D "{{justfile_directory()}}/.devbox/virtenv/postgresql/data" || true

# Run the archive-walk tests and the bucket-contract conformance suite against a
# real S3, the SQS sink against a real SQS, and the DynamoDB deduplication store
# against a real DynamoDB API: LocalStack serves all three.
#
# These are the tests that would have caught the two bugs the memory store hid:
# a walk covering one tenant, and a listing stopping at the first thousand
# keys. They skip when AUDIT_S3_URL is unset, so `check` stays hermetic.
[working-directory: 'audit']
audit-test-s3:
    #!/usr/bin/env bash
    set -euo pipefail
    docker rm -f audit-s3 >/dev/null 2>&1 || true
    docker run -d --name audit-s3 -p 4566:4566 -e SERVICES=s3,kms,sqs,dynamodb {{s3_image}} >/dev/null
    trap 'docker rm -f audit-s3 >/dev/null 2>&1 || true' EXIT
    for i in $(seq 1 40); do
        curl -sf -m 3 http://localhost:4566/_localstack/health >/dev/null 2>&1 && break
        sleep 3
    done
    AUDIT_S3_URL=http://localhost:4566 AUDIT_SQS_URL=http://localhost:4566 AUDIT_DYNAMODB_URL=http://localhost:4566 \
        go test ./internal/s3test/... ./internal/bucketcontract/... ./store/... ./sink/sqssink/... ./dedupe/...

# The whole suite with every service it can use: Postgres, S3 with object
# locking, and an OpenBAO dev server. Every test that skips without its service
# runs here — the searchers' conformance suite against all three searchers, the
# transports' corpus, the archive walks, the transit keys and signer, and the
# read-only conformance run against an embedded query service. A few minutes;
# not part of `check`, which needs nothing but the checkout. CI runs it as its
# own job.
[working-directory: 'audit']
audit-conformance:
    #!/usr/bin/env bash
    set -euo pipefail
    export PGDATA="{{justfile_directory()}}/.devbox/virtenv/postgresql/data"
    export PGHOST="{{justfile_directory()}}/.devbox/virtenv/postgresql"
    mkdir -p "$PGHOST"
    [ -d "$PGDATA/base" ] || initdb -U postgres --auth=trust >/dev/null
    pg_ctl status -D "$PGDATA" >/dev/null 2>&1 || \
        pg_ctl -D "$PGDATA" -o "-k $PGHOST -c listen_addresses=" -l "$PGHOST/log" start -w
    createdb -h "$PGHOST" -U postgres audit_test 2>/dev/null || true

    docker rm -f audit-s3 audit-bao >/dev/null 2>&1 || true
    trap 'docker rm -f audit-s3 audit-bao >/dev/null 2>&1 || true' EXIT
    docker run -d --name audit-s3 -p 4566:4566 -e SERVICES=s3,kms,sqs,dynamodb {{s3_image}} >/dev/null
    docker run -d --name audit-bao -p 8200:8200 -e BAO_DEV_ROOT_TOKEN_ID=root \
        {{audit_openbao_image}} server -dev -dev-listen-address=0.0.0.0:8200 >/dev/null
    for i in $(seq 1 40); do
        curl -sf -m 3 http://localhost:4566/_localstack/health >/dev/null 2>&1 && \
            curl -sf -m 3 http://localhost:8200/v1/sys/health >/dev/null 2>&1 && break
        sleep 3
    done

    AUDIT_POSTGRES_URL="postgres://postgres@/audit_test?host=$PGHOST" \
    AUDIT_S3_URL=http://localhost:4566 AUDIT_DYNAMODB_URL=http://localhost:4566 \
    AUDIT_OPENBAO_URL=http://localhost:8200 AUDIT_OPENBAO_TOKEN=root \
        go test -count=1 ./...

# Run the tests under the race detector. The emitter hands records to a
# background writer, so a data race there would be a lost or duplicated record
# rather than a crash, and would not show up in an ordinary run.
#
# This is not part of `check`, and deliberately. Everything else in this
# repository builds with cgo off, which is what makes the binaries static and
# the images small; the race detector is the one thing that needs a C
# toolchain. Putting it in the gate would mean every contributor needs one to
# run the gate at all. CI runs this as its own job, where the toolchain is the
# runner's own.
[working-directory: 'audit']
audit-race:
    CGO_ENABLED=1 go test -race ./...
    cd sdk && CGO_ENABLED=1 go test -race ./...

# Run linters
[working-directory: 'audit']
audit-lint:
    golangci-lint config verify
    golangci-lint run ./...
    cd sdk && golangci-lint run ./...
    cd deploy/pulumi && GOWORK=off golangci-lint run ./...
    goreleaser check
    # Nothing built is committed. A binary in a public repository's history
    # is in every clone forever, and carries the build machine's paths. The
    # largest source file here is under 100 KiB; 1 MiB is a build output.
    ! git ls-files -s | awk '{print $2" "$4}' | git cat-file --batch-check='%(objectsize) %(rest)' | awk '$1 > 1048576 {print "too large to be source:", $2; found=1} END {exit !found}'
    # A `;` inside a mermaid sequenceDiagram is a statement separator: it
    # splits the message and GitHub renders nothing. Keep them out of docs.
    ! grep -rn --include=*.md -E '^[[:space:]]*[A-Za-z][A-Za-z0-9_]*[[:space:]]*-?->>?.*;' docs/

# The consumer SDK must stay small. It is a module of its own (sdk/) so that
# an emitter's dependency graph holds what an emitter needs and not what the
# writer and the query service need: no database driver, no stream server, no
# object store, no OpenBao, no OpenTelemetry SDK or exporter, no JWT library.
# This lists the packages the SDK builds, refuses to pass on an empty list, and
# fails on any package from the forbidden set, naming it. The set is a deny
# list and not an allow list so that a Renovate bump of a dependency the SDK
# already has is never a failure; a NEW heavy dependency is.
[working-directory: 'audit']
audit-sdk-closure:
    #!/usr/bin/env bash
    set -euo pipefail
    cd sdk
    deps=$(go list -deps ./... | sort -u)
    count=$(grep -c . <<<"$deps")
    # The SDK has dozens of packages of its own and its dependencies; an empty
    # or tiny list means the listing failed, not that the closure is clean.
    if [ "$count" -lt 50 ]; then
        echo "sdk-closure: only $count packages listed; the check scanned nothing" >&2
        exit 1
    fi
    # Other products of the estate stay on the list by name: access-roster is
    # sluis's former name and both are listed, so that neither can be imported by
    # the SDK under the one the other is called now.
    forbidden='^(github.com/jackc/|github.com/nats-io/|github.com/aws/|github.com/klauspost/|github.com/lestrrat-go/|github.com/truvity/(gateway-auth|access-roster|sluis|gemaal|policy|ocictl)|go.opentelemetry.io/otel/(sdk|exporters)|go.opentelemetry.io/contrib/|google.golang.org/(grpc|genproto)|helm.sh/|github.com/openbao/|github.com/hashicorp/|k8s.io/)'
    # The module's own packages are github.com/truvity/sluis/audit/sdk/...;
    # anything else under github.com/truvity/sluis is the audit module or the
    # sluis root, and the SDK may import neither.
    bad=$(grep -vE '^github.com/truvity/sluis/audit/sdk(/|$)' <<<"$deps" | grep -E "$forbidden" || true)
    if [ -n "$bad" ]; then
        echo "sdk-closure: the SDK depends on server-only packages:" >&2
        echo "$bad" >&2
        exit 1
    fi
    echo "sdk-closure: $count packages, none server-only"

# Hold this repository's own presets and catalogue to the contracts it publishes,
# and hold this repository's own code to its catalogue the way an adopter's is
# held. The second line lists every action the common catalogue declares that
# nothing here emits yet; each is a job for a later milestone, and the digest
# jobs' events sat on that list unnoticed until the tool was pointed at home.
[working-directory: 'audit']
audit-schemas:
    go run ./cmd/audit validate --profiles profiles sdk/catalogue/common.yaml
    go run ./cmd/audit check-emitters . --catalogue sdk/catalogue/common.yaml

# The Pulumi library (deploy/pulumi) is a module of its own so that Pulumi is
# not in the root's dependency graph, and the committed go.work does not list
# it for the same reason: a workspace's module graph is one graph. Its tests use
# Pulumi's mocks, so they create nothing and need no credentials, and the
# rendered function configuration is validated against the schemas in
# schemas/config, so the library cannot drift from the binaries it deploys.
[working-directory: 'audit']
audit-pulumi-test:
    cd deploy/pulumi && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...

# Hold the chart to what the binaries will accept.
#
# The golden renders are committed, so a template change that alters a manifest
# shows up as a diff a reviewer reads rather than as a surprise in a cluster.
# The refusals matter more: each is a configuration the binaries reject at
# start-up, or accept and then get quietly wrong, and a chart that renders one
# anyway moves the failure somewhere nobody is looking. And the chart passes
# each component's `config` through unchanged, which `go test ./tests/chart`
# holds it to: every rendered ConfigMap is the values' block, and a file its
# binary's schema accepts.
[working-directory: 'audit']
audit-chart:
    helm lint ../charts/audit -f ../charts/audit/testdata/values/stream.yaml
    AUDIT_REQUIRE_HELM=1 go test -count=1 ./tests/chart/
    bash ../charts/audit/testdata/refuse.sh
    helm template audit ../charts/audit -f ../charts/audit/testdata/values/direct.yaml \
        > ../tests/golden/audit/direct.yaml
    helm template audit ../charts/audit -f ../charts/audit/testdata/values/stream.yaml \
        > ../tests/golden/audit/stream.yaml
    helm template audit ../charts/audit -f ../charts/audit/testdata/values/transit.yaml \
        > ../tests/golden/audit/transit.yaml
    # The attested tier on an S3-compatible store: no lock, an endpoint, path
    # style, static credentials, and an exports bucket on a store of its own.
    helm template audit ../charts/audit -f ../charts/audit/testdata/values/attested.yaml \
        > ../tests/golden/audit/attested.yaml
    # The two shapes the deployment pages document, rendered from the very
    # files those pages show. The values above are trial installs with no
    # index, so without these the migration hook -- which only exists when
    # there is a database -- is never rendered at all.
    helm template audit ../charts/audit -f ../charts/audit/examples/direct.yaml \
        > ../tests/golden/audit/example-direct.yaml
    helm template audit ../charts/audit -f ../charts/audit/examples/stream.yaml \
        > ../tests/golden/audit/example-stream.yaml
    helm template audit ../charts/audit -f ../charts/audit/examples/sqs.yaml \
        > ../tests/golden/audit/example-sqs.yaml
    # The writer elsewhere (the Lambda behind SQS): observe, query and a notary
    # in the cluster, every sink on the queue, and no write path rendered.
    helm template audit ../charts/audit -f ../charts/audit/examples/external-writer.yaml \
        > ../tests/golden/audit/example-external-writer.yaml
    # The OTLP endpoint value (decision N4a): with it set, every pod carries the
    # OpenTelemetry SDK environment, each with its own service name. The goldens
    # above, which set none, are what holds "empty renders nothing".
    helm lint ../charts/audit -f ../charts/audit/testdata/values/telemetry.yaml
    helm template audit ../charts/audit -f ../charts/audit/testdata/values/telemetry.yaml \
        > ../tests/golden/audit/telemetry.yaml
    # The two other things the chart renders, each alone: the alert rules and
    # the Grafana dashboards (`renders: alerts`, `renders: dashboards`).
    helm lint ../charts/audit -f ../charts/audit/testdata/values/alerts.yaml
    helm lint ../charts/audit -f ../charts/audit/testdata/values/dashboards.yaml
    helm template audit ../charts/audit -f ../charts/audit/testdata/values/alerts.yaml \
        > ../tests/golden/audit/alerts.yaml
    helm template audit ../charts/audit -f ../charts/audit/testdata/values/dashboards.yaml \
        > ../tests/golden/audit/dashboards.yaml
    # A hook Pod whose service account the chart creates normally is admitted
    # and then never scheduled: only an install finds that, so assert it here.
    for shape in direct stream transit attested telemetry example-direct example-stream example-sqs example-external-writer; do \
        yq ea '[.]' -o=json ../tests/golden/audit/$shape.yaml \
            | python3 ../charts/audit/testdata/hook-order.py; \
    done
    git diff --exit-code -- ../tests/golden/audit

# The alert rules and the dashboard, held to the observability contract: the
# generated dashboard is the committed one, passes the dashboard lint (and the
# lint is shown to refuse a dashboard pinned to a datasource, so it is not a
# check that passes whatever it is given), and every rule fires on what it
# should and stays silent on what it should not, on vmalert-tool. Needs the
# network for the two pinned tools. CI runs this as its own recipe.
[working-directory: 'audit']
audit-telemetry:
    #!/usr/bin/env bash
    set -euo pipefail
    python3 hack/dashboards/audit-overview.py | diff - ../charts/audit/dashboards/audit-overview.json

    tools=$(mktemp -d); trap 'rm -rf "$tools"' EXIT
    GOBIN="$tools" go install github.com/truvity/observability/cmd/dashboardlint@{{observability_version}}
    "$tools/dashboardlint" ../charts/audit/dashboards/*.json
    # The refusal is real: the same dashboard with a literal datasource must fail.
    sed 's/"uid": "${datasource}"/"uid": "a-literal-uid"/' ../charts/audit/dashboards/audit-overview.json > "$tools/pinned.json"
    if "$tools/dashboardlint" "$tools/pinned.json" 2>"$tools/pinned.err"; then
        echo "dashboardlint accepted a dashboard pinned to one datasource" >&2; exit 1
    fi
    grep -q "literal datasource" "$tools/pinned.err"

    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "no vmutils for $(uname -m)" >&2; exit 1 ;; esac
    tarball="vmutils-$os-$arch-{{vmutils_version}}.tar.gz"
    base="https://github.com/VictoriaMetrics/VictoriaMetrics/releases/download/{{vmutils_version}}"
    cache="${XDG_CACHE_HOME:-$HOME/.cache}/audit/vmutils-{{vmutils_version}}-$os-$arch"
    if [ ! -x "$cache/vmalert-tool-prod" ]; then
        mkdir -p "$cache"
        curl -fsSL "$base/$tarball" -o "$tools/$tarball"
        curl -fsSL "$base/${tarball%.tar.gz}_checksums.txt" -o "$tools/sums.txt"
        (cd "$tools" && grep " $tarball\$" sums.txt | sha256sum -c -)
        tar -xzf "$tools/$tarball" -C "$cache" vmalert-tool-prod
    fi
    AUDIT_REQUIRE_HELM=1 AUDIT_REQUIRE_VMALERT=1 AUDIT_VMALERT_TOOL="$cache/vmalert-tool-prod" go test -count=1 ./tests/chart/

# The TypeScript package: install, typecheck, test, build, and check what a
# publish would ship. Not part of `check`, which needs nothing but the
# checkout: this fetches from the npm registry. CI runs it as its own job.
[working-directory: 'audit']
audit-ts:
    cd ts && npm ci --no-audit --no-fund
    cd ts && npx tsc --noEmit
    cd ts && npx vitest run
    cd ts && rm -rf dist && npx tsc -p tsconfig.build.json
    # What a publish from the root would ship: the compiled package, and no
    # test or test helper. The root's prepare script is what builds the
    # package when it is installed from a git commit; it has just run above.
    npm pack --dry-run --ignore-scripts --json | grep -q '"path": "ts/dist/react/index.js"'
    ! npm pack --dry-run --ignore-scripts --json | grep -E '"path": "ts/dist/.*(test|testing)'

# Build everything a release would, locally and unpublished: the archives and,
# through ko, the two images the chart deploys. Not part of `check`: it builds
# for every platform and is minutes, not seconds.
[working-directory: 'audit']
audit-snapshot:
    goreleaser release --snapshot --clean

# Assemble the schema site into a scratch directory: proves every published
# schema's $id is the URL it is served at.
[working-directory: 'audit']
audit-pages:
    hack/pages-site.sh "$(mktemp -d)/site"

# Run Go vulnerability check
[working-directory: 'audit']
audit-vuln:
    govulncheck ./...
    cd sdk && govulncheck ./...

# The kind tier: this repository owns no cluster of its own — it installs
# onto truvity/policy's box (see that repository's hack/kind/), the second
# public repository to (docs/how-to/test-the-kind-tier.md has the full case). In CI the
# shared integration workflow stands the box up itself, via truvity/ci-actions'
# `cluster` action; on a laptop, stand truvity/policy's own box up first
# (`just cluster` there) and point KCTX/E2E_KCTX at it if it is not
# `kind-policy`.

# Build this repository's images and package its chart exactly as a release
# does, one architecture, into the box's own registry.
[doc("Build the images and chart exactly as a release does, into the local registry")]
[working-directory: 'audit']
audit-e2e-snapshot:
    bash hack/e2e-snapshot.sh

# Stand in for the platform: the database and its two roles, the stream and the
# archive bucket, under the exact names ../charts/audit/testdata/values/e2e.yaml
# gives the chart. Must run before `e2e-install`.
[doc("Provision what a platform would, by name")]
[working-directory: 'audit']
audit-e2e-fixture:
    bash e2e/fixture/apply.sh

# Install the PACKAGED chart `e2e-snapshot` produced — never the source
# directory — on top of what `e2e-fixture` provisioned.
[doc("Install the chart into the local cluster")]
[working-directory: 'audit']
audit-e2e-install:
    #!/usr/bin/env bash
    set -euo pipefail
    KCTX=${KCTX:-kind-policy}
    NS=${NS:-audit-e2e}
    RELEASE=${RELEASE:-audit-e2e}
    CHART_TGZ=${CHART_TGZ:-$(find dist/charts -name 'audit-*.tgz' 2>/dev/null | sort -V | tail -1)}
    if [ -z "$CHART_TGZ" ]; then
        echo "no packaged chart under dist/charts — run 'just e2e-snapshot' first" >&2
        exit 1
    fi
    kubectl --context "$KCTX" get namespace "$NS" >/dev/null 2>&1 || kubectl --context "$KCTX" create namespace "$NS"
    # A hook that fails is kept for debugging, and so are its pods, which is the
    # only place a failed migration says why: print them before giving up.
    if ! helm --kube-context "$KCTX" upgrade --install "$RELEASE" "$CHART_TGZ" -n "$NS" \
        -f ../charts/audit/testdata/values/e2e.yaml \
        --wait --timeout 8m; then
        echo "--- the release did not install; what the cluster says ---" >&2
        kubectl --context "$KCTX" -n "$NS" get pods,jobs >&2 || true
        kubectl --context "$KCTX" -n "$NS" describe jobs,pods >&2 || true
        kubectl --context "$KCTX" -n "$NS" logs -l "app.kubernetes.io/instance=$RELEASE" \
            --all-containers --prefix --tail=200 >&2 || true
        exit 1
    fi
    kubectl --context "$KCTX" -n "$NS" get pods

# Prove the chart works end to end — a Go suite
# (e2e/suite), reaching every Service through
# github.com/truvity/gemaal/pkg/harness. E2E_NAMESPACE turns it on;
# `go test ./...` (`just test`) stays hermetic without it.
[doc("Prove the chart works end to end")]
[working-directory: 'audit']
audit-e2e-smoke:
    E2E_NAMESPACE="${NS:-audit-e2e}" E2E_RELEASE="${RELEASE:-audit-e2e}" \
        go test ./e2e/suite/... -count=1 -v

# The whole kind tier, from a snapshot build to the suite.
[doc("The whole kind tier, from a snapshot build to the suite")]
[working-directory: 'audit']
audit-e2e-all: audit-e2e-snapshot audit-e2e-fixture audit-e2e-install audit-e2e-smoke

# Everything CI runs. `vuln` is deliberately not here: a new CVE in a
# dependency must not turn this gate red on a PR that never touched it. Run
# `just vuln` on its own to check.
[working-directory: 'audit']
audit-check: audit-build audit-test audit-lint audit-proto audit-drift audit-schemas audit-chart audit-telemetry audit-pages audit-sdk-closure audit-pulumi-test
