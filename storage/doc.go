// Package storage is the module that holds the state and key backends of the
// products: [github.com/truvity/sluis/storage/state] (a versioned key-value
// store) and [github.com/truvity/sluis/storage/keys] (a key by purpose). This
// page is about the OpenBao (or Vault) backends and the ACL policies an
// estate writes for them. The packages are:
//
//   - [github.com/truvity/sluis/storage/openbao]: the one client both use
//     (JWT login, namespace, CA bundle).
//   - [github.com/truvity/sluis/storage/state/openbao]: state on KV version 2.
//   - [github.com/truvity/sluis/storage/keys/transit]: keys on transit.
//
// # One client, one login
//
// A process builds one [github.com/truvity/sluis/storage/openbao.Client] and
// gives it to both backends:
//
//	c, err := openbao.New(openbao.Config{
//		Address:   "https://openbao.example:8200",
//		Namespace: "team-a",           // empty: the root namespace
//		CAFile:    "/etc/ssl/bao-ca.pem", // added to the system roots
//		Login: &openbao.Login{
//			Mount:     "jwt",          // the JWT auth mount, in the namespace
//			Role:      "sluis",
//			TokenFile: "/var/run/secrets/tokens/openbao", // a projected token
//		},
//	})
//	st, err := baostate.Open(ctx, c, baostate.Config{Mount: "kv", Prefix: "sluis/state"})
//	be := transit.New(c, transit.WithMount("transit"))
//
// The token file is read at every login, because the kubelet replaces a
// projected token before it expires. The client logs in on first use (it does
// not connect when built, so a server that is down at start does not stop the
// process), keeps the token until 80% of its lease has gone, and logs in again
// once when a request is refused with 403 on a token that is not new. The
// address must be https; AllowInsecureHTTP is for a development server.
//
// The role is what an estate controls: it binds the token's subject and
// audience (the ServiceAccount, the audience the auth mount accepts) and names
// the policies below. Nothing secret is stored in the process's configuration.
//
// # The ACL policies
//
// The paths below use the mount names kv and transit, the KV prefix
// sluis/state and the key names sluis-signing and audit-data. They were
// verified against OpenBao 2.6.2 (a development server) on 2026-10-08 by the
// tests of these packages, which run under exactly these policies, not under
// the root token.
//
// State on KV version 2:
//
//	path "kv/config" { capabilities = ["read"] }                 # the max_versions check of Open
//	path "kv/data/sluis/state/*" { capabilities = ["create", "update", "read"] }
//	path "kv/metadata/sluis/state" { capabilities = ["list"] }   # List of the root store
//	path "kv/metadata/sluis/state/*" { capabilities = ["read", "list", "delete"] }
//
// Keys on transit, for a role that uses every operation on one symmetric key
// and one signing key:
//
//	path "transit/keys/audit-data" { capabilities = ["read"] }   # type, derived, versions
//	path "transit/encrypt/audit-data" { capabilities = ["update"] }
//	path "transit/decrypt/audit-data" { capabilities = ["update"] }
//	path "transit/datakey/plaintext/audit-data" { capabilities = ["update"] }
//	path "transit/hmac/audit-data" { capabilities = ["update"] }
//	path "transit/keys/sluis-signing" { capabilities = ["read"] }
//	path "transit/sign/sluis-signing" { capabilities = ["update"] }
//
// Give a role only the lines for the operations it performs. A process that
// only verifies tokens needs none of the above on the signing key, for
// example, and a writer that must not decrypt has no decrypt line. The
// backend reads transit/keys/<name> (cached for a minute) to learn the key's
// type, whether it is derived, and the latest version and public key; a role
// without that read cannot use the backend.
//
// # Pinning the encryption context in a policy
//
// keys binds a ciphertext to a context (by default {"instance", "purpose"}).
// The transit backend sends that context as one base64 string, in
// associated_data or in context (see
// [github.com/truvity/sluis/storage/keys/transit]). A policy can pin it with
// allowed_parameters, so a role that is compromised cannot encrypt or decrypt
// for another instance or purpose, and require it with required_parameters.
//
// The string is the canonical JSON of the map (keys sorted, no white space),
// base64 with padding. For {"instance":"prod","purpose":"conceal"}:
//
//	echo -n '{"instance":"prod","purpose":"conceal"}' | base64
//	eyJpbnN0YW5jZSI6InByb2QiLCJwdXJwb3NlIjoiY29uY2VhbCJ9
//
//	path "transit/encrypt/audit-data" {
//	  capabilities        = ["update"]
//	  required_parameters = ["associated_data"]
//	  allowed_parameters  = {
//	    "plaintext"       = []
//	    "associated_data" = ["eyJpbnN0YW5jZSI6InByb2QiLCJwdXJwb3NlIjoiY29uY2VhbCJ9"]
//	  }
//	}
//
// (For a derived key use "context" in both places.) Verified on 2026-10-08,
// OpenBao 2.6.2, and tested in keys/transit:
//
//   - allowed_parameters on context and on associated_data works: a request
//     with another value is 403, with the value listed it succeeds. Values are
//     compared as the exact strings sent.
//   - allowed_parameters alone does NOT require the parameter. A request that
//     leaves associated_data out is allowed (the server checks only the
//     parameters that are present), and on a key that is not derived it then
//     encrypts with no binding at all. Always add required_parameters.
//     (On a derived key the server itself refuses a missing context.)
//   - allowed_parameters lists every parameter the request may carry, so name
//     plaintext (encrypt) or ciphertext (decrypt) with an empty list, as
//     above, or the request is refused. A decrypt policy pins the same way.
//   - Do not pin integer parameters such as key_version. A list holding 1 or
//     "1" did not match a request carrying key_version 1 on this version,
//     whatever the type in the policy, and the request was refused. Pin the
//     string parameters; pin the key by the path.
//   - The signing and MAC calls send no context to pin: sign carries the
//     digest, and hmac takes no context (transit ignores one with a warning).
//     A signing key is separated from the others by being a different key,
//     as with KMS.
//
// # associated_data and context
//
// associated_data is supported by transit for AEAD keys (aes128-gcm96,
// aes256-gcm96, chacha20-poly1305, xchacha20-poly1305), on encrypt, decrypt
// and datakey, and is authenticated: a ciphertext made with one value does not
// open with another or with none ("cipher: message authentication failed").
// It also works on a derived key, where it is separate from the context that
// derives the key. It is silently ignored by key types without an AEAD (an RSA
// encryption key), and context is silently ignored by a key that is not
// derived; the backend therefore reads the key's type and refuses to send a
// binding that would bind nothing (keys.ErrUnsupported).
//
// # Running the tests
//
// The OpenBao tests need a development server and skip without it:
//
//	bao server -dev -dev-root-token-id=root -dev-listen-address=127.0.0.1:8200 &
//	STORAGE_OPENBAO_ADDR=http://127.0.0.1:8200 STORAGE_OPENBAO_ROOT_TOKEN=root \
//		hack/openbao-conformance.sh
//
// (`just test-openbao` does both.) The script fails when a test skipped or a
// required test did not run.
package storage
