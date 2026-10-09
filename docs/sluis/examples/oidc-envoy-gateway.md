# Example: Envoy Gateway's OIDC filter in front of a console

## Goal

Put a login, a session and a forwarded bearer in front of a console that cannot run OpenID itself, with no proxy to run.

## What you need

- Envoy Gateway (the field names below are checked against v1.9), a Secret holding the client's secret, and an `HTTPRoute`
  the console owns (a `SecurityPolicy` targets a route, never a hostname).

## The policy snippet

```yaml
clients:
  myconsole.example.internal:
    kind: confidential          # the filter holds the secret
    secret: myconsole-oidc-client
    display_name: My Console
    redirects:  [https://myconsole.example.internal/oauth2/callback]
    signed_out: [https://myconsole.example.internal/]
    requires:   [all:myconsole:operator, all:myconsole:viewer]
    ttl_cap: 15m
```

## The exchange / command

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: SecurityPolicy
metadata: {name: myconsole-oidc, namespace: myconsole}
spec:
  targetRefs:
    - {group: gateway.networking.k8s.io, kind: HTTPRoute, name: myconsole}
  oidc:
    provider:
      issuer: https://issuer.example.internal
      authorizationEndpoint: https://issuer.example.internal/authorize
      tokenEndpoint: https://issuer.example.internal/token
      endSessionEndpoint: https://issuer.example.internal/end_session
    clientID: myconsole.example.internal
    clientSecret: {group: "", kind: Secret, name: myconsole-oidc-client}
    redirectURL: https://myconsole.example.internal/oauth2/callback
    logoutPath: /oauth2/sign_out
    refreshToken: true
    forwardAccessToken: true
```

State all three endpoints together: `endSessionEndpoint` is only discovered when the other two are left unset. The full
policy with its `jwt` and `authorization` blocks is in the recipe.

## Verify

Opening the console redirects to the issuer and back to `/oauth2/callback`; the backend receives `Authorization: Bearer`.
A person removed from `requires` is stopped at the next refresh, bounded by `ttl_cap`; there is no Back-Channel Logout here.

## Undo

Delete the `SecurityPolicy`, then the client row.

Recipe and its traps: [Envoy Gateway OIDC](../../how-to/connect/envoy-gateway-oidc.md); choosing:
[native or gateway OIDC](../../how-to/connect/choosing-native-or-gateway-oidc.md).

Snippet source: `docs/how-to/connect/envoy-gateway-oidc.md`; the client row is accepted by `sluisctl policy render`.
