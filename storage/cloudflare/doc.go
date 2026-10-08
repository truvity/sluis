// Package cloudflare is what sluis and audit share about Cloudflare account
// tokens: the shape of a token, the naming of the tokens sluis mints, the rules a
// prototype token must pass before it is cloned (and the refusal list that is
// the only guard between a minter and everything its creator could do), the way
// R2 derives S3 credentials from a token, and [Provider], an
// aws.CredentialsProvider that keeps R2 credentials fresh by cloning a prototype
// with a minter token.
//
// It lives in the storage module because that is the one module both the root
// module and audit may import, and because the refusal list must exist once: two
// copies would diverge. It depends on the standard library, the AWS SDK's
// credentials type and the storage module's own state package; the Cloudflare
// API client here is a few HTTP calls, so the module takes no Cloudflare SDK.
package cloudflare
