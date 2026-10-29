---
title: Kubernetes
description: How sluis verifies Kubernetes ServiceAccount tokens against a cluster's published keys, and how an API server trusts sluis as its OIDC issuer.
status: target
sidebar:
  order: 7
---

Kubernetes appears twice. A **workload** in a cluster proves itself to sluis with its ServiceAccount token. And a cluster's **API server** trusts sluis to say who a person or a CI job is. Only the first is an integration; the second is a client profile.

## What it's for

- **A service in one cluster calls a service in another.** The caller's ServiceAccount token goes to sluis; sluis returns a token for the callee's audience, with the groups that policy gives that ServiceAccount. The callee verifies one token format whatever cluster the caller is in.
- **An engineer uses `kubectl` with their own identity.** Their directory group is the `groups` claim, and RBAC binds that group. No kubeconfig holds a long-lived credential.
- **A CI job deploys to a cluster.** It exchanges its CI identity for the cluster's audience; policy matchers on repository and branch decide which jobs may.
- **A controller reads sluis's data as itself.** An in-cluster reconciler proves who it is with its ServiceAccount token and is admitted to the read-only calls it needs.
- **Many clusters, no credentials for them.** sluis checks a token against the cluster's published key set. It needs no kubeconfig and calls no cluster API.

## Integration actors

| Actor | What it does for Kubernetes |
|---|---|
| `WorkloadVerifier` | Verifies a ServiceAccount token against the issuing cluster's published key set and returns `cluster`, `namespace` and `name` as attributes. It holds no secret. |

Signing in to the API server is a client profile: the cluster has one OIDC client id per cluster (`k8s:<cluster>`), reads the `groups` claim, and sluis lists those clusters for the person.

sluis does **not** call `TokenReview`: that needs a kubeconfig per cluster, and a published key set needs none. (Recovery, the way in when no directory can vouch, is the one place that still asks a cluster to check a token.) sluis also does not write Kubernetes objects: clusters pull what they need.

## What sluis asks the vendor for

**For the workload verifier:** nothing but the cluster's issuer URL and the address of its key set. The key set is public. A cluster is declared once; a bad row stops the verifier at start.

**For the API server sign-in:** nothing from the cluster either. The cluster's owner points the API server at sluis's issuer URL, the client id, the username claim (`email`) and the groups claim (`groups`), and binds RBAC to the group names.

The signing algorithm has to match. sluis signs with EC P-384 by default, and some API servers accept only RS256. The per-audience RS256 exception ([Keys](/concepts/keys/)) covers that case with its own RSA key; other audiences stay on EC.

## What policy controls

- **Which ServiceAccounts count as which group**, by cluster, namespace and name.
- **Which groups may sign in to which cluster.** Each cluster is a client with `requires`, and `sluis kubeconfig` lists only the clusters a person is granted.
- **Group names.** RBAC binds the names as the policy spells them, so a typo is a silent denial. Render the policy and read them before binding.
- **Lifetime.** A revocation reaches the API server when the token expires; the client's lifetime cap is the delay, so keep it short and tell the cluster's owners.

## What is audited

- Each token exchange for a workload, with the cluster, ServiceAccount and audience, and each refusal.
- Each sign-in to a cluster's client.
- Declaring or removing a cluster.

## Setup

An example; the verifier is a small block, and the API server needs a few flags.

1. Declare the cluster for the verifier.
2. Point the API server at sluis (an EKS identity-provider config, or the `--oidc-*` flags or an `AuthenticationConfiguration` file).
3. Write the groups and the client in the policy.
4. A person runs `sluis kubeconfig`; a CI job uses the exchange action with `audiences: k8s:<cluster>`.

```yaml
# /etc/sluis/install.yaml (example)
integrations:
  kubernetes:
    prod:
      actors: [verifier]
      issuer: https://oidc.cluster.example.org/id/EXAMPLE
      jwks_uri: https://oidc.cluster.example.org/id/EXAMPLE/keys
      api_server_client:
        id: k8s:prod
        username_claim: email
        groups_claim: groups
        signing_alg: ES384        # or RS256 for a verifier that accepts nothing else
```

Keep a break-glass path outside sluis, the cloud's own cluster access, until a policy-granted admin has used the cluster, and test it on a schedule.

## v1 → v2 notes

- v1 does both today: ServiceAccount tokens from any number of clusters, and the API server sign-in. See [Connect a Kubernetes cluster](/v1/guides/sluis/connect/kubernetes-cluster/) and [Call one service from another](/v1/guides/sluis/connect/service-to-service/). The person's commands are `sluisctl kubeconfig` and `sluisctl kube-token`.
- In v2 a node's workload identity ([Identity](/concepts/identity/)) is separate from this: it is how sluis's own actors reach a cloud, not how a cluster's workloads reach sluis.
- Kubernetes as an **environment** (running sluis in pods) is separate from this page; see [Kubernetes](/environments/kubernetes/).
- Parity issues: [#784](https://github.com/truvity/sluis/issues/784) workload proofs, [#818](https://github.com/truvity/sluis/issues/818) the API server sign-in.
