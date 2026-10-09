# Move the secrets to layout v4

Move secrets from layout v3 (`<root>/private/...`, `<root>/export/...`) to layout v4 (`<root>/internal/...`, `<root>/external/<kind>/<id>`). `sluis migrate secrets-layout` moves an installation on SSM. `sluis migrate` brings a legacy in-cluster installation straight into v4. The layout is in [storage layout](../../../reference/sluis/storage-layout.md#layout-v4-secretslayout-v4).

## Before you start

- Nothing deletes v3 until the installation has run on v4.
- `--config` is the document `sluis serve` reads. AWS credentials are the SDK's own.
- Reports name addresses and versions, never values.

## Move an installation on SSM

### 1. Set `transition`

Set `secrets.layout: transition` and roll. The service writes v4 and v3, and reads v4 first.

### 2. Plan

```sh
sluis migrate secrets-layout --config sluis.yaml --to v4 --dry-run
```

The JSON report lists each v3 source, its v4 address, and `new`, `unchanged` or `conflict`. It skips the retired `export/` copies.

### 3. Copy

```sh
sluis migrate secrets-layout --config sluis.yaml --to v4
```

Each item is written, read back and compared; a mismatch exits non-zero.

| v3 | v4 |
|---|---|
| `private/config/<name>` (operator-seeded) | `internal/config/<name>`, the same text |
| `private/config/clients/<id>/secret` | `external/oidc/<id>`, an `oidc/v1` document |
| `private/credentials/oidc-client/<id>/secret` | `external/oidc/<id>`; a previous secret in its grace period is written first, as the previous revision |
| key of an installed runner App, or of a catalogue App with `export: true` | `external/github/<app>`, a `github/v1` document with ids from the App's State record |
| Slack App bot token | `external/slack/<id>`, a `slack/v1` document; the client secret stays internal |
| other `private/credentials/...` | `internal/credentials/...`, unchanged |

A pending App's key stays internal until installed. The `export` flags come from the policy.

The command is idempotent and completes an interrupted rotation. It never overwrites a different document: it refuses before writing and names both sides, for example `external/oidc/<id> (v3 private/config/clients/<id>/secret version 3, v4 revision 7)`. Remove the wrong side and run again.

### 4. Switch

Set `secrets.layout: v4` and roll every consumer. After a full overlap (`secrets.grace`, default 24h), check the token endpoint and the Apps.

### 5. Delete v3

```sh
sluis migrate secrets-layout --config sluis.yaml --delete-v3 --dry-run
sluis migrate secrets-layout --config sluis.yaml --delete-v3
```

It refuses unless `secrets.layout` is `v4`, and names any address missing at v4. It deletes `private/` and `export/`.

### Verify

A second `--to v4` run reports `written: 0` and every item `unchanged`. Read one document as a consumer does, with `remoteRef: {key: external/<kind>/<id>, property: <field>}`.

## Move from the legacy in-cluster store

[`sluis migrate`](migrate-state.md) writes layout v4 when the destination says `secrets.layout: v4`, with the table's v4 addresses. Credentials go under `secrets.kmsKeyId`. Nothing is written to v3, and the report's notes name the layout. A v3 destination is deprecated: the report carries `DEPRECATED`. Set `v4` before the next release removes v3.

The ring's entries copy as they are, keeping their encryption context, so old tokens verify. A destination given no ring starts a fresh one.

## Roll back

Set `secrets.layout` to `transition` or `v3` and roll. After step 5, restore from a [backup](../operate/back-up-and-restore.md).

Decided in: [0041](../../../decisions/0041-the-secret-contract.md).
