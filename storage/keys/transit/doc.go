// Package transit is the OpenBao (or Vault) transit backend of package keys.
//
// A key is named by its transit key name, as it is in the configuration:
//
//	keys:
//	  adapter: transit
//	  sign: sluis-signing
//	  conceal: {key: audit-data, context: default}
//
// OpenBao has no aliases. A name is the key itself, and rotating a key adds a
// version under the same name, so a configuration never has to change when a
// key is rotated. The transit mount, the namespace and the login belong to
// the shared [github.com/truvity/sluis/storage/openbao] client.
//
// # Operations
//
//	Encrypt, Decrypt   encrypt/<key>, decrypt/<key>
//	GenerateDataKey    datakey/plaintext/<key> (32 bytes, and the wrapped form)
//	Sign, PublicKey    sign/<key> over a digest, keys/<key>
//	MAC                hmac/<key>
//
// The ciphertext is transit's own string, "vault:v<N>:<base64>", as bytes;
// it names the key version, so a rotated key still opens what an earlier
// version sealed (min_decryption_version permitting).
//
// GenerateDataKey calls datakey/plaintext rather than datakey/wrapped: the
// wrapped endpoint returns only the ciphertext, and an envelope needs the
// plaintext too. The ACL path is transit/datakey/plaintext/<key>.
//
// # The encryption context
//
// keys passes a map (the "encryption context") to bind a ciphertext to its
// instance and purpose. Transit has no such map; it has two parameters that
// can carry one, and the backend serialises the map as canonical JSON (sorted
// keys, no white space) and sends it as base64 in one of them:
//
//   - associated_data, the AEAD's authenticated data, on a key of type
//     aes128-gcm96, aes256-gcm96, chacha20-poly1305 or xchacha20-poly1305.
//     This is the default for those keys when the key is not derived: the
//     ciphertext opens only with the same data, and nothing else about the
//     key changes.
//   - context, the key-derivation input of a derived key (created with
//     derived=true). The backend sends it for a derived key, and only that:
//     transit silently ignores context on a key that is not derived, so a
//     binding that never bound anything would read as working. The backend
//     refuses with keys.ErrUnsupported instead of sending it.
//
// The backend reads transit/keys/<key> (cached for [DefaultInfoTTL]) to
// choose between them and to refuse a key that can carry neither (an RSA
// encryption key has no associated data; transit accepts the parameter and
// ignores it). [WithBinding] forces one of the two.
//
// A caller that sends no context (the "off" mode of keys) sends neither
// parameter. A derived key cannot be used that way: transit answers that it
// needs a context.
//
// # Signing
//
// ecdsa-p384 keys sign ES384 (a SHA-384 digest) and rsa-2048/3072/4096 keys
// sign RS256, PKCS#1 v1.5 over a SHA-256 digest, both as transit's
// "prehashed" input. The ECDSA signature is ASN.1 DER, like KMS's; keys.ToJOSE
// converts it. As in the audit signer, every signature is pinned to a key
// version, read together with the public key: the version the backend read
// last (the key's latest, refreshed every [DefaultInfoTTL]), so a key rotated
// by somebody else changes the signer within a minute and never between a
// signature and the public key that goes with it. KeyVersion tells a signer
// which version that is, for naming a kid.
//
// # MAC
//
// keys.Key.MAC wants HMAC-SHA-256 under a secret unique to (purpose, tenant).
// Transit's hmac endpoint takes no context (it ignores one, with a warning),
// so a derived key cannot give a key per tenant. The backend instead keys the
// HMAC with the purpose's transit key (pinned to version 1; see
// [WithMACKeyVersion]) and prepends an unambiguous encoding of the purpose
// and the tenant to the data:
//
//	HMAC(key, "keys/mac/v1" | len(purpose) | purpose | len(tenant) | tenant | data)
//
// Two tenants (and two purposes) get unrelated outputs because the HMAC is a
// pseudo-random function of the whole input, and no (purpose, tenant, data)
// triple encodes like another. What this does not give is a per-tenant
// secret: whoever may call hmac on the key can compute every tenant's
// pseudonyms, and a policy cannot say "tenant a only" since the tenant is
// inside the input, not a parameter. For that, configure one transit key per
// tenant and purpose (for example from a naming scheme) behind a Backend of
// its own; the MAC then needs no prefix, but an ACL can allow it by path.
// Rotating the key changes nothing while version 1 is kept, which is why it
// is pinned.
//
// # Policy
//
// See the storage module's documentation for the ACL policies and for what
// allowed_parameters can and cannot pin.
package transit
