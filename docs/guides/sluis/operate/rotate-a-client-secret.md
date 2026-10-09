# Rotate a client secret

Replace the secret of a client the issuer generates, without cutting off the relying party between making the new value and deploying it.

## Before you start

- The client is declared `secret: {generate: true}` and has a stored record ([let the issuer generate it](../let-the-issuer-generate-a-clients-secret.md)).

- You are signed in with `sluisctl login` and your token holds the operators group. Only a token issued to sluisctl's default client or to `console` is accepted.

- The old secret stays valid for the overlap: 24h by default, 7d at most, `0` for a hard cut. Choose longer than the relying party's secret refresh. A rotation during an open overlap drops the older previous secret.

- Other replicas learn within 30 seconds. After a hard cut another replica may accept the old secret for up to 30 seconds.

- A replica whose store reads fail serves the last good record for about 5 minutes, then refuses the client.

- Rotated-away values stay in the store's version history, readable by whoever reads the store. Nothing here prints a secret.

## Steps

1. Rotate.

   ```sh
   sluisctl clients rotate <id> [--overlap 24h]
   ```

   The new secret is stored, the old one is accepted until the printed time, and `external/oidc/<client>` holds the new one at once. A refusal says why: the client is not generated, has no record yet, or another call is changing it (try again).

2. Confirm the relying party picked it up: its secret operator holds the new value and a sign-in works.

## Verify

`sluisctl clients show <id>` shows `rotated` as now and `previous still valid` as `true` until the overlap ends. The metric `sluis.client_secret.auth` counts matches as `current`, `previous` or `none`: `previous` should fall to zero before the overlap ends.

## Retire a generated client

A client that stops being generated is reported once as orphaned (`roster.client.secret.orphaned`) and kept. To delete it, run `sluisctl clients purge <id>`, which is refused while the policy still generates the client. Then delete the input secret `clients/<id>/secret` by hand. The issuer deletes neither. The audit records `roster.client.secret.deleted`.

## Roll back

Rotate again with a longer overlap. The audit trail records `roster.client.secret.created`, `.adopted`, `.rotated`, `.orphaned`, `.deleted` and `.denied`, never a value ([audit actions](../../../reference/sluis/audit-actions.md)). A request without an accepted token is only logged and counted as `sluis.client_secret.admin_refused`.
