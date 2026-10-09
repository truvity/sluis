# Configure pseudonymisation keys in OpenBao

Set up an OpenBao or Vault transit engine, JWT roles and policies so the `transit` key provider can pseudonymise and crypto-shred, and every replica uses the same key.

## Before you start

- Use this only when you must crypto-shred and chose `transit`. The default is `keys.provider: none`. See [key custody](../../../concepts/audit/key-custody.md).

- You administer an OpenBao namespace that can reach the cluster's service-account issuer.

- The key that signs seals is a different key: see [signing key](../../../concepts/audit/key-custody.md#signing-key). Keys are never rotated: see [never rotated](../../../concepts/audit/key-custody.md#never-rotated).

## Steps

1. In the namespace the keys live in, create a transit engine at `openbao.mount` and a JWT auth mount for the cluster's service-account tokens. Add one JWT role per component, binding its service account as subject and `openbao` as audience.

   | component | service account (chart default) | role set in | policy |
   |---|---|---|---|
   | writer | `<release>` | `writer.config.keys.transit.openbao.login.role` | writer |
   | query service, if it resolves | `<release>-query` | `query.config.keys.transit.openbao.login.role` | resolve |

   The writer creates one key per purpose and tenant, named `<prefix>.<purpose>.<tenant>`, for example `audit.security.acme`. A purpose is a profile name with no dot.

2. Write the policies. The dot after the purpose keeps `billing` from matching `billing2`.

   ```hcl
   # writer: one pair per profile; nothing on transit/keys/
   path "transit/hmac/audit.security.*"    { capabilities = ["update"] }
   path "transit/encrypt/audit.security.*" { capabilities = ["create", "update"] }
   path "transit/hmac/audit.billing.*"     { capabilities = ["update"] }
   path "transit/encrypt/audit.billing.*"  { capabilities = ["create", "update"] }

   # single-purpose role, such as metering
   path "transit/hmac/audit.billing.*" { capabilities = ["update"] }

   # query service, if it resolves
   path "transit/decrypt/audit.security.*" { capabilities = ["update"] }

   # erasure operator: a human group, not a workload role
   path "transit/keys/audit.*"    { capabilities = ["read", "update"] }
   path "transit/encrypt/audit.*" { capabilities = ["create", "update"] }
   ```

   The writer creates keys through `encrypt`, never `transit/keys`: that grant reaches `rotate`, `config` and `trim`, which together are erasure. Grant nobody `delete` on `transit/keys/audit.*` and set no key `deletion_allowed`: a deleted key is created afresh and gives the person a second identity.

3. Configure each component to sign in with its projected service-account token. Add `tokens: [{audience: openbao, mountPath: /var/run/openbao}]` beside `config:` in the chart.

   ```yaml
   keys:
     provider: transit
     transit:
       prefix: audit
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

The provider's tests run every policy above as its own token against a dev server.

```sh
docker run -d --rm --name bao -p 8200:8200 -e BAO_DEV_ROOT_TOKEN_ID=root \
    openbao/openbao:2.4.1 server -dev -dev-listen-address=0.0.0.0:8200
AUDIT_OPENBAO_URL=http://127.0.0.1:8200 AUDIT_OPENBAO_TOKEN=root go test ./keys/ ./internal/writer/
```

Then destroy a test tenant's key. See [erase a tenant's keys](erase-a-tenants-keys.md).

## Roll back

Remove the roles and policies. A key destroyed by `audit key destroy` stays destroyed.

## Trap: backups

A snapshot taken before the trim still holds the key version, and a restore brings it back. Erasure is complete once the last such snapshot ages out. Publish that period with your retention terms.

## Decided in

[0055 No pseudonymisation keys by default](../../../decisions/0055-no-pseudonymisation-keys-by-default.md).
