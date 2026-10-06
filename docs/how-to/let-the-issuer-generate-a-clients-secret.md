# Let the issuer generate a client's secret

## Purpose

Stop delivering a confidential client's secret by hand: the issuer makes it, keeps it, and copies it to the store the
relying party reads.

## Preconditions

- A confidential client in the policy (`kind: confidential`), and operator access.
- A Secrets adapter that can write only if absent: `ssm` or `openbao`. The `legacy` adapter cannot, and start is refused.
- A State shared between replicas, for example `dynamodb`. Start is refused otherwise (the `memory` adapter is excepted).
- An export store the relying party can read, and a secret operator on the relying party's side (External Secrets, for
  instance) that reads it.

## Before you start

- **Roll every replica before you write the object form.** An older binary refuses `secret: {generate: true}`.
- **The first run adopts an input secret that exists.** If `clients/<id>/secret` is there when the issuer first looks, it
  becomes the stored secret unchanged, so the relying party keeps working and nothing rotates.
- **Rotated-away values stay in the store's version history** (SSM parameter history, OpenBao KV versions). They are
  readable only by whoever can read the store.

## Steps

### 1. Declare the client as generated

**Run** change the client's `secret` in the policy from a name to an object:

```yaml
clients:
  grafana:
    kind: confidential
    secret: {generate: true}   # was: secret: grafana-oidc
    requires: [devel:grafana:viewer]
    redirects: [/login/generic_oauth]
```

**Expect** the issuer, once deployed, creates `credentials/oidc-client/grafana/secret`. With an input in place the audit
trail shows `roster.client.secret.adopted`; without one, `roster.client.secret.created`.
**Verify** `sluisctl clients show grafana` prints `created` and `generated in policy true`.
**Rollback**: put the name back. The stored record is then reported as orphaned and kept; see
[rotate a client secret](rotate-a-client-secret.md#retire-a-generated-client).

### 2. Export it

**Run** add an export that copies the client's id and current secret:

```yaml
exports:
  - source: oidc-client
    client: grafana
    path: oidc/grafana
    namespace: devel          # the OpenBao namespace, where the adapter has them
    interval: 1h
    properties: {client-secret: secret}   # optional: write only these, under these names
```

**Expect** `client-id` and `client-secret` at `path`, written with `replace`. Only the current secret is exported, never
the previous one. A rotation made with `sluisctl clients rotate` copies the new secret out at once; any other change
reaches the copy at the next `interval` (default 1h, at least 1m), because nothing watches the Secrets port.
**Verify** read the key in the store, or `sluisctl clients show grafana` and the issuer's log for the export.
**Rollback**: remove the entry. The copy already written stays where it is.

### 3. Have the relying party read the export

**Run** point the relying party's own secret operator at `path`. For External Secrets that is an `ExternalSecret` whose
`remoteRef.key` is the path and whose `property` is `client-secret`. The issuer writes the store and never touches the
relying party's Kubernetes Secret.
**Expect** the relying party's Secret holds the generated value within its refresh interval.
**Verify** a sign-in to the relying party succeeds.
**Rollback**: none needed, because the old input is still accepted until you remove it.

### 4. Remove the input secret

**Run** once the relying party signs in with the exported value, delete `clients/<id>/secret` from wherever the
installation delivers inputs. The token endpoint reads the stored record of a generated client first and the input only
while the store says there is none, so the input does nothing once the record exists.
**Expect** no change for the relying party.
**Verify** `sluisctl clients show <id>` still shows the record, and a sign-in still works.
**Rollback**: write the input again; it is ignored while the record exists.

## Afterwards

- A failure for one client is logged and counted (`sluis.client_secret.reconcile`) and does not stop the issuer; it is
  retried every five minutes on a server and on the directory refresh on Lambda.
- Rotate with [rotate a client secret](rotate-a-client-secret.md). Reference: [`secret` in the policy](../reference/policy-clients.md#a-generated-secret),
  [`source: oidc-client`](../reference/exports.md#the-policy-document-exports), and the decision in
  [ADR 0039](../decisions/0039-the-issuer-generates-confidential-client-secrets.md).
