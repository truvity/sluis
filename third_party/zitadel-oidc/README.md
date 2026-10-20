# zitadel/oidc, patched

A copy of [github.com/zitadel/oidc/v3](https://github.com/zitadel/oidc) at **v3.51.16**, with one line removed.
The root `go.mod` wires it in with `replace github.com/zitadel/oidc/v3 v3.51.16 => ./third_party/zitadel-oidc`.

## The change

`pkg/oidc/token.go`, `NewIDTokenClaims`: the line `ClientID: clientID,` is removed, so an ID token carries no `client_id` claim.
`azp` still names the client, and the client stays in `aud`.

`client_id` is not an ID token claim in OpenID Connect Core 1.0, section 2.
It belongs in a JWT access token (RFC 9068, section 2.2), and `NewAccessTokenClaims` still sets it there.
The OpenID conformance suite warns about it (`EnsureIdTokenDoesNotContainNonRequestedClaims`).

## What is copied

Only the packages sluis builds: `internal/otel`, `pkg/client`, `pkg/client/rp`, `pkg/crypto`, `pkg/http`, `pkg/oidc`, `pkg/oidc/grants/tokenexchange` and `pkg/op`, without their tests.
`go.mod`, `LICENSE` and `NOTICE` are upstream's, unchanged.
Every other file is byte-for-byte upstream's, which this command shows:

```sh
go mod download github.com/zitadel/oidc/v3@v3.51.16
up="$(go env GOMODCACHE)/github.com/zitadel/oidc/v3@v3.51.16"
cd third_party/zitadel-oidc && find . -type f ! -name README.md -exec cmp {} "$up/{}" \;
```

It prints one difference, `pkg/oidc/token.go`.

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
