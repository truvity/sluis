# Test the kind tier

Prove the chart end to end on the shared kind tier: a disposable cluster with plain Postgres, NATS with JetStream, an S3 stand-in and a local registry, and no operator.

## Before you start

- Bring up the `kind-policy` cluster first with `just cluster` in truvity/policy. This repository owns no cluster. Its `hack/kind/README.md` describes the box.

- In CI the shared integration workflow stands the box up for every pull request: see the `cluster` job in `.github/workflows/ci.yaml`.

- Edit values only in `charts/audit/testdata/values/e2e.yaml`. Both `e2e/fixture` and the chart install read it, and `e2e/fixture/drift_test.go` fails on a name changed in one only.

## Steps

1. Run the whole tier.

   ```sh
   just e2e-all
   ```

   The parts it runs:

   | path | role |
   |---|---|
   | `e2e/fixture` | the database with its writer and query roles, the JetStream stream and the archive bucket; `apply.sh` provisions them and `names.go` reads the values file |
   | `e2e/suite` | the Go suite, through Service endpoints, using `github.com/truvity/gemaal/pkg/harness` |
   | `hack/e2e-snapshot.sh` | builds the images and packages the chart as a release does, for one architecture, into the box's registry |

## Verify

The suite proves three things:

- A record emitted over the writer's Service in `mode: stream` crosses JetStream to the archive and the index, and reads back through the query role (`e2e/suite/write_test.go`).

- The query role reads the index and cannot write to it (`e2e/suite/roles_test.go`).

- The chart honours every name the fixture gave it (`e2e/fixture/drift_test.go`).

## What this tier does not prove

- Verification and sealing. The v1 bucket contract is tested in `internal/bucketcontract`, against the memory store and LocalStack S3 (`just test-s3`). The seals' suite is `internal/bucketcontract/seals_test.go`.

- Tamper evidence under Object Lock. The tier installs with `lockMode: none` because LocalStack Community's Object Lock is partial, and the `security` profile demands no lock. Profiles that demand one (`pci-dss`, `nen-7513`, `dora`, `evidence-etsi`) need a real bucket with Object Lock.

- The query service's HTTP API. It authorises by a token from a trusted OIDC issuer (`query.grants.issuers`) and the box has no issuer. The write test reads the query role's Postgres view instead.

## Decided in

The design follows truvity/policy's `docs/decisions/0047-kind-is-the-gate.md`.
