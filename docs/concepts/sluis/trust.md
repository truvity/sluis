# What does sluis trust?

An installation has two trust anchors, and a service accepts those two and no third. When a design and this page disagree, this page wins.

## Two anchors

| Anchor | Root of trust | Proves |
|---|---|---|
| The cluster | The API server checks a ServiceAccount token bound to an audience. | Recovery only: somebody who can mint a token for the recovery account in this cluster. |
| The issuer | sluis's signing key, published as a key set. | An identity the policy resolved to internal groups: people, CI, workloads, from any cluster or cloud account. |

The issuer stands on the cluster: its workload verifier turns a ServiceAccount token into an issuer token. It checks that token against the key set its own cluster publishes, so one issuer serves many clusters and holds access to none.

## Choosing the anchor

Scope decides, not preference.

| A caller is | It presents |
|---|---|
| A workload in the same cluster as the service | Its ServiceAccount token. |
| A workload in another cluster | An issuer token. |
| A person | An issuer token from a sign-in against a corporate directory. |
| A CI job | An issuer token from exchanging the platform's identity token. |
| A laptop | An issuer token from `sluisctl` trading its own sign-in. |
| The break-glass operator | A ServiceAccount token minted by hand. |

Of the issuer's own tokens, only the access token of a `sign_in_exchange` client's sign-in, presented by that client, is a proof. An ID token never is.

A service for local workloads needs only the cluster anchor, and one for people only the issuer. A service for both keeps the proofs apart by verifier and never admits an operator call on a workload's proof alone.

## One vocabulary

Whichever anchor proved the caller, a service acts on one thing: a list of internal group names. The issuer puts them in the flat `groups` claim. A `service_account` matcher gives a ServiceAccount the same names. Every relying party binds on those strings and none re-maps them.

The claim shape is `groups` only. Kubernetes reads a flat string array, ArgoCD reads `groups`, and AWS trust policies read `aud`. A fact about where a grant came from travels inside the string, for example `C0north:sluis:viewer`. The `claims` fragment table covers a claim that is not a group, such as cloud session tags.

### Naming

Every grant is `<scope>:<thing>:<role>`: three lowercase segments joined by `:`.

| Segment | Is | Examples |
|---|---|---|
| `scope` | An environment, a workspace id or `all`. Never a domain. | `dev`, `prod`, `C0north`, `all` |
| `thing` | What the role is on. | `k8s`, `shop`, `argocd` |
| `role` | A role from that thing's own ladder. | `admin`, `viewer`, `deployer`, `operator` |

Examples: `prod:k8s:admin`, `prod:shop:deployer`, `all:sluis:operator`. A role scoped to a workspace id also gates the GitHub organisations and Slack workspaces connected under that directory.

Two-segment names are not grants. `rung:<name>` carries a session lifetime and `emp:<slug>` is a person. A cluster-scoped consumer binds `<env>:k8s:<role>` by default and owns a `thing` of its own only when its role ladder diverges.

Identity claims travel beside `groups` and are not authorization. A relying party shows them and never reads them in policy.

```text
sub  email  name  given_name  family_name  preferred_username  sid  auth_time
```

A person's `sub` is their email. A ServiceAccount's is `<cluster>:k8s:<namespace>:<name>`, so the same account on two clusters is two subjects.

## Recovery is the root, not a back door

Break-glass is a ServiceAccount token minted with `kubectl create token` for an account the chart creates bound to nobody. When the directory is broken, the issuer is unavailable and only the cluster is still trusted. Authorization is cluster RBAC: who may mint that token.

It is the only human path that bypasses the issuer. See [recovery](recovery.md).

## Consoles and APIs

A console lives on the issuer anchor only. The gateway holds the browser session and forwards the issuer's token, and the console verifies issuer and audience. It never accepts a ServiceAccount token. Two gates decide who enters: the client's `requires` at the issuer, and the gateway's `groups` posture for narrowing a route. See [the Envoy Gateway recipe](../../guides/sluis/connect/envoy-gateway-oidc.md) and [oauth2-proxy](../../guides/sluis/connect/oauth2-proxy.md).

A service with a console and an API uses two listeners, or one where every route names its proof. The API is reached by Service DNS and never by a public hostname. 
sluis serves both on one listener. The console takes the browser's session. The API takes an issuer token or a ServiceAccount token, and `service_account` matchers decide what that workload holds. The GitHub and Slack controllers are those callers.

When an API admits both anchors, the grant is keyed by the principal, so a consumer proven either way gets the same answer.

### Admission is not authorization

The directory API is not served. A consumer of it would hold a grant along three axes: which directory, which groups, which questions. Outside the grant, answers look like an unserved domain, never like a refusal. See [contracts](../../reference/sluis/contracts.md).

## What the rule rules out

The Go module offers two verifiers, `Issuer` and `Cluster`, both yielding one `Verified` with `Groups []string`. See [the Go module](../../sdk/go/sluis.md) and [the TypeScript package](../../sdk/typescript/sluis.md).

The rule forbids:

- A service verifying another cluster's key set directly.
- A shared secret between services.
- A roles claim beside `groups`.
- A library that re-maps group names.
- A console that accepts ServiceAccount tokens.
- An API listener that gates on a browser session.

## Decided in

- [ADR 0010: a declared vocabulary](../../decisions/0010-a-declared-vocabulary.md)
- [ADR 0030: workload identity on both platforms](../../decisions/0030-workload-identity-on-both-platforms.md)
- [ADR 0035: renamed to sluis](../../decisions/0035-renamed-to-sluis.md)
