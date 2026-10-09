# Layout, and how to add to it

For contributors. [CONTRIBUTING](../../../CONTRIBUTING.md#audit) has the gate and the rules. This page lists where things are. Recipes are the root Justfile's `audit-*` recipes; `just audit-check` is the gate.

## Modules

| Module | Path | Holds |
|---|---|---|
| `github.com/truvity/sluis/audit` | `audit/` | The installation: writer, query service, stores, operator command |
| `github.com/truvity/sluis/audit/sdk` | `audit/sdk/` | What an application imports to emit records. No database driver, stream server, object-store client or JWT library |
| `github.com/truvity/sluis/audit/deploy/pulumi` | `audit/deploy/pulumi/` | The AWS shape as a Pulumi library. Imports nothing of this repository |
| `github.com/truvity/sluis/storage` | `storage/` | State and keys by purpose. The only sluis module `audit` imports; the SDK and Pulumi library import none |

| Rule | Detail |
|---|---|
| Joining | `audit/go.mod` requires the SDK and storage and carries `replace` directives to the checkout. The release workflow pins the requires to the release. There is no `go.work` |
| SDK closure | `just audit-sdk-closure` fails, naming packages, when the SDK builds a server-only package. It refuses a list too short to be real |
| Pulumi tests | `just audit-pulumi-test` runs against Pulumi's mocks with the workspace off |
| Consumption | The root module is consumed as binaries, images and a chart, not as an import target |
| Release | `vX.Y.Z` for the root, and `sdk/vX.Y.Z` and `deploy/pulumi/vX.Y.Z` at the same commit. Push the root tag only; the release workflow tags the others |
| Public packages | `sdk/record`, `sdk/catalogue`, `profile`, `sdk/emit`, `sdk/sink`, `keys`, `store`, `index`, `sdk/auth`, `authn`, `writer`, `query`. Everything else is under `internal/`. Test helpers live in `*test` packages. `examples/` imports public packages only, and a test enforces it |

`writer` and `query` are public because the binaries build on them. Embedding a writer in an application is not a shape ([0053](../../decisions/0053-one-installation-per-service-or-product.md)).

## Directories

Paths are under `audit/`.

| Path | Holds |
|---|---|
| `proto/audit/v1/` | Contracts: record, sink, registry, query, seal |
| `gen/jsonschema/` | The record's JSON Schema, generated and committed |
| `schemas/config/` | Each binary's configuration schema |
| `profiles/` | Framework profiles |
| `profile/` | Profile composition and the deployment document |
| `sdk/catalogue/` | Catalogue loading, validation, composition; `common.yaml` |
| `sdk/record/` | Canonical record: identifiers, bounds, negative list |
| `sdk/emit/` | The emitter, async queue, request middleware, `Register` |
| `sdk/sink/` | Write contract, durability, Connect client, memory sinks, `logsink`, `sinktest` |
| `sdk/auth/` | `Principal`, `Authenticator`, `Authorizer`, grants, workload tokens |
| `sdk/telemetry/`, `sdk/metaschema/`, `sdk/schemas/` | Span names and interceptors; meta-schema validation; meta-schemas |
| `authn/` | JWT authenticator and the `access-roster` grants preset (legacy identifier, renamed in v1.75–v1.76) |
| `sinkserver/` | `SinkService` handler and the receiver |
| `sink/natssink/`, `sink/sqssink/` | Stream publishers |
| `dedupe/dynamodbdedupe/` | Deduplication on DynamoDB |
| `keys/` | Pseudonymisation providers and seal signers |
| `store/` | Object store interface, v1 layout, `s3store/`, `storetest/`, `routed/` |
| `index/` | Indexer and searchers: `postgres/`, `s3scan/`, `indextest/` |
| `wire/`, `writer/`, `query/` | Connect JSON codec; the writer library; the query library |
| `internal/writer/`, `internal/observe/`, `internal/query/` | Split, identity, roll, put, dead letters; cursor indexer; search, facets, get, export, resolve |
| `internal/recobj/`, `internal/merkle/`, `internal/seal/` | Record object; RFC 6962 tree; seals, delegations, revocations |
| `internal/bucketcontract/` | Bucket-contract checker and conformance suite |
| `internal/identity/`, `internal/hold/`, `internal/registry/`, `internal/clock/` | Sealed identities; legal holds; registered catalogues; SNTP client |
| `internal/telemetry/`, `internal/cli/`, `internal/config/` | OTLP export; commands; configuration types, loader and schema generator |
| `internal/s3test/`, `internal/pgtest/`, `internal/authtest/`, `internal/corpus/`, `internal/schemagen/` | Test S3; test database; token issuers; record corpus; JSON Schema generator |
| `cmd/audit/` | Operator command: `validate`, `check-emitters`, `messages`, `profile`, `verify`, `conformance`, `replay`, `migrate`, `reindex`, `purge`, `clock-sync`, `hold`, `key` |
| `cmd/audit-writer/`, `cmd/audit-observe/`, `cmd/audit-query/`, `cmd/audit-notary/` | Receiver and writer; indexer; query service; notary |
| `cmd/audit-writer-lambda/`, `cmd/audit-notary-lambda/` | The writer and notary as Lambda functions |
| `cmd/protoc-gen-audit-jsonschema/` | buf plugin for the record's JSON Schema |
| `ts/`, `react/` | `@truvity/audit` client; `@truvity/audit-react` hooks and default MUI view |
| `examples/`, `testdata/`, `e2e/`, `hack/` | `emit` and `read` examples; record corpus; end-to-end suites; dashboards and snapshot scripts |
| `charts/audit/` (repository root) | The chart an application's own chart instantiates |

## Tests

| Recipe | Needs | Runs |
|---|---|---|
| `just audit-check` | The checkout | Build, unit tests, lint, proto, drift, schemas, chart, telemetry, SDK closure, Pulumi tests |
| `just audit-race` | A C toolchain | Tests under the race detector |
| `just audit-test-postgres` | Postgres (under `.devbox`) | Index, dedupe table, writer against a database |
| `just audit-test-s3` | Docker (LocalStack) | Archive walks, Object Lock refusal, KMS signing |
| `just audit-conformance` | Docker, Postgres | Everything, with Postgres, LocalStack and OpenBao |
| `just audit-chart` | The checkout | Chart goldens and refusals |
| `just audit-ts` | The npm registry | TypeScript typecheck, tests, build, publish contents |

A test skips when its service is absent. CI runs each service in a job that fails if the tests skipped.

## How to add

| Addition | Steps |
|---|---|
| Action in the common catalogue | Add it to `sdk/catalogue/common.yaml` with underscore template arguments. Emit it with the name as a literal. Run `just audit-schemas` and `just audit-ts-sentences` |
| Searcher | Implement `index.Searcher`. Declare what it cannot do in `Capabilities`; the suite requires a refusal, not a narrower answer. Return `index.ErrNotFound` for a missing record. Run `indextest.Run(t, "name", searcher)` |
| Key provider | Implement `keys.Provider` (and `keys.Sealer` for resolve). It must be stable across replicas and restarts, separate tenants and purposes, never mint a key for an erased (tenant, purpose), and be tested against the real service. Add it to the `keys` block of `internal/config/schema/schema.go`, run `just audit-config-schemas`, and add loader refusals |
| Signer | Implement `keys.Signer`. `KeyID` names the key version so a verifier can pick the public half |
| Configuration key | Add it to `internal/config/types.go` and `internal/config/schema/schema.go`, run `just audit-config-schemas`, and document it in the [configuration reference](configuration.md). The chart passes it through `config:` |
| Chart value | For platform-only values: add it to `values.yaml` with a comment, use it in the templates, add a refusal under `tests/invalid/audit/` for any combination the binary rejects, and run `just audit-chart` to update the goldens |
| Command | A function in `cmd/audit/main.go` that parses flags and calls a type in `internal/cli`, tested there. A scheduled command also takes `--config` with a schema in `schemas/config/`. Add it to the usage text |
