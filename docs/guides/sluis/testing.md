# Test sluis

Run the test tiers from the repository root with the `just` recipes.

```sh
just test            # unit and handler tests, with coverage
just test-race       # the whole module under the race detector
just test-s3         # S3 and DynamoDB adapters against LocalStack
just test-openbao    # OpenBao storage backends against a dev server
just acceptance      # a throwaway kind cluster running cmd/acceptance
just chart-lint      # chart goldens, negative fixtures, properties
just golden          # regenerate chart goldens; review the diff
just audit-catalogue # validate the audit catalogue
just check           # every recipe CI runs
just vuln            # dependency CVEs, outside check
```

## Unit and handler tests

Handler tests call the Connect handler and assert the response, so a rule that is never wired fails a test. `internal/rosterapp`, `internal/app` and `internal/issuerapp` assemble the service, and each has an acceptance suite.

## Fakes

- Backend: an in-memory Google Workspace with scriptable failures and a tenant whose domains change between probes.

- Store and cache: interfaces with in-memory implementations. The Valkey backend runs against `miniredis`.

- GitHub: an in-memory organisation with members, teams, invitations and seats.

- Slack: `internal/slackapp/slackfake`, several workspaces behind one httptest server. A test can fail any method with a code or a 429 and read back every call.

- Audit: record through `audittest`, which holds each record to the catalogue. A test that records something the catalogue refuses fails.

`internal/rails` carries the table tests for what both controllers share. The GitHub and Slack decision packages test only what is theirs.

## Acceptance

`just acceptance` creates a kind cluster, runs `cmd/acceptance` and deletes the cluster. It checks what fakes cannot: object-name validation, a create that raced another, and TokenReview. It is a command, not a `go test` package, because `go test ./...` assumes no cluster.

`demo: true` brings up two in-memory tenants and a policy that exercises every mechanic.

## Charts

`just chart-lint` renders every `tests/cases/<chart>/<case>/values.yaml` with `helm template` and compares it byte for byte with `tests/golden/<chart>/<case>.yaml` through `hack/golden.sh`. Each refusal has a fixture in `tests/invalid/<chart>/<rule>.yaml`. Its first line says why it must fail, and its second (`# error: ...`) says what the refusal reads. A new rule gets a fixture in the same change.

Update the vendored `hack/golden.sh` by copying the canonical file from the shared CI repository.

## Issuer and CLI

Run the OpenID Foundation suite by hand before a release that touches the provider ([run the conformance suite](run-conformance.md)). Results: [conformance findings](../../concepts/sluis/conformance-findings.md). Install the chart by hand against a cluster ([install](operate/install-with-helm.md)), because `helm lint` cannot start it.

## What CI runs

`just check` runs `build`, `test`, `lint`, `chart-lint`, `archive-check`, `docs-check`, `leak-canary`, `audit-catalogue` and `ts`. `.github/workflows/security.yaml` runs `just vuln` on its own. For audit action tests see [change the audit catalogue](change-the-audit-catalogue.md).
