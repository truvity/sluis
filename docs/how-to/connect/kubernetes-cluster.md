# Connect a Kubernetes cluster

## Purpose

Let people and CI jobs sign in to a cluster's API server with sluis.

## Preconditions

- Access to the cluster's API server configuration (EKS: an identity provider config; kubeadm: `--oidc-*` flags).
- The policy declares the cluster's groups, and sluis is reachable from the API server at its issuer URL.
- A break-glass path that does not go through sluis (see the end of this page).

## Before you start

- **Match the signing algorithm to the cluster.** The chart's default key signs ES384 and kube-apiserver defaults to
  RS256. A managed cluster (EKS) accepts RS256 only. Look: the sign-in succeeds in the browser and every `kubectl` call
  answers `Unauthorized`. Fix it in the cluster-side step below.
- **Preview the policy before applying it** (`sluisctl policy render`) and read the group names: RBAC binds them exactly
  as the policy spells them, so a typo is a silent denial.
- **Keep the break-glass role until a policy-granted admin has used the cluster.**

## Steps

**Anchor:** the issuer. The API server trusts it with one client id per
cluster and reads the `groups` claim into RBAC, binding the internal
group names exactly as the policy spells them. People use **kubelogin**
or `sluisctl`, and both work today: `sluisctl kubeconfig` writes a
context per cluster you are granted, with `sluisctl kube-token` as the
exec plugin behind it.
(A workload *inside* the cluster calling a service inside the cluster
is the other anchor and does not come here:
[service-to-service.md](service-to-service.md).)

### 1. Cluster side

**Run**:

- A static, public client per cluster: `id: k8s:<cluster>`.
- The API server's OIDC configuration (EKS: an identity provider config;
  kubeadm: `--oidc-*` flags): issuer URL, client id `k8s:<cluster>`,
  username claim `email`, groups claim `groups`, a groups prefix if you
  want one.
- The signing algorithm. kube-apiserver's `--oidc-signing-algs` defaults
  to `RS256`, and the chart's default key signs ES384, so the flag must
  list `ES384` (its allowed values include it). An `AuthenticationConfiguration`
  file has no such setting: kube-apiserver's source allows every known
  algorithm when the file is used. A managed cluster's identity-provider
  configuration exposes no algorithm setting either (EKS takes the issuer
  URL, client id, claims and prefixes and documents no algorithm) **and
  its associated OIDC identity provider accepts RS256 only** — confirm
  with a self-managed cluster's own documentation whether it verifies
  ES384, and where it does not (or for EKS, always), name `k8s:<cluster>`'s
  own client `signing_alg: RS256` in the policy
  ([reference/policy.md#signing-algorithm-per-audience](../../reference/policy.md#signing-algorithm-per-audience))
  and add an RS256 key to `signingKey.additional`
  ([reference/configuration.md](../../reference/configuration.md)) — every
  OTHER audience keeps signing ES384. Moving the whole installation's
  default to RSA (`signingKey.certificate: {algorithm: RSA, size: 2048,
  encoding: PKCS1}`) still works but moves every relying party's tokens,
  not only this cluster's.
- RBAC bindings by group name — the internal group's name, as it stands
  in the policy. Name the groups after what the bindings already say and
  the cutover changes no binding.

**Expect**: the API server accepts `k8s:<cluster>` tokens for the groups claim.

**Verify**: `kubectl auth whoami` after step 3 names the person and the groups.

**Rollback**: remove the identity provider config; the break-glass path stays.

### 2. Policy

**Run**:

The cluster is a public client. The group names its RBAC binds **are**
the internal groups; nothing is re-mapped on the way:

```yaml
groups:
  prod:k8s:admin:  { members: [role-sre@example.com, role-admin@example.com] }
  prod:k8s:viewer: { members: [team-eng@example.com] }
clients:
  k8s:prod: { kind: public, requires: [prod:k8s:admin, prod:k8s:viewer] }
```

The sign-in page names a `k8s:<cluster>` client *Kubernetes — `<cluster>`*
on its own, or as `display_name` says; when the redirect is on the
person's own computer it says a program there is asking. `requires` is
what lets `sluisctl kubeconfig` know this person may use this cluster; the group names in the token are what the cluster's RBAC
binds — `<env>:k8s:<role>`, the cluster tier of the
[naming rule](../../explanation/trust.md#naming). An installation that renders
its policy from an access matrix mints these
names from one function, so the binding and the token cannot drift
apart. Renaming an installation's existing bindings is safe to do
gradually: RBAC binds any number of group names to one ClusterRole, so
the old and the new spelling coexist until the old issuer is gone.

**Expect**: `sluisctl kubeconfig` lists the cluster for a person holding `requires`.

**Verify**: render the policy and read the client row.

**Rollback**: remove the client row.

### 3. Person side

**Run**:

Either `sluisctl kubeconfig`, which writes a context per granted
cluster with `sluisctl kube-token` as the exec plugin, or a hand-written
context with kubelogin:

```yaml
users:
  - name: prod
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        command: kubectl
        args: [oidc-login, get-token, --oidc-issuer-url=https://issuer.example.internal, --oidc-client-id=k8s:prod, --oidc-extra-scope=groups]
```

On a laptop, `sluisctl kube-token` trades the cached sign-in for the
cluster's audience, which the issuer allows only because sluisctl's own
client declares `sign_in_exchange: true`
([service-to-service.md](service-to-service.md#calling-with-an-issuer-token-anywhere-else)).

**Expect**: `kubectl get ns` works with the person's groups.

**Verify**: `kubectl auth whoami`.

**Rollback**: delete the context.

### 4. Job side

**Run**:

The API server trusts one issuer, sluis, so a job's GitHub token is
never presented to it. Either the action, `truvity/sluis` pinned
to a release with `audiences: k8s:<cluster>`, exchanges the job's token
at the issuer and writes a kubeconfig with the resulting token; or the
same kubeconfig a person uses works unchanged in a job granted
`id-token: write`, because `sluisctl kube-token` exchanges the job's
own token there ([github-actions.md](github-actions.md#or-the-same-files-a-laptop-uses)).
The machine group's matchers on repository, ref and visibility decide
which jobs may. The token's lifetime is the issuer's CI client setting;
a step that outlives it re-runs the action.

**Expect**: a job granted by the machine group gets a working kubeconfig.

**Verify**: run the job once on a branch the matchers allow, and once on one they do not.

**Rollback**: remove the machine group's matcher.

## Afterwards

- **Break-glass** is outside sluis: the cloud's own cluster access mechanism bound to a break-glass cloud role. Test it
  once a quarter.
- Tell the cluster's owners that a revoke reaches the API server at the token's expiry (the client's `ttl_cap`).

