## Scope

The slice of workload exchange that KV needs, pulled forward from parity (ADR 0047, "Consequences" and ADR 0022): a Kubernetes ServiceAccount token becomes a sluis access token.

- `exchange` verifies a foreign token with the verifier `k8s-sa-token`, against the cluster's key set declared in `ExchangeTrust`. Today it verifies a trusted-issuer proof and then refuses it.
- `ExchangeTrust` carries the declared tenant of a workload; a workload never reports its own.
- `policy` decides `token.exchange` for the workload, with the facts `cluster`, `namespace` and `serviceaccount`.
- The token's life is capped at 15 minutes, and an identical login within 60 seconds is accepted, because the Vault client retries a failed login with the same body.
- The rest of workload proofs (AWS role tokens, CI identity tokens, the other verifiers) stays in parity (#784, #965).
- Done when a ServiceAccount token of the test cluster is exchanged for a sluis token with the audience `kv`, a token of another cluster or audience is refused, and each case has a test.

Decision: ADR 0047, ADR 0022. Related: #784, #965, {{ref:61}}, {{ref:63}}.

## Decision codes

K4 (ADR 0047); decision A4 A, 2026-11-18.

## Milestone

Core: kv

Linear: mirrored.
