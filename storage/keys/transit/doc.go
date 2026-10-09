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
// so a derived key cannot give a key per tenant. What the backend does depends
// on the purpose:
//
//   - pseudonym: a key per tenant, see "Per-tenant keys".
//
//   - any other purpose: the HMAC is keyed with the purpose's transit key
//     (pinned to version 1; see [WithMACKeyVersion]) and an unambiguous
//     encoding of the purpose and the tenant is prepended to the data:
//
//     HMAC(key, "keys/mac/v1" | len(purpose) | purpose | len(tenant) | tenant | data)
//
// Two tenants (and two purposes) get unrelated outputs because the HMAC is a
// pseudo-random function of the whole input, and no (purpose, tenant, data)
// triple encodes like another. What this does not give is a per-tenant
// secret: whoever may call hmac on the key can compute every tenant's
// outputs, and a policy cannot say "tenant a only" since the tenant is inside
// the input, not a parameter. Rotating the key changes nothing while version 1
// is kept, which is why it is pinned.
//
// # Per-tenant keys
//
// For the pseudonym purpose each tenant has a transit key of its own, named
//
//	<key>.pseudonym.<escaped tenant>
//
// where <key> is the name configured for the purpose. Its material never
// leaves the engine: MAC is the engine's hmac on that key, and
// keys.Key.EncryptFor and DecryptFor (the sealed identifiers of audit) are
// its encrypt and decrypt, all pinned to version 1. The key is made on first
// use, through the encrypt endpoint, which makes a key when the policy grants
// create there: a writer's policy then names hmac and encrypt on <key>.pseudonym.*, and
// read on the keys, never decrypt (only a resolver opens) and nothing that
// rotates, configures or trims a key, which is erasure.
//
// The tenant is escaped by [EscapeTenant]: a-z, 0-9 and '-' stand for
// themselves, every other byte ('.', '_', '/', '@', upper-case letters, bytes
// above ASCII) is '_' and the two lower-case hex digits of the byte. '_' only
// ever starts an escape, so the map is injective: two tenants never share a
// key name (the tests walk every short string over the awkward characters).
// The name is readable ("security/acme@eu" is "security_2facme_40eu") and
// begins with the beginning of the tenant, so a policy can allow a profile by
// glob (audit.pseudonym.security_2f*). A name that would exceed transit's 128
// characters is refused, never shortened.
//
// # Erasing a tenant
//
// keys.Key.Destroy on the pseudonym purpose rotates the tenant's key once,
// sets min_decryption_version and min_encryption_version to 2, and trims
// version 1. Every pseudonym and every sealed identifier of the tenant was
// made under version 1, so none can be computed or opened again by the engine.
// A snapshot of the engine taken before the destroy still holds the key, so
// the erasure is complete once such snapshots have expired. The key stays, with
// versions nothing uses, and that state is the tombstone: calls find the key
// before they would make one, so a destroyed tenant is refused with
// keys.ErrDestroyed and never gets a fresh key (a second identity for the same
// person). Destroying a tenant never seen makes its key and destroys it at
// once. Destroy is idempotent and a second call finishes a first that stopped.
// keys.Key.Destroyed reads the tombstone.
//
// Destroy needs rights on the key itself (read, create and update on
// keys/<key>.pseudonym.*, which reaches rotate, config and trim) that the
// writer's role does not have: it is the eraser's, a human role.
//
// The other purposes (seal, conceal, archive) have one key for the
// installation and do not erase a tenant: Destroy for them is
// keys.ErrUnsupported.
//
// # Policy
//
// See the storage module's documentation for the ACL policies and for what
// allowed_parameters can and cannot pin.
package transit
