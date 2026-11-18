## Scope

Decisions E1 and E2 A (2026-11-18): the Lambda shape sits behind Cloudflare; sluis does its side (custom domain with mutual TLS against the Cloudflare authenticated-pull CA, the default endpoint off, cache headers; #1338 sets the key set max-age) and holds no Cloudflare credential in the core.

## Decision codes

E1, E2 A (maintainer decisions of 2026-11-18).

## Milestone

Core: lambda

Linear: mirrored.
