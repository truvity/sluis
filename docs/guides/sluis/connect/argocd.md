# Connect Argo CD

Sign people in to Argo CD with sluis. Argo CD runs its own OIDC code flow, reads groups from the ID token and maps them in its own policy.

## Before you start

- You have a running Argo CD, a Secret `argocd-oidc-client` holding the client secret, and permission to change the policy, `argocd-cm` and `argocd-rbac-cm`.

- The groups `prod:k8s:admin` and `prod:k8s:viewer` exist in the policy ([naming](../../../concepts/sluis/trust.md#naming)).

- Keep the local admin until a policy-granted identity has signed in as an admin.

- `display_name` and `description` show to anyone who starts a sign-in. Keep them harmless.

## Steps

### 1. Declare the client

```yaml
clients:
  argocd:
    kind: confidential
    display_name: Argo CD
    description: deployments on the prod cluster
    secret: argocd-oidc-client
    redirects:  [https://argocd.example.internal/auth/callback]
    signed_out: [https://argocd.example.internal/]
    requires:   [prod:k8s:admin, prod:k8s:viewer]
```

Render the policy and check that the client appears. Roll back by removing the row.

### 2. Configure Argo CD

Set `oidc.config` in `argocd-cm`:

```yaml
name: Corporate
issuer: https://issuer.example.internal
clientID: $argocd-secret:oidc.clientID
clientSecret: $argocd-secret:oidc.clientSecret
requestedScopes: [openid, profile, email, groups]
logoutURL: https://issuer.example.internal/end_session?id_token_hint={{token}}&post_logout_redirect_uri=https://argocd.example.internal
```

Sign in with `Corporate` and land back on Argo CD. Roll back by removing `oidc.config`.

### 3. Bind the groups

Bind the cluster tier in `policy.csv`: `g, prod:k8s:admin, role:admin`. Give Argo CD its own `thing` only when its ladder diverges from the cluster's. Sign in as an admin and as a viewer and check what each can do.

### 4. Turn the local admin off

After a policy-granted admin has signed in, set `admin.enabled: "false"` in `argocd-cm`. Roll back with `"true"`.

## Afterwards

- The `argocd` CLI logs in through the same client with the browser flow.
- `oidc.config` needs no signing-algorithm setting. A relying party that accepts RS256 only needs `signingKey.certificate: {algorithm: RSA, size: 2048, encoding: PKCS1}` ([reference](../../../reference/sluis/configuration.md)).
- Argo CD keeps its own session. A revoke at sluis reaches it at the next token refresh, not through back-channel logout.
