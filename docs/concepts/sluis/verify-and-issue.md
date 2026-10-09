# What does sluis verify and issue?

sluis verifies three kinds of proof, resolves each to internal groups, and issues tokens through six grants.

## The policy

One file holds the rules: [policy reference](../../reference/sluis/policy.md). Every proof resolves to internal groups.
People resolve through their directory groups and machines through matchers. A token carries those groups, and a client's
`requires` gates on them.

Who is in which internal group is this file, rendered from the installation's access model and reviewed in git. A console
cannot add a membership.

A policy the process rejects is a start-up failure. In a rolling update the new pod does not start while the previous pods
keep serving the previous policy. The only symptom is that the new clients are absent. `sluisctl render` therefore
validates its output with the same loader in CI.

## What it verifies

| Proof | From | How |
|---|---|---|
| a corporate sign-in | Google Workspace, Entra next | an OIDC authorization-code flow this process starts and finishes, resolved against the directory in the same process |
| a CI identity token | GitHub Actions, per organisation | token exchange against GitHub's key set; the audience must be this issuer's own URL |
| a workload token | a ServiceAccount in any cluster | token exchange against the key set that cluster publishes; one row per cluster, a name and a URL, no credential |

The **owner** allow-list is the whole trust boundary for CI tokens. Anybody gets a valid token for their own repository,
so an empty list verifies nothing. The audience check stops a token minted for a cloud provider from replaying here.

A cluster's key set is public. EKS publishes one per cluster and Talos serves it at `/openid/v1/jwks`. This is how one
issuer serves many clusters without holding a kubeconfig for each. The issuer's own cluster is a row like any other.

A key set does not notice a deleted ServiceAccount, so its token stays usable until it expires. Bound tokens last
minutes. TokenReview remains only for [recovery](recovery.md).

## What it issues

| Grant | For |
|---|---|
| authorization code + PKCE | every browser flow and every CLI: `sluisctl login` and kubelogin open a browser and listen on a loopback port |
| refresh | sessions that outlive a token |
| userinfo | relying parties that ask |
| `end_session` | sign-out ends the sign-in, not one application's cookie |
| revocation | "sign out everywhere" and the operator's revoke |
| token exchange | the one machine grant, and the CLI's re-audiencing for a cluster, AWS or another audience |

Three of the six are grants and three are endpoints. `grant_types_supported` lists three, and the endpoints are
advertised in their own fields.

Token exchange takes three subject kinds, all verified against a key set this process trusts and holds no credential for:

| Subject | Verified against | Rule kind |
|---|---|---|
| a GitHub Actions token | GitHub's key set, an owner allow-list | CI job: repository, ref, visibility |
| a ServiceAccount token from any cluster | that cluster's key set | workload: cluster, namespace, name |
| the access token of a CLI sign-in, presented by that client | our own key set and the client's `sign_in_exchange` | none: re-audiencing |

The third kind is narrow. An ID token goes to every relying party a person signs in to, so a holder could exchange it for
any audience the person's groups admit. The issuer accepts only the access token of a live session at a public client
that declares `sign_in_exchange: true`, presented by that client. AWS accepts only a token whose `aud` matches a client on
its OIDC provider, so `sluisctl` trades the token it holds for one audienced at AWS.

The protocol is the library `github.com/zitadel/oidc/v3`, certified for the Basic and Config profiles. This process
contributes the storage behind the library, the mapping from policy to token claims, the verifiers, the session index
and two small HTML pages. Absent surface is listed in [not served](not-served.md).

## Decided in

- [ADR 0002](../../decisions/0002-mission-boundary-tokens-and-memberships.md): mission-boundary tokens and memberships
- [ADR 0030](../../decisions/0030-workload-identity-on-both-platforms.md): workload identity on both platforms
