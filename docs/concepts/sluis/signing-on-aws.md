# Signing on AWS

How sluis signs tokens on AWS, what each mode trades, and where its trust boundary is. The key policy, the IAM
statements and the settings are in [the signing key policy](../../reference/sluis/aws-signing-key.md); the library arguments
(`WrappedSigning`) are in [the Pulumi library](../../reference/sluis/pulumi-library.md#inputs-lambdaargs).

The aws-serverless, aws-hybrid and k8s-aws presets sign tokens with the `kms-wrapped` adapter, called **the ring**.
Direct signing with an asymmetric KMS key (`signingKey.kms`, "remote" below) is deprecated: it still works and warns at
start.

| | `kms` (remote, deprecated) | the ring (`kms-wrapped`) |
|---|---|---|
| Keys | one asymmetric KMS key per algorithm | one symmetric key the estate supplies, named by alias (`keys.sign`); sluis generates the ES384 and RS256 key pairs itself, locally |
| Signing | one `kms:Sign` per token; the private key never leaves KMS | local, with a private key decrypted into the process's memory (`kms:Decrypt` once per key per process) |
| Verifying | the published public keys | the published public keys: no KMS call, no decrypt |
| If the signing role leaks | the holder can sign only while it holds the role (every signature is in CloudTrail) | the holder can decrypt the wrapped keys in the State and sign **offline, with no further trace**, for as long as those keys are published |
| Throughput | the account's `Sign` request quota | none from KMS |
| Rotation | append a key by hand | automatic, every `rotateEvery` (default 24h), free |

The trade is deliberate, and it is larger than "until rotation": a wrapped key
is extractable by whoever may call `kms:Decrypt` on the key with the signing
context, and the ciphertext and that context are in the State. So:

- the **State's write access is part of the trust boundary**. Whoever can write
  the key ring items (`keyring`, `keyring-index`, `keyring-retired`, and the
  `lease` `signing-keygen/<alg>`) can plant a public key into the JWKS, retire a
  key early, or stop rotation, so that one key stays active and published
  indefinitely; rotation is not a bound on a compromise that includes the State;
- a signing key a leaked role has decrypted stays valid for as long as it is
  published: its life as the active key plus `retain` after it (about one
  rotation period plus the retention), **only if** the State is not also written
  to keep it;
- who may use the key is therefore part of the boundary. The sluis role's grant is pinned to the context
  `{instance, purpose: sign}`, but a grant is only what the library gives: the key's policy and the account's IAM decide
  who else may call `kms:Decrypt` on it. Keep that to the signing roles, because the root delegation would otherwise let
  every role with `kms:Decrypt` on the key, a CI role or a PowerUser, read a wrapped key out of the State and forge with
  no trace but a CloudTrail `Decrypt`. Where the key is shared with other workloads, a statement that denies the context
  `purpose=sign` to every principal but the signing roles does the same job.

A remote key is not extractable at all. Choose `kms` where that matters more than
rotation and quota.

**The key is the estate's.** The key that wraps the ring is a symmetric key the estate owns, named by alias in
`keys.sign` and in the Pulumi library's `Keys.Sign`. The library looks the alias up and grants Encrypt, Decrypt and
GenerateDataKey on the key behind it, only under the context `{instance, purpose: sign}` and no other context key; it
creates no key and no `kms:Sign` grant. The key may be shared with other workloads: the context keeps the ciphertexts
apart, and the grant is on the key and not on the alias. Letting the library create the key
(`WrappedSigning`, with its reserved-context key policy) is deprecated: [the policy it wrote](../../reference/sluis/aws-signing-key.md#legacy-wrappedsigning-the-library-creates-the-key)
is kept for stacks that still use it, and [the move to supplied aliases](../../guides/sluis/migrate/cutover.md#moving-a-stack-from-library-created-keys-to-supplied-ones)
keeps the key.

**How it works.** For each algorithm (ES384, RS256) the process generates a key pair locally, wraps the private half
with `kms:Encrypt` under the sign key and the context `{instance, purpose: sign}`, and records the public key, the
wrapped private key and that context in the key ring of the State (`issuer:keyring:entry:<alg>:<kid>`; the private half is
the `wrapped` field and is never plaintext). To sign, a process calls `kms:Decrypt`, keeps the key in memory and signs
locally. The `kid` is a random 128-bit id. `instance` is the document's `instance`, falling back to its `release`.

**Rotation.** A new pair per algorithm every `rotateEvery`, generated under the
State lease `lease.signing-keygen:<alg>` (a replica that finds it held does
nothing; a lost race costs one more published key, never a wrong token). The new
key is published in the JWKS at once and signs only after `prepublish`; a
replaced key stays published for `retain`. The defaults: `rotateEvery` 24h,
`prepublish` 15m (the issuer sends the JWKS with no `Cache-Control`, and Envoy's
`jwt_authn` caches a key set for 10 minutes and does not refetch on an unknown
`kid`, so a key must be published for longer than that before it signs),
`retain` = `lifetimes.token` + 5m (a token is valid for at most `lifetimes.token`
after it was signed, plus clock skew). The settings are checked at start:
`retain` at least that, `rotateEvery` longer than `prepublish` and at most 168h.
A process looks for work at most every `pollInterval` (30s), on a request, and
on Kubernetes also on a timer, so a Lambda with no traffic rotates at its next
request. The first key of an estate, and the first wrapped key beside keys of
another source (a migrated file or KMS key, which stay published until they
retire), is active at once: there is nothing wrapped to wait behind.

**Algorithms.** `ES384` and `RS256` (the one EKS's OIDC provider and Kargo need).
EdDSA is not supported yet: KMS can generate `ECC_NIST_EDWARDS25519` pairs and
go-jose signs EdDSA, but the policy's `signing_alg`, the issuer's verifiers and
discovery know only RSA and ECDSA; it is refused with a message that says so.

## The sign key, and moving between signers

The key that wraps the ring is `keys.sign` in the service document, by alias:

```yaml
keys:
  adapter: kms
  sign: alias/sluis-<instance>-sign      # or {key: alias/..., context: off | {..}}
```

A new entry is wrapped under the key's default encryption context
`{instance, purpose: sign}` and records that context beside the ciphertext. An entry written by
an earlier release records none and is opened with its old context
`{purpose: sluis-signing, alg, kid}`, without being re-wrapped (it was made with
`GenerateDataKeyPair`); the ring turns over within `rotateEvery` plus `retain`, so no entry has to be migrated. While
old entries exist the signing role must admit both contexts: the Pulumi library keeps the older grant while
`Keys.LegacySigningContext` is true (its default). Set it false once the ring has rotated past the old entries; a fresh
ring never needs it. Removing it early does not sign anyone out: it stops signing if the active key is an old one.
`signingKey.kmsWrapped.keyId` is the deprecated spelling of `keys.sign`: an
alias given there is mapped onto it with a warning for one release, and an ARN
or a key id is refused.

**From direct KMS signing (`signingKey.kms`) to the ring.** Direct signing is
deprecated (a warning at start). To move: export the public key of each direct
key and list it under `signingKey.verifyOnly` with its `kid` and an `until`
that covers the tokens it signed plus the verifiers' cache; replace
`signingKey.kms` with `signingKey.kmsWrapped` and `keys.sign`. The ring signs
from the first start, the old keys stay in the JWKS as verify-only until
`until`, and tokens signed before the switch keep verifying.

## Trust boundary

**A shared key.** A principal with `kms:PutKeyPolicy` on the key (the estate's key administrators) can widen who may
decrypt, so they are inside the signing trust boundary. **A multi-Region key** needs the same policy in EVERY replica: a
wrapped key made with the primary decrypts on any replica, and each replica's policy is its own.

Why a context separates the users of a shared key: a wrapped signing key's ciphertext is bound to its encryption
context, so decrypting it needs a request that presents `{instance, purpose: sign}`; the other users of the key (an
unseal, a secrets provider) present no context or different ones, and cannot open it. The context is not a permission:
it keeps ciphertexts apart, and the grant on the key keeps principals apart. The library-created key of the deprecated
`WrappedSigning` carried a policy that denied the older context `purpose=sluis-signing` to every principal but the
signing roles ([that policy](../../reference/sluis/aws-signing-key.md#legacy-wrappedsigning-the-library-creates-the-key)); an
estate-supplied key is the estate's to write.

**Writes to the key ring.** With one role (v1.63) there is no role that is denied the key ring: the controllers run in
the signing process. Whoever else has write access to the table is inside the trust boundary above.

## Known limits

Follow-ups, not in this release:

- a process publishes a key it reads from the State without having unwrapped it
  and matched it to its public half (only the active key is unwrapped, by the
  process that signs with it);
- foreign entries (public keys another source recorded) are published with no
  migration allowlist, so whoever can write the State can add one;
- the encryption context carries no creation time, so a wrapped key is not
  refused for being older than `rotateEvery` + `prepublish` + `retain`;
- a process that cannot unwrap the newest key signs with an older one, which can
  be past its `retain` (accepted: signing beats not signing; the log says so);
- the first key of a cutover is active at once, with no pre-publish (accepted: a
  verifier that cached the JWKS before it rejects the new `kid` for up to its
  cache lifetime).
