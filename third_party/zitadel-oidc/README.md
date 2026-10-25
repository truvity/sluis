# zitadel/oidc, patched

A copy of [github.com/zitadel/oidc/v3](https://github.com/zitadel/oidc) at **v3.51.16**, with one line removed, the authorization endpoint's error logging changed, the ID token's audience narrowed to the client, and a public client's secret refused at the token endpoint.
The root `go.mod` wires it in with `replace github.com/zitadel/oidc/v3 v3.51.16 => ./third_party/zitadel-oidc`.

## The change

`pkg/oidc/token.go`, `NewIDTokenClaims`: the line `ClientID: clientID,` is removed, so an ID token carries no `client_id` claim.
`azp` still names the client, and the client stays in `aud`.

`client_id` is not an ID token claim in OpenID Connect Core 1.0, section 2.
It belongs in a JWT access token (RFC 9068, section 2.2), and `NewAccessTokenClaims` still sets it there.
The OpenID conformance suite warns about it (`EnsureIdTokenDoesNotContainNonRequestedClaims`).

`pkg/op/error.go`, `AuthRequestError` and `TryErrorRedirect`: the "auth request" log lines carry `error`, `reason`, `cause`, `client_id` and, where there is a request, `path`, each on one line and bounded, in place of the `oidc_error` value (which carried the request's `state`).
A missing `client_id` or `redirect_uri` is an `invalid_request`, so it logs at WARN, not ERROR.
An error with a server error anywhere in its chain still logs at ERROR.
`pkg/op/auth_request.go`, `Authorize`: those two checks return that `invalid_request` instead of a plain error.
The lines never carry the query, `state` or a PKCE challenge.
Not upstream; drop it with the directory.

`pkg/op/token.go`, `CreateIDToken`: the ID token's audience is the client alone.
Upstream passes the request's audience, which is the resource a request named (RFC 8707), and appends the client; that resource is the access token's audience and not the ID token's.

`pkg/op/token_code.go`, `AuthorizeCodeClient`, and `pkg/op/token_refresh.go`, `AuthorizeRefreshClient`: a client whose method is `none` (a public one) that presents a `client_secret`, in the body or in HTTP Basic, is refused with `invalid_client`.
Upstream ignores the secret for such a client.
Not upstream; drop them with the directory, after checking that a release does the same.

## What is copied

Only the packages sluis builds: `internal/otel`, `pkg/client`, `pkg/client/rp`, `pkg/crypto`, `pkg/http`, `pkg/oidc`, `pkg/oidc/grants/tokenexchange` and `pkg/op`, without their tests.
`go.mod`, `LICENSE` and `NOTICE` are upstream's, unchanged.
Every other file is byte-for-byte upstream's, which this command shows:

```sh
go mod download github.com/zitadel/oidc/v3@v3.51.16
up="$(go env GOMODCACHE)/github.com/zitadel/oidc/v3@v3.51.16"
cd third_party/zitadel-oidc && find . -type f ! -name README.md -exec cmp {} "$up/{}" \;
```

It prints six differences: `pkg/oidc/token.go`, `pkg/op/error.go`, `pkg/op/auth_request.go`, `pkg/op/token.go`, `pkg/op/token_code.go` and `pkg/op/token_refresh.go`.

## When it goes

The fix is not yet upstream: [zitadel/oidc issue 1020](https://github.com/zitadel/oidc/issues/1020), [pull request 1021](https://github.com/zitadel/oidc/pull/1021).
Drop this directory and the `replace` only after a zitadel/oidc release contains #1021 or an equivalent fix, then bump the require to that release.
Dropping either earlier puts `client_id` back in the ID token.
Conformance then regresses: `oidcc-server` and every module that runs `EnsureIdTokenDoesNotContainNonRequestedClaims` warn again.

A bump of the require alone leaves the `replace` behind, because the `replace` names v3.51.16.
`internal/issuer/idtoken_claims_test.go` then fails, as an upstream ID token carries `client_id` again.
To take a release without the fix, copy the same packages from it and remove the same line.

## Licence

Apache License 2.0, in [LICENSE](LICENSE), with upstream's [NOTICE](NOTICE).
`pkg/oidc/token.go` carries a comment where the line was removed.
