// Package keys gives the products one way to ask for a cryptographic key:
// by short purpose. The caller says "sign" or "seal"; the deployment's
// configuration says which key that is; a Backend does the work.
//
// # Purposes
//
//	sign       sluis: wraps the signing ring (symmetric KMS key) or signs directly (deprecated)
//	seal       audit: seals the trail
//	pseudonym  audit: per-tenant pseudonyms (Key.MAC)
//	conceal    audit: values that must be recoverable
//	archive    audit: long-term storage
//
// # Configuration
//
//	keys:
//	  adapter: kms
//	  sign: alias/sluis-signing
//	  seal: {key: alias/audit-seal, context: default}
//	  conceal: {key: alias/audit-data, context: {app: audit}}
//
// A key is a string (its alias or transit name, default context) or
// {key, context}. Config reads and validates this; the JSON Schema at
// storage/schemas/keys.schema.json is the same statement for both products to
// $ref. ARNs and key ids are refused with a message: a file that is shared
// between deployments must not carry an account or a region, and an alias can
// be pointed at a new key without editing every file.
//
// # Encryption context
//
// The context is ON by default. The default context is
//
//	{"instance": <Options.Instance>, "purpose": <purpose>}
//
// (ContextInstance, ContextPurpose; product-neutral on purpose). It is bound
// into every Encrypt and GenerateDataKey and checked on Decrypt, so a
// ciphertext made for one deployment, or for one purpose, does not open as
// another's even when they share a key, and a key policy can condition on it
// (kms:EncryptionContext:purpose). "off" sends none. A map is sent as is.
//
// Instance is bound into ciphertexts: renaming it needs WithContext on
// decrypt for everything written before. WithContext and WithoutContext are
// also how an old ciphertext opens without being re-wrapped, for example the
// sluis key ring's {purpose: sluis-signing, alg, kid}.
//
// # Two shared keys, and their limits
//
// A deployment may point several purposes at the same key, for example
// conceal and archive at one alias, and seal and pseudonym at another. The
// context keeps their ciphertexts apart. It does not keep their fates apart:
//
//   - Disabling or deleting a shared key stops every purpose that uses it.
//     The context separates what a ciphertext opens as; it is not a separate
//     key.
//   - A caller that sends no context ("off", or legacy entries) needs a
//     key-policy statement with no kms:EncryptionContext condition, and that
//     statement allows its principal to Decrypt anything under the key
//     whatever the context. Conditions on the statements of context callers
//     do not protect against it. Give such a caller its own key.
//   - Asymmetric Sign and PublicKey take no context: KMS has none for them.
//     A signing key is separated from the others only by being a different
//     key.
//   - An alias is not a permission. Naming alias/x in a file grants nothing,
//     and the IAM grant on the key (not on the alias) decides what a role can
//     do; re-pointing an alias moves every caller to the new key.
//
// # Operations
//
// Key.Encrypt and Key.Decrypt; Key.GenerateDataKey and Key.UnwrapDataKey for
// envelope encryption (store Wrapped, use Plaintext); Key.Sign over a digest
// (SHA-384 for ES384, SHA-256 for RS256) with ToJOSE for the r||s form a JWS
// wants; Key.PublicKey; and Key.MAC for pseudonyms.
//
// MAC is HMAC-SHA-256 under a secret unique to (purpose, tenant). It is a
// separate capability, MACBackend, because a backend must either derive that
// secret (local) or hold a wrapped per-(purpose, tenant) data key (kms: a
// data key wrapped under the purpose's key with context {purpose, tenant},
// cached in memory, its wrapped form stored by the caller).
//
// # Backends
//
// keys/local (tests; refuses weak roots), keys/kms (AWS KMS: aliases only;
// Encrypt takes 4096 bytes, use data keys beyond that), keys/transit (OpenBao
// or Vault transit: key names, no aliases; the context travels as
// associated_data or as the derivation context of a derived key).
// keys/conformance is the suite every backend passes.
package keys
