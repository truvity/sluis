# Expose a business surface to employees for testing

Open a product surface on a test tier to employees without teaching the product about the issuer. The product's end users keep signing in with the product's own identity provider.

## Before you start

- On Envoy Gateway, use gateway-native OIDC: a `SecurityPolicy` with `oidc:` against a declared client ([Envoy Gateway OIDC](envoy-gateway-oidc.md)).
- On any other gateway, run upstream oauth2-proxy ([recipe](oauth2-proxy.md)).

## 1. Declare the client

```yaml
clients:
  app.test.example.internal:
    kind: confidential
    secret: app-test-client
    redirects: ["https://app.test.example.internal/oauth2/callback"]
    requires: []  # any signed-in employee
```

An empty `requires` admits any signed-in identity. No internal group is needed.

## 2. Route the hostname

Route the hostname to the console backend and gate the route with a `SecurityPolicy` on this client. The gateway forwards the bearer to the app. The application authorises itself or ignores the identity.

## Verify

Open the hostname in a browser. It redirects to the issuer, and after sign-in you reach the app.
