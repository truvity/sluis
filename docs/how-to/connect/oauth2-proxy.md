# Run oauth2-proxy on a gateway that is not Envoy Gateway

Sluis does not ship or deploy oauth2-proxy. The `access-proxy` chart that wrapped it was removed in v1.32.0
([ADR 0003](../../decisions/0003-deprecate-access-proxy.md)). This page is a recipe for running the upstream
`oauth2-proxy` yourself, as a confidential client of sluis, in the one case nothing else covers: a console with no
authorization model of its own, behind a gateway that cannot run OIDC itself.

Pick the door first:

- **Gateway-native OIDC** on Envoy Gateway runs the code flow in the gateway, with no proxy to run:
  [Envoy Gateway OIDC](envoy-gateway-oidc.md). This is the default for a console that cannot run OpenID itself.
- **Native OIDC in the application** is the right answer when the console needs identity inside itself (per-user
  authorization, per-user audit, tokens of its own): [Connect a console](console-app.md).
- **This page**: any other gateway, a console with no OpenID flow of its own.

## Purpose

Put a login against sluis, a session that outlives a token, and a bearer forwarded to the backend in front of a console,
using upstream oauth2-proxy and one declared client row.

## Preconditions

- A gateway that can route a hostname to a Service and forward `/oauth2/*` to the proxy.
- A console backend reachable from the proxy.
- Permission to change the policy (`clients`) and to hold two Secrets: the client's secret and the proxy's cookie secret.
- The proxy is yours: you pin its image, run it, patch it and watch it. Nothing in sluis does.

## Before you start

- **Keep the cookie secret in a Secret you own.** A new cookie secret on every render signs everyone out. Do not
  generate it in a template that re-renders.
- **The issuer gates access, not the proxy.** `--email-domain=*` is deliberate: admission is the client's `requires`.
  Without a `requires`, any signed-in person gets in.
- **oauth2-proxy cannot receive Back-Channel Logout.** It encrypts each session with a key that lives only in the
  browser's cookie, so nothing server-side can open one. A revoked person is stopped when the proxy next refreshes:
  bounded by the client's `ttl_cap` and by the proxy's `--cookie-lifetime` (168 hours by default). Read
  [sessions](../../explanation/sessions.md) and [back-channel logout](../../explanation/back-channel-logout.md) before promising an operator anything shorter.
- **Declare the client; there is no self-registration.** An endpoint that mints clients is the surface an issuer least
  wants, and a declaration keeps *who can obtain tokens for which audience* answerable from the repository.
- **Preview the policy before you roll it out** (`sluisctl policy render policy/ -o policy.yaml`, then read the diff): a typo in `redirects` is a
  sign-in that fails at the issuer, not in the proxy.

## Steps

### 1. Declare the client row

**Run**: add a confidential client to the policy's `clients` table, keyed by its id.

```yaml
clients:
  my-console-proxy:
    kind: confidential
    secret: my-console-proxy-oidc
    redirects: ["https://myconsole.example.com/oauth2/callback"]
    signed_out: ["https://myconsole.example.com/"]
    requires: ["all:my-console:viewer"]
```

**Expect**: the policy loads. The secret named by `secret` is the one you create next.

**Verify**: render the policy (`sluisctl policy render`) and read the client's row ([policy clients](../../reference/policy-clients.md)).

**Rollback**: remove the row; the proxy's sign-in then fails at the issuer and admits nobody.

### 2. Run upstream oauth2-proxy

**Run**:

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

`--oidc-issuer-url` is sluis's root URL; `--client-id` and `--client-secret` are the row's id and the Secret its
`secret` names; `--pass-access-token` and `--set-authorization-header` forward the bearer to the backend. Route
`https://myconsole.example.com` on your gateway to the proxy at `:4180`.

**Expect**: opening the console redirects to the issuer's sign-in and back to `/oauth2/callback`.

**Verify**: the backend receives an `Authorization: Bearer` header that its `identity` verifier accepts
([Connect a console](console-app.md)).

**Rollback**: route the hostname back to the backend, or remove the route.

### 3. Wire sign-out

Two halves, both needed. `/oauth2/sign_out` ends this proxy's session, one application's cookie. The issuer still holds
the sign-in, so on its own that leaves the next click admitted again with no password. The proxy must redirect to the
issuer's `end_session`, which ends the sign-in and every session that browser opened. The inner address is
percent-encoded, or the `&` would end `rd`.

**Run**: point the console's sign-out link at

```
GET /oauth2/sign_out?rd=https%3A%2F%2Fissuer.example.com%2Fend_session%3Fclient_id%3Dmy-console-proxy%26id_token_hint%3D<token>
```

**Expect**: the issuer's logout lands back on the `signed_out` address you declared.

**Verify**: after sign-out, a click on the console asks for sign-in again.

**Rollback**: none, because it is a link in your console; restore the old link.

## Afterwards

- Check that a person removed from `requires` is stopped within `ttl_cap` and `--cookie-lifetime`.
- Pin the proxy image and put it on your own update path.
- Tell the console's owners that sign-out is two halves and revocation is bounded by the refresh, not instant.

## Why not something else

- **Not Envoy Gateway's native OIDC filter**, if you are on Envoy: it runs the code flow, keeps a session in its own
  cookie, forwards the bearer and refreshes in the background, all from configuration. Use that.
- **Not a proxy of sluis**: identity-critical code on every request path, for no capability oauth2-proxy lacks
  ([ADR 0003](../../decisions/0003-deprecate-access-proxy.md)).
- **Not sluis as the authorization backend**: it would hold per-user sessions and sit on every console's request path,
  which is oauth2-proxy rebuilt inside it.
