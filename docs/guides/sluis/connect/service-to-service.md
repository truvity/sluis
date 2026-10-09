# Call one service from another

Present the right credential when a workload, a CI job or a person calls your service, and make the service accept it. The model is in [trust](../../../concepts/sluis/trust.md): the cluster anchors callers next door and the issuer anchors the rest.

## Before you start

- Name the callers a listener admits. An empty list admits nobody.
- An exchange client authenticates by HTTP Basic with an empty secret. A `client_id` only in the form fails as `invalid_client`.

| Is the caller in the service's cluster? | Present |
|---|---|
| Yes | The caller's ServiceAccount token, projected for the service's audience |
| No | An issuer token, from an exchange (workload, CI job) or a sign-in (person) |

## Steps

### Calling with a ServiceAccount token (same cluster)

```yaml
volumes:
  - name: my-service-token
    projected:
      sources:
        - serviceAccountToken:
            audience: my-service            # the service's audience, from its values
            expirationSeconds: 3600
            path: token
```

Read the file on every call: the kubelet rotates it. Send it as `Authorization: Bearer`. The sluis console API accepts this ([contracts](../../../reference/sluis/contracts.md)).

### Calling with an issuer token (anywhere else)

A workload in another cluster exchanges its ServiceAccount token:

```
POST /token
Authorization: Basic base64(<the service's client id>:)
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
subject_token=<the workload's ServiceAccount token>
subject_token_type=urn:ietf:params:oauth:token-type:jwt
audience=<the service's client id>
```

The issuer verifies the token against the cluster's published key set. Connect a cluster with one row:

```yaml
exchange:
  clusters:
    - name: dev
      issuer: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE
```

EKS publishes that key set, and Talos serves it at `/openid/v1/jwks`. A deleted ServiceAccount goes unnoticed until its token expires.

A CI job exchanges its platform token ([GitHub Actions](github-actions.md)). A person runs `sluisctl login`, then `sluisctl token --audience <client id>` for the token or `sluisctl exchange` for the whole response. The CLI's `public` client declares `sign_in_exchange: true`. The exchange accepts only that client's live access token as proof, and refuses an ID token.

### Declaring the service as a client

```yaml
groups:
  all:inventory:reader:
    matchers:
      - service_account: { namespace: team-sync, name: team-sync }
clients:
  inventory:
    kind: public
    requires: [all:inventory:reader, all:inventory:operator]
```

Name a group for the role it grants, as `<scope>:<thing>:<role>` ([naming](../../../concepts/sluis/trust.md#naming)).

### Accepting callers in the service

Serve people and workloads on separate listeners.

| Listener | Behind | Verifier | Accepts |
|---|---|---|---|
| console | Gateway OIDC, `oauth2-proxy` or your own flow | `Issuer` | people |
| API | Service DNS | `Cluster`, plus `Issuer` for remote callers | workloads here, and further away through the issuer |

```go
cluster := &identity.Cluster{
    Review:   kube.ReviewToken,        // yours, or client-go's
    Audience: "my-service",
    Name:     "prod",                  // the same namespace on two clusters is two callers
    Groups:   []string{"prod:k8s:admin"},
}
issuer := &identity.Issuer{
    URL:      "https://issuer.example.internal",
    Audience: "my-service",            // this service's client id at the issuer
}

// Either anchor, tried in order, first to answer wins.
http.ListenAndServe(":8080", identity.Middleware(cluster, issuer)(api))
```

You supply `Review`: a TokenReview, or a check against the cluster's published key set. State `Groups` yourself, because a ServiceAccount token carries none. The handler sees one `identity.Verified`, so key any grant table by caller, not by anchor. A verifier that cannot reach the issuer stops the chain. A service for people needs only `Issuer`.

## Never do

- Share a secret between two services.
- Put an API key on the console listener.
- Trust `X-Forwarded-Email` outside a local run.
- Re-map group names on the way in.

## Verify

Call with each token and expect success. Call with a token for another audience and expect a refusal.

## Recovery

Copy no break-glass pattern into a service: [recovery](../../../concepts/sluis/trust.md#recovery-is-the-root-not-a-back-door).
