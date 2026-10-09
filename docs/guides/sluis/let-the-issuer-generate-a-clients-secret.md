# Let the issuer generate a client's secret

Stop delivering a confidential client's secret by hand. The issuer makes it, keeps it and exposes it to the relying party.

## Before you start

- Use a confidential client (`kind: confidential`), a Secrets adapter that writes only if absent (`ssm` or `openbao`), and a State shared between replicas such as `dynamodb`. Start is refused otherwise.

- Roll every replica first. An older binary refuses `secret: {generate: true}`.

- If `clients/<id>/secret` exists when the issuer first looks, it is adopted unchanged and nothing rotates.

## 1. Declare the client as generated

```yaml
clients:
  grafana:
    kind: confidential
    secret: {generate: true}   # was: secret: grafana-oidc
    requires: [devel:grafana:viewer]
    redirects: [/login/generic_oauth]
```

The issuer creates `credentials/oidc-client/grafana/secret`. The audit trail shows `roster.client.secret.adopted` when an input existed, else `roster.client.secret.created`.

```sh
sluisctl clients show grafana
```

It prints `created` and `generated in policy true`.

## 2. Point the relying party at the document

The secret lives once, as the `oidc/v1` document at `external/oidc/<client>` ([secrets](../../reference/sluis/secrets.md#the-external-documents)). Grant the relying party that address on its own side. For External Secrets, set the `ExternalSecret`'s `remoteRef.key` to `external/oidc/<client>` under the installation's root and `property` to `client-secret`. The issuer never touches the relying party's Secret.

The document holds only the current secret. Verify: a sign-in to the relying party succeeds.

## 3. Remove the input secret

Delete `clients/<id>/secret` from wherever the installation delivers inputs. The token endpoint ignores the input once the stored record exists. `sluisctl clients show <id>` still shows the record.

## Roll back

Put the name back in `secret`. The stored record is then reported as orphaned and kept ([rotate a client secret](operate/rotate-a-client-secret.md#retire-a-generated-client)). Rotated-away values stay in the store's version history, readable by whoever can read the store.

A failure for one client is logged and counted as `sluis.client_secret.reconcile`. It is retried every five minutes on a server and on the directory refresh on Lambda.

## Decided in

[ADR 0039](../../decisions/0039-the-issuer-generates-confidential-client-secrets.md). Fields: [`secret` in the policy](../../reference/sluis/policy-clients.md#a-generated-secret). Rotate with [rotate a client secret](operate/rotate-a-client-secret.md).
