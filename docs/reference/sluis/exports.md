# Exports (retired)

The exports controller, `ports.export` and the policy document's `exports` section are retired by
[ADR 0041](../../decisions/0041-the-secret-contract.md). A document that still names one is refused at load, with a
message that points here.

Nothing is copied out of sluis any more. A value a consumer needs is stored once, at an address of the public
contract, and the consumer reads it there with its own grant:

| What was exported | Where it is now |
|---|---|
| A catalogue Slack App's bot token | `external/slack/<app>`, a `slack/v1` document |
| A catalogue GitHub App (`export: true` in the catalogue) | `external/github/<app>`, a `github/v1` document |
| A runner App | `external/github/runner-<tier>-<org>`, a `github/v1` document |
| A confidential client's secret | `external/oidc/<client>`, an `oidc/v1` document |
| The recovery bundles | Retired; backup and restore are whole-installation |

The documents and their schemas are in [secrets](secrets.md#the-external-documents). A consumer reads one field with External Secrets `remoteRef: {key: <address>, property: <field>}` and names its own Secret's key. To move to the new addresses, run `sluis migrate secrets-layout` ([ADR 0041](../../decisions/0041-the-secret-contract.md#migration)).

The alerts `AccessRosterExportFailing` and `AccessRosterExportStale`, the `exportFailing` and `exportStale` chart values,
the dashboard row and the Lambda `{"kind":"exports"}` event are gone with the controller.
