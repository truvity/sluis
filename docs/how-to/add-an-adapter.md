# Adding an adapter in a fork

An adapter you need that the [matrix](../reference/adapters.md) shows as *on
request* (or that is not there at all) is an extension: a new package that
registers itself. Nothing in the existing adapters changes. sluis is MIT-licensed,
so a fork may add it and run it.

Read [choosing a deployment](../getting-started/README.md) first to see which concern
and preset the adapter belongs to.

## The checklist

1. **The adapter package** under `internal/port/<name>`. It implements the
   concern's port: `port.State` for state (optionally `port.Index` and
   `port.Trigger`), `port.Secrets` for secrets, `port.Blob` for blobs,
   `port.Trigger` for trigger. Look at `internal/port/dynamodb` or
   `internal/port/s3blob` for the shape.
2. **`port.Register` with an honest Descriptor**, from an `init` function: name,
   concern, a one-sentence summary, what it `Requires` (AWS, Kubernetes,
   OpenBao), the runtimes it works on (leave empty only if it works on all),
   `ProcessLocal` if it keeps data in the process, `SecretStore` if it is a real
   secret store, and a `Factory` from its settings. Start refuses an adapter whose
   requirements the platform does not meet, so an optimistic descriptor only moves
   the failure into production. Import the package from `internal/store` so the
   process links it.
3. **Pass the port's conformance suite**: `porttest.Run` (state),
   `porttest.RunSecrets`, `porttest.RunExport` or the suite of your concern, from
   a `conformance_test.go` in your package. An adapter that does not pass it is not
   an adapter.
4. **The config schema entry**, then `just config-schemas`. The schemas under
   `schemas/config` and the chart's `values.schema.json` are generated and
   checked for drift.
5. **The chart's values and templates**: RBAC and network egress the adapter needs, with the goldens
   (`just golden`; review the diff). Settings reach the adapter through the service document's `adapters.<concern>.settings`.
6. **A Pulumi resource in `deploy/pulumi`** if it needs cloud infrastructure (a
   table, a queue, a key, a role grant).
7. **Regenerate the matrix**: `just adapters-doc`. Your adapter appears as
   implemented, and leaves the on-request catalogue (`port.Catalogue`): delete its
   entry there. `just docs-check` fails while the committed matrix is stale.

Then add an entry under `## Unreleased` in `CHANGELOG.md`, and run `just check`.

## Offer it upstream

Offer it upstream as a PR; that's how an on-request row becomes implemented.
