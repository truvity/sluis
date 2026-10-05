# What sluis verifies and what it issues

## The policy

One file: [reference/policy.md](../reference/policy.md). Every proof resolves to internal groups (people through
their directory groups, machines through matchers), and the groups are the whole of what a token carries and the
whole of what a client's `requires` gates on.

**One layer.** Who is in which internal group is this file, rendered from the installation's own access model and
reviewed in git, and nothing else. A console that could add a membership was a second source of truth beside git and
a merge to reconcile them, so `git log` is the complete history of access.

**Validated at load.** A policy the process will not accept is a start-up failure. In a rolling update that means the
new pod does not start while the previous pods go on serving the previous policy: the ConfigMap is correct, every
Application reads Synced, and the only symptom is that the new clients are absent. That failure is invisible by
construction, so the render validates its own output with this same loader at CI time (`sluisctl render`).

## What it verifies

| Proof | From | How |
|---|---|---|
| a corporate sign-in | Google Workspace, Entra next | an OIDC authorization-code flow this process starts and finishes; the address it returns is resolved against the directory in the same process |
| a CI identity token | GitHub Actions, per organisation | token exchange, verified against GitHub's key set. The **owner** allow-list is the whole trust boundary (anybody gets a valid token for their own repository, so an empty list verifies nothing), and the audience must be this issuer's own URL, so a token minted for a cloud provider cannot be replayed here |
| a workload token | a ServiceAccount in **any** cluster | token exchange, verified against the key set that cluster publishes for its own ServiceAccount tokens. One row per cluster: a name and a URL, no credential |

The third row is what makes one issuer serve many clusters cheaply. The other way to check such a token is a
TokenReview, which means holding a kubeconfig for every cluster whose workloads may exchange, inside the service
designed to hold almost no credential. A key set is public: EKS publishes one per cluster (it is what IRSA rests on)
and Talos serves the same keys at the API server's `/openid/v1/jwks`. This process's own cluster is a row like any
other, because a special case for it would be a second code path only one installation exercises.

What is given up, plainly: a TokenReview notices a deleted ServiceAccount and a key set does not, so a token stays
usable until it expires. Bound tokens are short-lived, so the window is minutes. TokenReview survives for one thing
only, [recovery](recovery.md).

## What it issues

Six grants, and nothing else. Each exists for one of three needs: a browser reaching a web UI, a CLI on a laptop
with a browser to confirm in, and a machine that already holds a token.

| Grant | For |
|---|---|
| authorization code + PKCE | every browser flow, and every CLI: `sluisctl login` and kubelogin open a browser and listen on a loopback port |
| refresh | sessions that outlive a token |
| userinfo | relying parties that ask |
| `end_session` | sign-out ends the sign-in, not one application's cookie |
| revocation | "sign out everywhere", and the operator's revoke |
| **token exchange** | the one machine grant, and the CLI's re-audiencing for a cluster, AWS or any other audience |

Three of the six are grants and three are endpoints, so `grant_types_supported` prints three and the rest are
advertised in their own fields. Listing an endpoint as a grant type would be the metadata lying in a new way.

Token exchange takes three kinds of subject, all verified the same way, against a key set this process trusts and
holds no credential for:

| Subject | Verified against | Rule kind |
|---|---|---|
| a GitHub Actions token | GitHub's key set, an owner allow-list | CI job: repository, ref, visibility |
| a ServiceAccount token from any cluster | that cluster's key set | workload: cluster, namespace, name |
| the access token of a CLI sign-in, presented by that client | our own key set, and the client's `sign_in_exchange` | none: re-audiencing for a cluster, AWS or another audience |

The third row is narrow on purpose. An ID token is handed to every relying party a person signs in to, so a holder of
one could have exchanged it for any audience the person's groups admit. The only token of its own the issuer takes is
the access token of a live session at a **public** client that declares `sign_in_exchange: true`, presented by that
client (the CLI); everything else it signs is refused. It is why the machine grant is *one* grant: AWS accepts only a
token whose `aud` matches a client on its OIDC provider, so `sluisctl` trades the token it holds for one audienced at
AWS.

The protocol is a library, `github.com/zitadel/oidc/v3`, certified for the Basic and Config profiles. What this
process contributes is not protocol: the storage behind the library, the mapping from the policy to the claims in a
token, the verifiers, the session index, and two small HTML pages. The conformance suite proves the library is wired
correctly, not that we wrote a protocol. Surface that is deliberately absent is listed in [not served](not-served.md).
