# Move the secrets to layout v4

## Purpose

Move an installation's secrets from layout v3 (`<root>/private/...`, plus the exports controller's `<root>/export/...`
copies) to layout v4 (`<root>/internal/...` and the typed documents at `<root>/external/<kind>/<id>`,
[0041](../../../decisions/0041-the-secret-contract.md)), and bring a legacy in-cluster installation straight into v4. There are
two commands: `sluis migrate secrets-layout` for an installation that already keeps its secrets in SSM, and `sluis
migrate` for one that does not. The layout itself is in [storage layout](../../../reference/sluis/storage-layout.md#layout-v4-secretslayout-v4)
and [secrets](../../../reference/sluis/secrets.md#ssm-layout-v4).

## Move an installation on SSM

The steps are separate on purpose: nothing deletes v3 until the installation has run on v4.

1. **Set `secrets.layout: transition`** in the installation document and roll it. The service now writes every secret to v4
   and then to v3, and reads v4 first, so what changes while you copy is written to both.
2. **Look at the plan.** The configuration is the document `sluis serve` reads; the AWS credentials are the SDK's own.

   ```sh
   sluis migrate secrets-layout --config sluis.yaml --to v4 --dry-run
   ```

   The report is JSON on stdout: each v3 source, the v4 address it goes to, and whether that address is `new`,
   `unchanged` or a `conflict`. It names addresses and versions and never a value. The exports copies under `export/` are
   listed as skipped: the exports are retired, so they are not copied.
3. **Copy.**

   ```sh
   sluis migrate secrets-layout --config sluis.yaml --to v4
   ```

   Every item is written to its address, read back and compared; the run ends non-zero if any does not match. What goes
   where:

   | v3 | v4 |
   |---|---|
   | `private/config/<name>` (operator-seeded) | `internal/config/<name>`, the same text |
   | `private/config/clients/<id>/secret` | `external/oidc/<id>`, an `oidc/v1` document |
   | `private/credentials/oidc-client/<id>/secret` (a generated client) | `external/oidc/<id>`; a previous secret still inside its grace period is written first, so it is the document's previous revision |
   | the key of an installed runner App, or of a catalogue App with `export: true` | `external/github/<app>`, a `github/v1` document whose ids come from the App's record in State |
   | a Slack App's bot token | `external/slack/<id>`, a `slack/v1` document; the client secret stays internal |
   | every other `private/credentials/...` item | `internal/credentials/...`, unchanged |

   An installed App's key is kept once, at its external address; a pending App's key stays internal until it is
   installed. The catalogue's `export` flags are read from the policy the document names.
4. **Re-run after any failure.** The command is idempotent and resumable: it plans again, skips every address that already
   holds an identical document and writes only what is missing. An address that holds a *different* document is never
   overwritten. The run refuses before writing anything and names both sides, for example
   `external/oidc/<id> (v3 private/config/clients/<id>/secret version 3, v4 revision 7)`. Decide which is right, remove the
   other, and run again. A rotation interrupted between its two writes is completed, not refused.
5. **Switch.** Set `secrets.layout: v4` and roll every consumer. Run for a full overlap period (`secrets.grace`, default
   24h) and check the token endpoint and the Apps before the next step.
6. **Delete v3, later.**

   ```sh
   sluis migrate secrets-layout --config sluis.yaml --delete-v3 --dry-run
   sluis migrate secrets-layout --config sluis.yaml --delete-v3
   ```

   It refuses unless `secrets.layout` is `v4`, and refuses naming the addresses if any v3 item has nothing at its v4
   address. It deletes `private/` and the retired `export/` copies.

**Rollback** is to set `secrets.layout` back to `transition` (or `v3`) and roll. Until step 6 v3 is untouched and, in
`transition`, still written, so it holds everything. After step 6 there is no v3 to return to: restore from a backup
instead ([back up and restore](../operate/back-up-and-restore.md)).

## Move from the legacy in-cluster store

`sluis migrate --from <legacy.yaml> --to <new.yaml>` ([the command](migrate-state.md)) writes the secrets through the
destination's Secrets, so it writes layout v4 whenever the destination's installation document says `secrets.layout: v4`:
credentials go to `internal/credentials/...` under the destination's `secrets.kmsKeyId` (the key alias every v4 address is
encrypted with), an installed runner App and an exported catalogue App to `external/github/<app>`, and a Slack App's bot
token to `external/slack/<id>`. The destination's policy supplies the `export` flags. Nothing is written to v3, and there
is no second step. The report's notes say which layout it wrote.

Layout v3 is still accepted as a destination for this release. The report then carries a `DEPRECATED` note and the log a
warning; set `secrets.layout: v4` on the destination before the next release removes it.

**The signing ring.** The ring's entries are State records and are copied as they are: each keeps the encryption context
it was wrapped under, so a token issued before the move keeps verifying. A destination that is given no ring, because the
source has none or `--skip issuer` was passed, starts a fresh ring on its first start, under its own context. A direct
migration from a legacy installation to a v4 destination therefore starts its ring fresh unless the source ring is copied;
the key ring's public halves are published through the overlap either way.

## Checks

- After `--to v4`, the second run reports `written: 0` and every item `unchanged`.
- `sluis migrate secrets-layout --to v4 --dry-run` prints no value: the report is safe to attach to a ticket.
- Read one external document the way a consumer does, with its `remoteRef: {key: external/<kind>/<id>, property: <field>}`.
