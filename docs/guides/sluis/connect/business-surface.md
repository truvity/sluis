# Expose a business surface to employees for testing

Open a product surface on a test tier to employees without teaching the product about the issuer. The product's end users keep signing in with the product's own identity provider.

## Before you start

- On Envoy Gateway, use gateway-native OIDC: a `SecurityPolicy` with `oidc:` against a declared client ([Envoy Gateway OIDC](envoy-gateway-oidc.md)).
- On any other gateway, run upstream oauth2-proxy ([recipe](oauth2-proxy.md), [ADR 0003](../../../decisions/0003-deprecate-access-proxy.md)).

## 1. Declare the client

```yaml
groups:
  all:app-test:user: { members: [everyone@example.com] }  # every employee
clients:
  app.test.example.internal:
    kind: confidential
    secret: app-test-client
    redirects: ["https://app.test.example.internal/oauth2/callback"]
    requires: [all:app-test:user]
```

`requires` cannot be empty: sluis refuses to start on a client that requires no group. Gate on one group that holds every employee.

## 2. Route the hostname

Route the hostname to the surface's backend and gate the route with a `SecurityPolicy` on this client. The gateway forwards the bearer to the app. The application authorises itself or ignores the identity.

## Verify

Open the hostname in a browser. It redirects to the issuer, and after sign-in you reach the app.
