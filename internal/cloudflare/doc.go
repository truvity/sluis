// Package cloudflare is sluis as the STS for Cloudflare: short-lived account
// API tokens and R2 credentials handed out on proof of identity, because
// Cloudflare has no web-identity federation of its own.
//
// A preset (config.CloudflarePreset) names a DISABLED prototype token whose
// policies and condition are the rights; sluis clones them into a new account
// token that expires, and either stores it for consumers to read
// (external/cloudflare/<preset>, schema cloudflare/v1) or hands it to a granted
// caller. This package is the vocabulary that the pieces share:
//
//   - [Token] and [NewToken], the two shapes of an account token the rest of
//     the code speaks, as raw JSON for what must be copied exactly;
//   - [CheckPrototype], the refusals a prototype must pass before it is cloned;
//   - the naming of minted tokens ([StoredName], [OnDemandName], [Prefix]),
//     which is the only thing that says a token is sluis's to delete;
//   - [R2Secret], how R2 derives an S3 secret from a token's value.
//
// minter does the work over an API interface; cfapi is the real client over
// cloudflare-go. docs/guides/sluis/cloudflare-tokens.md is the operator's guide.
package cloudflare
