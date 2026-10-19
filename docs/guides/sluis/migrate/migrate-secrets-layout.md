# Move the secrets to layout v4

Layouts v3 and v4 are both gone: v3 (`<root>/private/...`, `<root>/export/...`), `transition` and `sluis migrate secrets-layout` in v1.75, v4 (`<root>/internal/config/...`, `<root>/internal/credentials/...`) in v1.77.1. This page is the v1.74 move from v3 to v4, kept for an installation still on v3.

| On | Move with | Then |
|---|---|---|
| layout v3 or `transition` | v1.74.x, this page | layout v4 |
| layout v4 | v1.75 to v1.77.0: [upgrade to v1.75](../upgrade/v1.75.md), [v1.76](../upgrade/v1.76.md), `sluis migrate v5` of v1.77.0 | layout v5 |
| layout v5 | [upgrade to v1.77.1](../upgrade/v1.77.1.md) | v1.77.1 |

The current layout is in [storage layout](../../../reference/sluis/storage-layout.md).

## Before you start

- Run every command below with the v1.74.x binary (v1.74.1 or later), not v1.75. v1.75 refuses `secrets.layout: v3` and `transition`.
- `--config` is the document `sluis serve` reads. AWS credentials are the SDK's own.
- Reports name addresses and versions, never values.
- Nothing deletes v3 until the installation has run on v4.

## Move an installation on SSM

1. Set `secrets.layout: transition` and roll. The service writes v4 and v3, and reads v4 first.
2. Plan: `sluis migrate secrets-layout --config sluis.yaml --to v4 --dry-run`. The report lists each v3 source, its v4 address, and `new`, `unchanged` or `conflict`.
3. Copy: `sluis migrate secrets-layout --config sluis.yaml --to v4`. Each item is written, read back and compared; a mismatch exits non-zero. The command is idempotent, and it never overwrites a different document: it refuses before writing and names both sides.
4. Set `secrets.layout: v4` and roll every consumer. After a full overlap (`secrets.grace`, default 24h), check the token endpoint and the Apps.
5. Delete v3: `sluis migrate secrets-layout --config sluis.yaml --delete-v3` (with `--dry-run` first). It refuses unless `secrets.layout` is `v4`, and names any address missing at v4.

A second `--to v4` run reports `written: 0` and every item `unchanged`.

## Then upgrade

Upgrade to v1.75. Set `secrets.layout: v4` or leave it out: v4 is the default. The Pulumi library now writes the state secret and the recovery password at `<root>/internal/config/...`; the value is the same, so an apply creates the new parameters and deletes the old `private/config/...` ones.

## Roll back

Before step 5, set `secrets.layout` to `transition` or `v3` and roll. After step 5, restore from a [backup](../operate/back-up-and-restore.md).

Decided in: [0041](../../../decisions/0041-the-secret-contract.md).
