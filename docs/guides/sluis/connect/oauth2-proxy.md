# Run oauth2-proxy on another gateway

Run upstream `oauth2-proxy` as a confidential client of sluis. Use it for a console with no authorization model of its own, on a gateway other than Envoy Gateway. Sluis does not ship or run the proxy. The proxy chart was removed in v1.32.0.

## Purpose

On Envoy Gateway, use [Envoy Gateway OIDC](envoy-gateway-oidc.md). For identity inside the console, use [Connect a console](console-app.md). The choice is in [choose native or gateway OIDC](choosing-native-or-gateway-oidc.md).

## Before you start


- You own the proxy: pin its image, patch it and watch it.

- Keep the cookie secret in a Secret you own. A new secret on every render signs everyone out.

- The client's `requires` gates access. `--email-domain=*` admits any signed-in person, so declare `requires`.

- The proxy cannot receive Back-Channel Logout. A revoke lands at the next refresh, bounded by `ttl_cap` and `--cookie-lifetime` (168 hours by default).

## Steps

### 1. Declare the client row

```yaml
clients:
  my-console-proxy:
    kind: confidential
    secret: my-console-proxy-oidc
    redirects: ["https://myconsole.example.com/oauth2/callback"]
    signed_out: ["https://myconsole.example.com/"]
    requires: ["all:my-console:viewer"]
```

Create the Secret that `secret` names. Run `sluisctl policy render policy/ -o policy.yaml` and read the diff: a typo in `redirects` fails at the issuer. Fields are in [policy clients](../../../reference/sluis/policy-clients.md).

### 2. Run the proxy

```bash
oauth2-proxy \
  --provider=oidc \
  --oidc-issuer-url=https://issuer.example.com \
  --client-id=my-console-proxy \
  --client-secret=<the client's secret> \
  --cookie-secret=<a random 32-byte string from your Secret> \
  --email-domain=* \
  --pass-access-token \
  --set-authorization-header \
  --skip-provider-button \
  --http-address=:4180 \
  --upstream=http://backend-service:8080/
```

Route `https://myconsole.example.com` and `/oauth2/*` on your gateway to port 4180. The backend receives `Authorization: Bearer` and verifies it with the `identity` verifier ([Connect a console](console-app.md)).

### 3. Wire sign-out

Point the console's sign-out link at the proxy with the issuer's `end_session` as `rd`:

```
GET /oauth2/sign_out?rd=https%3A%2F%2Fissuer.example.com%2Fend_session%3Fclient_id%3Dmy-console-proxy%26id_token_hint%3D<token>
```

`/oauth2/sign_out` ends only the proxy cookie. The `end_session` redirect ends the issuer sign-in. Percent-encode the inner address, or its `&` ends `rd`.

## Verify


- Open the console: it redirects to the issuer and back to `/oauth2/callback`.

- Sign out and click the console: it asks for sign-in again.

- Remove a person from `requires`: they are stopped within `ttl_cap` and `--cookie-lifetime`.

## Roll back

Route the hostname back to the backend, then remove the client row.

## Why not something else


- Envoy Gateway's OIDC filter needs no proxy: use it on Envoy.

- A proxy built into sluis puts identity code on every request path.

The decision is in [ADR 0001](../../../decisions/0001-sessions-and-an-absolute-limit.md).
