# Where do audit keys live?

Audit uses two kinds of key. Pseudonymisation keys are off by default. Signing keys sign seals and are independent of pseudonyms.

| kind | does | how many | held by | ends by |
|---|---|---|---|---|
| pseudonymisation (symmetric) | turns an identifier into a stable pseudonym and seals the identity behind it | one per tenant and purpose, created on first use | the writer makes, resolve opens | destruction, which is erasure |
| signing (asymmetric) | signs seals | one per installation | the notary only, never the writer | replacement, with the old public half kept |

## Pseudonymisation keys

No keys is the default (`keys` unset): no key directory, no `identity/` prefix, and resolve refused as unimplemented. Such a deployment declares `external_identifiers_are_opaque` and skips the rest of this section.

Choose a provider only if the contract demands erasure of an identifier from a locked archive. Moving keys later gives every person a new identity.

The model is one key per tenant and purpose, where a purpose is a profile name. Two purposes yield unrelated pseudonyms for one person.

```text
pseudonym = "ps_" + base64url( HMAC-SHA256( key[tenant, purpose], identifier ) )
```

For resolve, the writer also seals the identifier: under the tenant's own key with `transit`, under the `conceal` key with `kms` and `local`. The sealed form lands at `identity/tenant=<t>/purpose=<p>/<pseudonym>`. Set `forgetIdentities: true` to turn this off.

| provider | where the key lives | use it for |
|---|---|---|
| `none` | no keys | anything that need not crypto-shred |
| `local` | 32-byte data keys wrapped under a 32-byte root in a Secret, kept by the local adapter | tests, a laptop, one instance |
| `transit` | one key per profile and tenant, named `<key>.pseudonym.<tenant>`, inside an OpenBAO or Vault transit engine | more than one replica, or secrets already in OpenBAO |
| `kms` (`keys.adapter`) | data keys from `GenerateDataKey` under one customer-managed KMS key, wrapped copies in the index database or SSM (`keys.state`) | AWS without OpenBAO |

With `local`, set the root with `keys.rootFile`; the adapter is for tests and a single instance.

With `transit`, the writer creates each tenant's key on first use and signs in with the pod's projected token on a JWT auth mount. Policy scopes each role:

- The writer may `hmac` and `encrypt` on its profiles' keys, and touch nothing that rotates, configures or trims a key.
- The resolve role may `decrypt` and nothing else.
- Only a human eraser role reaches `rotate`, `config` and `trim`.

[OpenBAO keys](../../guides/audit/operate/configure-openbao-keys.md) has the engine set-up.

With `kms`, set `keys.adapter: kms` and `keys.state`: `backend: database` keeps wrapped secrets in your PostgreSQL, `backend: ssm` keeps them under `root`. Without `keys.state` the writer cannot pseudonymise.

## Never rotated

Rotating a pseudonymisation key gives every person a second, unrelated pseudonym.

## Destroying a key

`audit key destroy --tenant <t> --purpose <p> --by <who> --reason <why>` is erasure. It refuses while a legal hold covers the tenant. It refuses without a writer to record `audit.key.destroyed`.

The pseudonyms stay in the archive but nothing can compute or match them. The provider leaves a tombstone, checked before any key is created, so a later record cannot mint a fresh key. With `transit` every sealed identity is unreadable. With `kms` and `local` the provider refuses to open it, but the `conceal` key still decrypts the stored copy.

With `transit`, destroy rotates the tenant's key once, raises the minimum usable version past the first and trims the first version. The key stays as the tombstone. A tenant destroyed before it was seen gets a key made and destroyed at once.

## Signing key

The notary (`audit-notary`) signs each hourly seal ES384 with a P-384 key. The writer must have no path to that key. The chart refuses a notary that shares the writer's ServiceAccount, cloud role annotations or OpenBAO role.

| source | setup | rights |
|---|---|---|
| AWS KMS `ECC_NIST_P384`, `SIGN_VERIFY` | create once, outside the chart | notary: `kms:Sign`, `kms:GetPublicKey` on that key |
| OpenBAO transit `ecdsa-p384` | `bao write transit/keys/audit-seal type=ecdsa-p384` | notary: `update` on `transit/sign/<key>`, `read` on `transit/keys/<key>` |
| key file, P-384 PEM (PKCS#8 or SEC 1) | from a Secret | development; the private half sits beside the archive's credentials |

```sh
aws kms create-key --key-spec ECC_NIST_P384 --key-usage SIGN_VERIFY \
  --description "audit seals"
aws kms create-alias --alias-name alias/sluis-<instance>-audit-seal --target-key-id <key id>
```

The notary signs a SHA-384 digest. A key of another spec is refused at start. CloudTrail `Sign` events record when the notary signed.

Give the seal key the policy `SealKeyPolicy` writes. Only the notary's role signs. The control plane (`kms:PutKeyPolicy`, `kms:CreateGrant`, `kms:ScheduleKeyDeletion`, `kms:DisableKey`, `kms:RevokeGrant`) belongs to named key administrators and is denied to every other principal, the account root included.

`audit key public --kms-key alias/sluis-<instance>-audit-seal --thumbprint` prints the RFC 7638 thumbprint a verifier pins. `--jwks` prints the JWK Set for `keys/roots.jwks`. The default is PEM.

The notary writes `keys/roots.jwks` once if the bucket has none. It refuses to sign if the file omits its key.

### Trust is the pin

An auditor trusts a key because they pinned its thumbprint, taken from you out of band. Anything under `keys/` is distribution. A verifier ignores an unpinned key, so bucket write access cannot add a root. Tell verifiers before the thumbprint changes.

### Delegation

A root can sign a statement that another key may sign seals for given profiles and tenants for at most 25 hours. `audit verify` checks the window, scope, limit and revocation, and that a pinned root signed it. The notary does not take a delegation yet: its key must be a root in `keys/roots.jwks`.

### Replacement

Replace a signing key freely. A seal names its key by thumbprint, so old seals verify while the verifier pins the old thumbprint too. Keep every public half that ever signed, and every verifier pin. The chain survives: `prev` hashes the previous seal's bytes.

## Platform identities

On Kubernetes each component has its own ServiceAccount, bindable through `serviceAccount`, `receiver.serviceAccount`, `query.serviceAccount` and `jobs.*.serviceAccount`. In stream mode the chart refuses a receiver and writer that share a ServiceAccount name.

## Decided in

- [0047 Identity tiers and pseudonymisation](../../decisions/0047-identity-tiers-and-pseudonymisation.md).
- [0052 Key providers](../../decisions/0052-key-providers.md).
- [0055 No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md).
- [0061 Seals](../../decisions/0061-seals.md).
