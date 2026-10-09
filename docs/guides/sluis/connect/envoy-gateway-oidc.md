# Front a console with Envoy Gateway OIDC

Put an Envoy Gateway `SecurityPolicy` in front of a console. The gateway runs the sign-in and the console runs no OpenID flow. To choose this shape first, read [choosing native or gateway OIDC](choosing-native-or-gateway-oidc.md).

## Before you start


- A `SecurityPolicy` that does not attach leaves the console open. Run the check under [Verify](#verify) after every change.

- There is no Back-Channel Logout receiver. A revoke reaches the console at its next refresh, bounded by the client's `ttl_cap`.

- A very short `ttl_cap` multiplies the refresh race in [Traps](#traps).

- Field names are checked against Envoy Gateway v1.9. Check them against your release.

## Steps

### 1. Declare the client

```yaml
clients:
  myconsole.example.internal:
    kind: confidential
    secret: myconsole-oidc-client
    display_name: My Console
    redirects:  [https://myconsole.example.internal/oauth2/callback]
    signed_out: [https://myconsole.example.internal/]
    requires:   [all:myconsole:operator, all:myconsole:viewer]
    ttl_cap: 15m
```

The filter holds the secret, so the client is `confidential`. `requires` is checked at sign-in and at every refresh. Every client field is in [policy clients](../../../reference/sluis/policy-clients.md#clients).

### 2. Give the console its own `HTTPRoute`

Attach the policy to that route. A policy on a `Gateway` or shared `ListenerSet` fronts everything routed through it.

### 3. Attach the `SecurityPolicy`

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: SecurityPolicy
metadata:
  name: myconsole-oidc
  namespace: myconsole
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: myconsole
  oidc:
    provider:
      issuer: https://issuer.example.internal
      authorizationEndpoint: https://issuer.example.internal/authorize
      tokenEndpoint: https://issuer.example.internal/token
      endSessionEndpoint: https://issuer.example.internal/end_session
    clientID: myconsole.example.internal
    clientSecret:
      group: ""
      kind: Secret
      name: myconsole-oidc-client
    redirectURL: https://myconsole.example.internal/oauth2/callback
    logoutPath: /oauth2/sign_out
    refreshToken: true
    forwardAccessToken: true
    forwardIDToken:
      header: x-id-token
  jwt:
    providers:
      - name: myconsole-idtoken
        issuer: https://issuer.example.internal
        remoteJWKS:
          uri: https://issuer.example.internal/keys
          cacheDuration: 5m
        extractFrom:
          headers:
            - name: x-id-token
        claimToHeaders:
          - header: x-groups
            claim: groups
  authorization:
    defaultAction: Deny
    rules:
      - name: operators-and-viewers
        action: Allow
        principal:
          jwt:
            provider: myconsole-idtoken
            claims:
              - name: groups
                valueType: StringArray
                values: [all:myconsole:operator, all:myconsole:viewer]
```

Four fields bite:


- `endSessionEndpoint` is discovered only when you also leave the other two endpoints unset. State all three, or sign-out never ends the issuer session.

- `valueType: StringArray` is required. The default `String` never matches the array `groups` claim, so the rule never allows.

- `extractFrom` names `x-id-token`, because `Authorization` already carries the forwarded access token. `forwardIDToken.header` may not be `Authorization` while `forwardAccessToken` is `true`.

- Write the defaulted fields (`group`, `kind`, `cacheDuration`). A GitOps controller reads a field the API server fills in as permanent drift.

The backend reads the forwarded access token with the `identity` verifier ([connect a console](console-app.md)).

### 4. Sign-out

The filter serves `logoutPath` (default `/logout`). It clears the filter cookies, then redirects to `endSessionEndpoint`, which ends the issuer session. This filter serves no `/oauth2/start`: loading a protected page starts the sign-in.

## Traps

| Symptom | Cause | Fix |
|---|---|---|
| Sign-in loop or 401 under load | Each gateway replica refreshes per request and a rotating refresh token is single-use | Keep `ttl_cap` above a few minutes |
| A `backendSettings` change on one side does nothing | The token endpoint and `remoteJWKS` share a host and port, so one Envoy cluster serves both | Give the JWKS side its own `backendRefs` |
| Cookie rejected | A large `groups` claim nears the 4 KB cookie limit | Read claims from `x-id-token`, not the cookie |

Pin the JWKS cluster like this:

```yaml
remoteJWKS:
  uri: https://issuer.example.internal/keys
  backendRefs:
    - name: sluis
      port: 443
```

## Verify

```sh
curl -sD - -o /dev/null https://myconsole.example.internal/
```

A `302` to `authorizationEndpoint` means the policy is attached. A `200` means it is not. Then sign in once, and open the logout path: the issuer lists no session for the client.

## Roll back

Deleting the `SecurityPolicy` alone leaves the console open. Remove the route, then delete the policy.

## Decided in

[ADR 0001](../../../decisions/0001-sessions-and-an-absolute-limit.md), [ADR 0006](../../../decisions/0006-groups-claim-scoped-per-audience.md).
