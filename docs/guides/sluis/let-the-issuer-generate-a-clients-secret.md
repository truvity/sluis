# Let the issuer generate a client's secret

## Purpose

Stop delivering a confidential client's secret by hand: the issuer makes it, keeps it, and copies it to the store the
relying party reads.

## Preconditions

- A confidential client in the policy (`kind: confidential`), and operator access.
- A Secrets adapter that can write only if absent: `ssm` or `openbao`. The `legacy` adapter cannot, and start is refused.
- A State shared between replicas, for example `dynamodb`. Start is refused otherwise (the `memory` adapter is excepted).
- A secret operator on the relying party's side (External Secrets, for instance) that can read the document at
  `external/oidc/<client>`: the relying party is granted that exact address on its own side.

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
[rotate a client secret](operate/rotate-a-client-secret.md#retire-a-generated-client).

### 2. Have the relying party read the document

The secret is stored once, as the `oidc/v1` document at `external/oidc/<client>` (layout v4; see
[secrets](../../reference/sluis/secrets.md#the-external-documents)). Nothing is copied.

**Run** point the relying party's own secret operator at that address. For External Secrets that is an `ExternalSecret`
whose `remoteRef.key` is `external/oidc/<client>` (under the installation's root) and whose `property` is
`client-secret`; it names the key of its own Secret itself. The issuer never touches the relying party's Kubernetes
Secret.
**Expect** the relying party's Secret holds the generated value within its refresh interval. Only the current secret is
in the document, never the previous one.
**Verify** a sign-in to the relying party succeeds.
**Rollback**: none needed, because the old input is still accepted until you remove it.

### 3. Remove the input secret

**Run** once the relying party signs in with the generated value, delete `clients/<id>/secret` from wherever the
installation delivers inputs. The token endpoint reads the stored record of a generated client first and the input only
while the store says there is none, so the input does nothing once the record exists.
**Expect** no change for the relying party.
**Verify** `sluisctl clients show <id>` still shows the record, and a sign-in still works.
**Rollback**: write the input again; it is ignored while the record exists.

## Afterwards

- A failure for one client is logged and counted (`sluis.client_secret.reconcile`) and does not stop the issuer; it is
  retried every five minutes on a server and on the directory refresh on Lambda.
- Rotate with [rotate a client secret](operate/rotate-a-client-secret.md). Reference: [`secret` in the policy](../../reference/sluis/policy-clients.md#a-generated-secret),
  the [`oidc/v1` document](../../reference/sluis/secrets.md#the-external-documents), and the decision in
  [ADR 0039](../../decisions/0039-the-issuer-generates-confidential-client-secrets.md).
