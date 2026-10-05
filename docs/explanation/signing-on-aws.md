# Signing on AWS

How sluis signs tokens on AWS, what each mode trades, and where its trust boundary is. The key policy, the IAM
statements and the settings are in [the signing key policy](../reference/aws-signing-key.md); the library arguments
(`WrappedSigning`) are in [the Pulumi library](../reference/pulumi-library.md#inputs-lambdaargs).

The aws-serverless and aws-hybrid presets sign tokens with the `kms-wrapped`
adapter, and so does `k8s-aws`; `kms` (remote signing) stays selectable.

| | `kms` (remote) | `kms-wrapped` |
|---|---|---|
| Keys | one asymmetric KMS key per algorithm, provisioned by hand or by this library | one symmetric "application" key per estate; the issuer generates the key pairs |
| Signing | one `kms:Sign` per token; the private key never leaves KMS | local, with a private key decrypted into the process's memory (`kms:Decrypt` once per key per process) |
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
- the use of the key is **reserved** to the signing roles by the [key policy](../reference/aws-signing-key.md#the-key-policy), because the root delegation would otherwise let every role with
  `kms:Decrypt` on it, a CI role or a PowerUser, read a wrapped key out of the
  State and forge with no trace but a CloudTrail `Decrypt`.

A remote key is not extractable at all. Choose `kms` where that matters more than
rotation and quota.

**Own key or shared key.** The key is either the one the library creates (only
sluis uses it) or an existing symmetric key you pass as `WrappedSigning.KeyArn`,
which other workloads may share (an auto-unseal key, a secrets-provider key).
Both are first-class. A shared key is safe only with the reserved-context
denial of the [key policy](../reference/aws-signing-key.md#the-key-policy), merged into it.

**How it works.** For each algorithm (ES384 on `ECC_NIST_P384`, RS256 on
`RSA_3072`) the issuer calls `kms:GenerateDataKeyPairWithoutPlaintext` on the
application key and records the public key and the private key *encrypted under
it* in the key ring of the State port (`issuer:keyring:entry:<alg>:<kid>`; the
private half is the `wrapped` field and is never plaintext). To sign, a process
calls `kms:Decrypt`, keeps the key in memory and signs locally. The `kid` is a
random 128-bit id chosen before the pair is generated, and the encryption
context is `{"purpose": "sluis-signing", "alg": "<alg>", "kid": "<kid>"}`, on
both calls: a ciphertext moved under another kid or algorithm does not decrypt.

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

## Trust boundary

**A shared key.** A principal with `kms:PutKeyPolicy` on the
key (its OpenBao or Pulumi administrators) can remove these statements, so they
are inside the signing trust boundary. **A multi-Region key** needs the
statements in the key policy of EVERY replica: a wrapped key made with the
primary decrypts on any replica, and each replica's policy is its own. The
library accepts a `KeyArn` that names an `mrk-` key and does not touch any
replica's policy.

Why this is sufficient on a shared key: a wrapped signing key's ciphertext is
bound to its encryption context, so decrypting it needs a request that presents
`purpose=sluis-signing` (with the right `alg` and `kid`, which are in the State).
The statement denies every principal but the signing roles any request that
presents that purpose, so no one else can ever unwrap a signing key. The other
users of the key (an unseal, a secrets provider) present no context or different
ones, never `purpose=sluis-signing`, and are untouched. The signing roles'
own denials keep them to that context in the other direction.

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
