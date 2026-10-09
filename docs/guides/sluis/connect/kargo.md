# Connect Kargo

Kargo runs its own OIDC flow, maps claims to its own roles and has a CLI that signs in on a loopback port. It needs two public clients.

## Before you start

- Kargo accepts RS256 only. Its `api.oidc` has no signing-algorithm setting and go-oidc's verifier defaults to RS256.
- Keep Kargo's admin account until a policy-granted admin has logged in.
- `groups` is on every token, so no `additionalScopes` is needed.

## Steps

### 1. Declare the clients

```yaml
clients:
  kargo:
    kind: public
    display_name: Kargo
    description: promotions between environments
    redirects:
      - https://kargo.example.internal/
      - https://kargo.example.internal/login
    signed_out: [https://kargo.example.internal]
    requires:   [prod:k8s:admin, prod:k8s:viewer]
    ttl_cap: 5m
    signing_alg: RS256
  kargo-cli:
    kind: public
    display_name: Kargo CLI
    loopback: true
    requires:   [prod:k8s:admin, prod:k8s:viewer]
```

Kargo's UI runs the code flow with PKCE and holds no secret. Add an RS256 key to `signingKey.additional` ([configuration](../../../reference/sluis/configuration.md)). Other audiences keep signing ES384 ([per-audience algorithm](../../../reference/sluis/policy.md#signing-algorithm-per-audience)).

### 2. Configure Kargo

```yaml
api:
  oidc:
    enabled: true
    issuerURL: https://issuer.example.internal
    clientID: kargo
    cliClientID: kargo-cli
    admins: { claims: { groups: [prod:k8s:admin] } }
```

Bind per-project roles on the `groups` claim in Kargo's RBAC. `<env>:<project>:approver` is the promotion gate.

Sign in through the UI and with `kargo login`. Roll back by removing the two client rows.

## Sign-out and revocation

Checked against Kargo 1.11.2 on 2026-09-12; [hack/verify_kargo.py](../../../../hack/verify_kargo.py) repeats it.

- **Logout** drops Kargo's tokens only. Kargo has no RP-initiated logout, so the issuer session stays and the next *SSO Login* needs no password. End the sign-in with the console's **Sign out** or the issuer's `/logout`.
- **Revocation** stops Kargo within `ttl_cap`. Kargo serves on its access token until the next renewal, which the issuer refuses. Expect about four minutes on a five-minute cap.

## Known Kargo defect

After a silent sign-in, which follows Kargo's own Logout, the page stays on `/login?code=…` and shows *SSO Login*. The tokens are already stored. Reloading replays the code, the issuer refuses it and revokes the session, and the next reload starts a new one.

Kargo navigates after the exchange only when a `redirectTo` exists, and Logout lands on `/login` without one. The fix is [akuity/kargo#7190](https://github.com/akuity/kargo/pull/7190), reported in [akuity/kargo#7189](https://github.com/akuity/kargo/issues/7189). Until it lands, open the root after a Logout-then-login instead of reloading.
