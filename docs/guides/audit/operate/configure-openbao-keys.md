# Configure pseudonymisation keys in OpenBao

Set up an OpenBao or Vault transit engine, JWT roles and policies so the `transit` adapter can pseudonymise and crypto-shred, with a key per tenant that no replica can mint twice.

## Before you start

- Use this only when you must crypto-shred and chose `transit`. The default is no `keys` block. See [key custody](../../../concepts/audit/key-custody.md).

- You administer an OpenBao namespace that can reach the cluster's service-account issuer.

- The key that signs seals is a different key: see [signing key](../../../concepts/audit/key-custody.md#signing-key). Keys are never rotated: see [never rotated](../../../concepts/audit/key-custody.md#never-rotated).

## Steps

1. In the namespace the keys live in, create a transit engine at `openbao.mount` and a JWT auth mount for the cluster's service-account tokens. Add one JWT role per component, binding its service account as subject and `openbao` as audience.

   | component | service account (chart default) | role set in | policy |
   |---|---|---|---|
   | writer | `<release>` | `writer.config.keys.openbao.login.role` | writer |
   | query service, if it resolves | `<release>-query` | `query.config.keys.openbao.login.role` | resolve |

   The `pseudonym` purpose has one transit key per profile and tenant, `<keys.pseudonym>.pseudonym.<escaped profile/tenant>`, created on first use. Any byte but `a-z`, `0-9` and `-` is written `_` and two hex digits, so `security/acme` is `audit-pseudonym.pseudonym.security_2facme`.

2. Write the policies. `security_2f*` ends at the escaped `/`, so it does not match `security2`.

   ```hcl
   # writer, one block per profile. It seals and pseudonymises, never decrypts,
   # and has nothing that rotates, configures or trims. It reads the key.
   path "transit/hmac/audit-pseudonym.pseudonym.security_2f*"    { capabilities = ["update"] }
   path "transit/encrypt/audit-pseudonym.pseudonym.security_2f*" { capabilities = ["create", "update"] }
   path "transit/keys/audit-pseudonym.pseudonym.security_2f*"    { capabilities = ["read"] }

   # query service, if it resolves sealed identifiers: decrypt only
   path "transit/decrypt/audit-pseudonym.pseudonym.security_2f*" { capabilities = ["update"] }
   path "transit/keys/audit-pseudonym.pseudonym.security_2f*"    { capabilities = ["read"] }

   # erasure operator, a human group. The glob on keys/ reaches rotate, config
   # and trim; encrypt create destroys a tenant that was never seen.
   path "transit/keys/audit-pseudonym.pseudonym.*"    { capabilities = ["read", "create", "update"] }
   path "transit/encrypt/audit-pseudonym.pseudonym.*" { capabilities = ["create", "update"] }
   ```

   Grant nobody `delete` on `transit/keys/` and set no `deletion_allowed`: a deleted key is created afresh and gives the person a second identity.

3. Configure each component to sign in with its projected service-account token. Add `tokens: [{audience: openbao, mountPath: /var/run/openbao}]` beside `config:` in the chart.

   ```yaml
   keys:
     adapter: transit
     instance: audit
     pseudonym: audit-pseudonym
     openbao:
       address: https://openbao.example.com:8200
       mount: transit
       login:
         mount: jwt-devel
         role: audit-writer
         jwtFile: /var/run/openbao/token
   ```

   For an engine without JWT logins, set one of `tokenFile` or `tokenSecret` instead of `jwtFile`. `audit key destroy` reads `BAO_ADDR`, `BAO_NAMESPACE`, `BAO_CACERT` and `BAO_TOKEN`, or the `VAULT_` names.

4. For a private certificate chain, set `trust.configMap` and `openbao.caFile: /etc/audit/trust/<key>`.

## Verify

The backend's tests run these policies as their own tokens against a dev server.

```sh
docker run -d --rm --name bao -p 8200:8200 -e BAO_DEV_ROOT_TOKEN_ID=root \
    openbao/openbao:2.4.1 server -dev -dev-listen-address=0.0.0.0:8200
cd storage && STORAGE_OPENBAO_ADDR=http://127.0.0.1:8200 STORAGE_OPENBAO_ROOT_TOKEN=root go test ./keys/transit/
```

Then destroy a test tenant's key. See [erase a tenant's keys](erase-a-tenants-keys.md).

## Roll back

Remove the roles and policies. A key destroyed by `audit key destroy` stays destroyed.

## Trap: backups

A snapshot taken before the trim still holds the key version, and a restore brings it back. Erasure is complete once the last such snapshot ages out. Publish that period with your retention terms.

## Decided in

[0055 No pseudonymisation keys by default](../../../decisions/0055-no-pseudonymisation-keys-by-default.md).
