# Add an adapter in a fork

Add an adapter the [matrix](../../reference/sluis/adapters.md) lists as *on request*, or omits, as a new package that registers itself. Existing adapters do not change. Pick the concern and preset first ([choosing a deployment](../../get-started/sluis/README.md)).

## 1. Write the package

Create `internal/port/<name>` implementing the concern's port: `port.State` (optionally `port.Index` and `port.Trigger`), `port.Secrets`, `port.Blob` or `port.Trigger`. Copy the shape of `internal/port/dynamodb` or `internal/port/s3blob`.

## 2. Register it

Call `port.Register` from an `init` function with a Descriptor:

- name, concern and a one-sentence summary;

- `Requires` (AWS, Kubernetes, OpenBao) and the runtimes it works on, empty only for all;

- `ProcessLocal` if it keeps data in the process, and `SecretStore` if it is a real secret store;

- a `Factory` from its settings.

Start refuses an adapter whose requirements the platform does not meet. Import the package from `internal/store` so the process links it.

## 3. Pass the conformance suite

Call `porttest.Run` (state), `porttest.RunSecrets`, `porttest.RunExport` or your concern's suite from a `conformance_test.go` in the package.

## 4. Generate and check

```sh
just config-schemas   # schemas/config and the chart's values.schema.json
just golden           # chart goldens; review the diff
just adapters-doc     # the matrix
just check
```

Add the schema entry before `config-schemas`. Add the chart's RBAC, egress and values before `golden`; settings reach the adapter through `adapters.<concern>.settings`. Delete the adapter's entry from `port.Catalogue` so it leaves the on-request list. Add a Pulumi resource in `deploy/pulumi` if it needs cloud infrastructure. Add a line under `## Unreleased` in `CHANGELOG.md`.

Offer the adapter upstream as a PR to turn an on-request row into an implemented one.
