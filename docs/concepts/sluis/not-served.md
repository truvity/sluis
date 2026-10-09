# What is not served?

Each entry shipped or was asked for, and is not here. Check the reason before you add one back.

## What was removed, and why

| Removed | Reason |
|---|---|
| the directory's API listener and the TokenReview between the two services | one consumer, now the same process. The endpoint returns when a second consumer exists |
| the console layer of the policy and the `memberships` table | a second source of truth beside git. The key is refused now, not ignored |
| `SetOAuthClient` | the client is a Secret, delivered like every other credential |
| the `/account` page | the console is same-origin and already shows both halves |
| the device flow | both headless cases, a CI job and a workload, use token exchange |
| client credentials | a machine with a stored secret is what this design avoids |
| a client secret an operator makes up and delivers | the issuer generates it (`secret: {generate: true}`): [how to](../../guides/sluis/let-the-issuer-generate-a-clients-secret.md). The named input stays |
| JWT bearer (RFC 7523) | token exchange with a different spelling |
| introspection (RFC 7662) | the tokens are JWTs, verified offline against the key set |
| dynamic client registration (RFC 7591) | an endpoint that mints trust. A client this installation does not deploy identifies itself with a Client ID Metadata Document, and an allow-list of origins guards it |
| the implicit and hybrid flows | superseded by code with PKCE |
| take over from git for a Slack channel | one channel would have two definitions. A channel defined in both places is held: see [the Slack reconciler](slack-reconciler.md) |
| `slack.workspaces.<key>.team_id`, `.domains`, `.owner` and `github.<org>.owner` in the policy | each is known at run time. The loader refuses them and names the new source |
| a Slack Connect record's `from` internal groups | Slack Connect channels are fed by directory groups. A record with internal groups is listed `invalid` |
| guest-side probe of every shared channel | it cost a call per other workspace per unmanaged channel per pass. Only managed channels are probed |
| TokenReview for workload exchange | it needs a kubeconfig per cluster. A published key set needs none. It stays for [recovery](recovery.md) alone |
| the separate controller processes and the proxy chart | one process runs everything. A console with no OpenID flow of its own uses [gateway-native OIDC or upstream oauth2-proxy](../../guides/sluis/connect/oauth2-proxy.md) |

## What was never served?

The session-management iframe, front-channel logout, PAR, DPoP, mTLS and CIBA. Each is surface without a consumer.
Back-channel logout is served for a client that runs its own session and opts in:
see [back-channel logout](back-channel-logout.md).

## Decided in

- [ADR 0037](../../decisions/0037-one-process-everywhere.md): one process everywhere
- [ADR 0039](../../decisions/0039-the-issuer-generates-confidential-client-secrets.md): the issuer generates confidential client secrets
