# Choose native or gateway OIDC

Decide whether an app signs itself in or a gateway signs it in. The wrong choice shows on the day someone is revoked and their console keeps answering.

## Before you start

- The installation's absolute limit (`config.lifetimes.absolute`, 24 hours from `auth_time` by default) ends the SSO session and each refresh chain. It does not end a session the app minted for itself.
- Never run both shapes on one app. Two doors double the sign-out surface.
- Sessions are explained in [sessions](../../../concepts/sluis/sessions.md).

## Choose the shape

| The app | Use | Build it |
|---|---|---|
| Needs identity inside itself: per-user authorization from `groups`, per-user audit, or tokens of its own | Native OIDC in the app | [Connect a console](console-app.md) |
| Has no authorization model of its own, on Envoy Gateway | Gateway OIDC | [Envoy Gateway OIDC](envoy-gateway-oidc.md) |
| Has no authorization model of its own, on another gateway | Upstream `oauth2-proxy` | [oauth2-proxy](oauth2-proxy.md) |

## Check the session class

Every relying party is class A or class B.

| Class | Shape | How the absolute limit reaches it |
|---|---|---|
| A | Refreshes against the issuer to stay signed in | Directly, within the token lifetime or the client's `ttl_cap` |
| B | Mints its own session after one sign-in | Only if you cap that session at 24 hours or less |

Class B is a choice the operator makes. Cap the app's own session, or accept Back-Channel Logout and its window.

| App | Class | Set |
|---|---|---|
| Gateway-fronted console | A | Nothing |
| Kargo | A | `signing_alg: RS256` ([Kargo](kargo.md)) |
| Kubernetes API server | A | Often `signing_alg: RS256` ([cluster](kubernetes-cluster.md)) |
| OpenBAO UI | A | `ttl_cap: 5m` ([issuer side](openbao-issuer-side.md)) |
| OpenBAO token from `jwt-roster` | B | The role's maximum TTL, 24 hours or less |
| ArgoCD | B | `users.session.duration` in `argocd-cm`, 24 hours or less ([ArgoCD](argocd.md)) |
| Grafana `generic_oauth` | B | `login_maximum_lifetime_duration` under `[auth]`, default 30 days |
| Headlamp | Not verified | Check whether it refreshes or mints its own session |
| `sluisctl`, kubelogin | A | Nothing |

Grafana's setting comes from Grafana's own reference, not this repository. Check it against your version. OpenBAO's role field names are in the [OpenBAO sluis integration](https://github.com/truvity/openbao/blob/master/docs/integrations/sluis.md).

## Replace the removed proxy chart

The proxy chart was removed in v1.32.0. On Envoy Gateway, use gateway OIDC. On any other gateway, run upstream `oauth2-proxy` yourself.

Neither shape receives Back-Channel Logout, because the session lives in an encrypted browser cookie. See [back-channel logout](../../../concepts/sluis/back-channel-logout.md).

## Decided in

[ADR 0001](../../../decisions/0001-sessions-and-an-absolute-limit.md), [ADR 0003](../../../decisions/0003-deprecate-access-proxy.md), [ADR 0009](../../../decisions/0009-a-default-signing-algorithm-and-per-audience-exceptions.md).
