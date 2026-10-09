# Connect a Kubernetes cluster

Let people and CI jobs sign in to a cluster's API server. The API server trusts the issuer with one client id per cluster and reads `groups` into RBAC. For in-cluster workloads, see [service to service](service-to-service.md).

## Before you start

- You can edit the API server's OIDC configuration: an EKS identity provider config or kubeadm `--oidc-*` flags.

- Render the policy and read the group names. RBAC binds them as spelled, and a typo is a silent denial.

- The API server can reach the issuer URL.

- Keep a break-glass path outside sluis, the cloud's own cluster access, until a policy-granted admin has used the cluster. Test it quarterly.

- Match the signing algorithm. The default key signs ES384, kube-apiserver defaults to RS256 and EKS accepts RS256 only. A mismatch lets sign-in succeed but every `kubectl` call answers `Unauthorized`.

## Steps

### 1. Configure the cluster

Point the API server at client `k8s:<cluster>`: issuer URL, client id, username claim `email`, groups claim `groups`, an optional prefix. Bind RBAC by the policy's group names.

For the algorithm, pick one:

- Self-managed with `--oidc-signing-algs`: list `ES384`. An `AuthenticationConfiguration` file allows every known algorithm.

- EKS, or any verifier that accepts RS256 only: set `signing_alg: RS256` on the `k8s:<cluster>` client ([per-audience algorithm](../../../reference/sluis/policy.md#signing-algorithm-per-audience)) and add an RS256 key to `signingKey.additional` ([configuration](../../../reference/sluis/configuration.md)). Other audiences keep ES384.

### 2. Declare the policy

```yaml
groups:
  prod:k8s:admin:  { members: [role-sre@example.com, role-admin@example.com] }
  prod:k8s:viewer: { members: [team-eng@example.com] }
clients:
  k8s:prod: { kind: public, requires: [prod:k8s:admin, prod:k8s:viewer] }
```

RBAC binds the token's group names `<env>:k8s:<role>`, the cluster tier of the [naming rule](../../../concepts/sluis/trust.md#naming). `requires` lets `sluisctl kubeconfig` list the cluster.

### 3. Set up the person

Run `sluisctl kubeconfig`. It writes a context per granted cluster with `sluisctl kube-token` as the exec plugin. Or write a kubelogin context by hand:

```yaml
users:
  - name: prod
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        command: kubectl
        args: [oidc-login, get-token, --oidc-issuer-url=https://issuer.example.internal, --oidc-client-id=k8s:prod, --oidc-extra-scope=groups]
```

`sluisctl kube-token` trades the cached sign-in for the cluster audience ([calling anywhere else](service-to-service.md#calling-with-an-issuer-token-anywhere-else)). Check with `kubectl get ns`.

### 4. Set up the job

Use the `truvity/sluis` action pinned to a release with `audiences: k8s:<cluster>`. It exchanges the job's token and writes a kubeconfig. Or use the person's kubeconfig in a job with `id-token: write` ([GitHub Actions](github-actions.md#5-reuse-a-laptops-files-optional)).

The machine group's matchers on repository, ref and visibility decide which jobs may. Run the job on an allowed and a disallowed branch.

## Afterwards

A revoke reaches the API server at the token's expiry, the client's `ttl_cap`. Tell the cluster's owners.
