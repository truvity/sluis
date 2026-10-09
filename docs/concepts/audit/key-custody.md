# Where do audit keys live?

Audit uses two kinds of key. Pseudonymisation keys are off by default. Signing keys sign seals and are independent of pseudonyms.

| kind | does | how many | held by | ends by |
|---|---|---|---|---|
| pseudonymisation (symmetric) | turns an identifier into a stable pseudonym and seals the identity behind it | one per tenant and purpose, created on first use | the writer makes, resolve opens | destruction, which is erasure |
| signing (asymmetric) | signs seals | one per installation | the notary only, never the writer | replacement, with the old public half kept |

## Pseudonymisation keys

`keys.provider: none` is the default: no key directory, no `identity/` prefix, and resolve refused as unimplemented. Such a deployment declares `external_identifiers_are_opaque` and skips the rest of this section.

Choose a provider only if the contract demands erasure of an identifier from a locked archive. The choice is permanent: moving keys means a new identity for every person.

The model is one key per tenant and purpose, where a purpose is a profile name. The same person, tenant and purpose always yield the same pseudonym. Two purposes yield unrelated pseudonyms.

```text
pseudonym = "ps_" + base64url( HMAC-SHA256( key[tenant, purpose], identifier ) )
```

For resolve, the writer also seals the identifier with AES-GCM under a sealing key derived from the data key. The sealed form lands at `identity/tenant=<t>/purpose=<p>/<pseudonym>`. Set `ForgetIdentities` or `--keep-identities=false` to turn this off.

| provider | where the key lives | use it for |
|---|---|---|
| `none` | no keys | anything that need not crypto-shred |
| `local` | 32-byte data keys wrapped under a 32-byte root in a Secret, as files in `keys.local.dir` | tests, a laptop, one instance |
| `transit` | a key per tenant and purpose inside an OpenBAO or Vault transit engine, named `<prefix>.<purpose>.<tenant>` | more than one replica, or secrets already in OpenBAO |
| `kms` | data keys from `GenerateDataKey` under one customer-managed KMS key, stored in the index database | AWS without OpenBAO |

With `local`, set the root with `keys.local.rootFile`. Two writers sharing a directory agree, because creation is create-once via link. Two writers with separate directories do not, and the writer refuses that. A replica count above one without `keys.local.dir` is refused. The chart's `keysVolume` holds the directory.

With `transit`, the writer creates each key on first use and signs in with the pod's projected service-account token on a JWT auth mount. Key material never leaves the engine. Policy scopes each role:

- The writer may `hmac` and `encrypt` on its purposes and touch nothing under `transit/keys`.
- The resolve role may `decrypt` and nothing else.
- Only a human eraser role reaches `rotate`, `config` and `trim`.

[OpenBAO keys](../../guides/audit/operate/configure-openbao-keys.md) has the engine set-up.

With `kms`, the encryption context names the tenant and purpose, and a processor's IAM role is scoped to the purposes it may unwrap. Set `keys.adapter: kms`, and `keys.state.backend: database` to keep wrapped secrets in your PostgreSQL. It runs through the SDK's default credential chain and needs no SSM.

## Never rotated

Rotating a pseudonymisation key gives every person a second, unrelated pseudonym. A leaked key is answered by where the key lives.

## Destroying a key

`audit key destroy --tenant <t> --purpose <p> --by <who> --reason <why>` is erasure. It refuses while a legal hold covers the tenant. It refuses without a writer to record `audit.key.destroyed`.

The pseudonyms stay in the archive but nothing can compute or match them, and every sealed identity is unreadable. The provider leaves a tombstone, checked before any key is created, so a later record cannot mint a fresh key.

With `transit`, destroy rotates the key once, raises the minimum usable version past the first and trims the first version. A tenant destroyed before it was seen gets a key made and destroyed at once.

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
aws kms create-alias --alias-name alias/audit-seal --target-key-id <key id>
```

The notary signs a SHA-384 digest (`MessageType DIGEST`, `ECDSA_SHA_384`). A key of another spec is refused at start. CloudTrail `Sign` events record when the notary signed.

`audit key public --kms-key alias/audit-seal --thumbprint` prints the RFC 7638 thumbprint a verifier pins. `--jwks` prints the JWK Set for `keys/roots.jwks`. The default is PEM.

The notary writes `keys/roots.jwks` once if the bucket has none. It refuses to sign if the file omits its key.

### Trust is the pin

An auditor trusts a key because they pinned its thumbprint, taken from you out of band. Anything under `keys/` is distribution. A verifier ignores an unpinned key, so bucket write access cannot add a root. Tell verifiers before the thumbprint changes.

### Delegation

A root can sign a statement that another key may sign seals for given profiles and tenants for at most 25 hours. `audit verify` checks the window, the scope, the limit, revocation and that only a pinned root counts. The notary does not take a delegation yet: its key must be listed in `keys/roots.jwks` as a root.

### Replacement

Replace a signing key freely. A seal names its key by thumbprint, so old seals verify while the verifier pins the old thumbprint too. Keep every public half that ever signed, and every verifier pin. The chain survives: `prev` hashes the previous seal's bytes.

## Platform identities

On Kubernetes each component has its own ServiceAccount, bindable through `serviceAccount`, `receiver.serviceAccount`, `query.serviceAccount` and `jobs.*.serviceAccount`. In stream mode the chart refuses a receiver and writer that share a ServiceAccount name.

## Decided in

- [0052 Key providers](../../decisions/0052-key-providers.md).
- [0055 No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md).
- [0061 Seals](../../decisions/0061-seals.md).
