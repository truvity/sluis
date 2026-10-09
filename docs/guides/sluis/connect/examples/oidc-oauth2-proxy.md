# Example: oauth2-proxy on a gateway that is not Envoy Gateway

## Goal

A console with no OpenID flow of its own, behind any other gateway, gets a login and a forwarded bearer. You run oauth2-proxy as a confidential client of the issuer.

## What you need

- A gateway that can route a hostname to the proxy and `/oauth2/*` to it.
- Two Secrets you own: the client's secret and the proxy's cookie secret. Do not generate the cookie secret in a template that re-renders.

## The policy snippet

```yaml
clients:
  my-console-proxy:
    kind: confidential
    secret: my-console-proxy-oidc
    redirects: ["https://myconsole.example.com/oauth2/callback"]
    signed_out: ["https://myconsole.example.com/"]
    requires: ["all:my-console:viewer"]
```

## The exchange / command

```sh
oauth2-proxy \
  --provider=oidc --oidc-issuer-url=https://issuer.example.com \
  --client-id=my-console-proxy --client-secret=<the client's secret> \
  --cookie-secret=<32 random bytes from your Secret> \
  --email-domain=* --pass-access-token --set-authorization-header \
  --skip-provider-button --http-address=:4180 --upstream=http://backend-service:8080/
```

`--email-domain=*` admits everyone the client's `requires` admits. Point the console's sign-out link at `/oauth2/sign_out?rd=` followed by the percent-encoded issuer `end_session` address. Otherwise the issuer keeps the sign-in.

## Verify

The backend receives `Authorization: Bearer` that its identity verifier accepts. After sign-out a click asks for sign-in
again. oauth2-proxy cannot receive Back-Channel Logout. A removed person is stopped at the next refresh, bounded by `ttl_cap` and `--cookie-lifetime`.

## Undo

Route the hostname back to the backend; remove the client row.

Recipe: [oauth2-proxy](../oauth2-proxy.md). Sluis does not ship or run the proxy.

Snippet source: `docs/guides/sluis/connect/oauth2-proxy.md`; the client row is accepted by `sluisctl policy render`.
