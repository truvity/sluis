# Connect ArgoCD

## Purpose

Sign people in to ArgoCD with sluis. ArgoCD runs its own OIDC code flow, so there is no proxy; it reads groups from the
ID token and maps them in its own policy, and nothing about that policy changes when the issuer does.

## Preconditions

- A running ArgoCD and its `argocd-secret`, and a Secret named `argocd-oidc-client` holding the client's secret.
- The groups `prod:k8s:admin` and `prod:k8s:viewer` declared in the policy ([naming](../../../concepts/sluis/trust.md#naming)).
- Permission to change the policy and ArgoCD's `argocd-cm` and `argocd-rbac-cm`.

## Before you start

- **Keep ArgoCD's local admin until a policy-granted identity has signed in as an admin.** Turning it off first locks
  you out if the client row or the group names are wrong.
- **Render the policy and read it before you apply it**; ArgoCD only shows a failed sign-in as a redirect error.
- **Bind the cluster tier, not a new name.** `policy.csv` binds `prod:k8s:admin`, so a rename needs both spellings until
  the old issuer is gone.
- `display_name` and `description` are shown to anyone who starts a sign-in: put nothing in them a stranger should not
  read.

## Steps

### 1. Declare the client

**Run**: add to the policy.

```yaml
clients:
  argocd:
    kind: confidential
    display_name: Argo CD          # the sign-in page: "Sign in to continue to Argo CD"
    description: deployments on the prod cluster
    secret: argocd-oidc-client
    redirects:  [https://argocd.example.internal/auth/callback]
    signed_out: [https://argocd.example.internal/]
    requires:   [prod:k8s:admin, prod:k8s:viewer]
```

**Expect**: the policy renders.

**Verify**: the client appears among the policy's clients.

**Rollback**: remove the row.

### 2. Configure ArgoCD's OIDC

**Run**: set ArgoCD's `oidc.config`.

```yaml
name: Corporate
issuer: https://issuer.example.internal
clientID: $argocd-secret:oidc.clientID
clientSecret: $argocd-secret:oidc.clientSecret
requestedScopes: [openid, profile, email, groups]
logoutURL: https://issuer.example.internal/end_session?id_token_hint={{token}}&post_logout_redirect_uri=https://argocd.example.internal
```

**Expect**: ArgoCD offers a sign-in with `Corporate`.

**Verify**: sign in and land back on ArgoCD.

**Rollback**: remove `oidc.config`; the local admin still works.

### 3. Bind the groups

**Run**: in `policy.csv` bind the **cluster tier** of its own scope, `g, prod:k8s:admin, role:admin`, rather than
owning a `prod:argocd:*` name: being admin of the cluster is the qualification for being admin of the ArgoCD that
manages it. Give ArgoCD a `thing` of its own only when its ladder genuinely diverges from the cluster's. During a
rename, list both spellings.

**Expect**: an admin of the cluster tier is admin in ArgoCD.

**Verify**: sign in as an admin and as a viewer and check what each can do.

**Rollback**: revert `policy.csv`.

### 4. Turn the local admin off

**Run**: once a policy-granted identity has signed in as an admin, set `admin.enabled: "false"`.

**Expect**: the local admin login is gone.

**Verify**: the policy-granted admin still signs in.

**Rollback**: set `admin.enabled: "true"`.

## Afterwards

- The `argocd` CLI logs in through the same client with the browser flow; no separate client is needed.
- `oidc.config` has no signing-algorithm setting and needs none: ArgoCD verifies with go-oidc's provider verifier, which
  takes the algorithms the issuer's discovery document advertises (its `util/oidc/provider.go`), so the chart's default
  ES384 key verifies as an RSA one would. An installation with a relying party that accepts RS256 only sets
  `signingKey.certificate: {algorithm: RSA, size: 2048, encoding: PKCS1}` ([reference](../../../reference/sluis/configuration.md)).
- ArgoCD runs its own session, so a revoke at sluis reaches it at its next token refresh; it does not consume a
  back-channel logout. Tell the owners of the ArgoCD.
