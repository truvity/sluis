# Example: the issuer generates a client's secret

## Goal

Stop delivering a confidential client's secret by hand: the issuer makes it, keeps it at `external/oidc/<client>`, and the
relying party reads it with its own secret operator.

## What you need

- A confidential client, a Secrets adapter that can write only if absent (`ssm` or `openbao`), and a State shared between
  replicas.
- Every replica rolled to a version that understands `secret: {generate: true}` before you write it.

## The policy snippet

```yaml
clients:
  grafana:
    kind: confidential
    secret: {generate: true}   # was: secret: grafana-oidc
    requires: [devel:grafana:viewer]
    redirects: [/login/generic_oauth]
```

## The exchange / command

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: grafana-oidc, namespace: grafana}
spec:
  secretStoreRef: {kind: ClusterSecretStore, name: sluis}
  target: {name: grafana-oidc}
  data:
    - secretKey: client-secret
      remoteRef:
        key: external/oidc/grafana
        property: client-secret
```

Once the relying party signs in with the generated value, delete the input `clients/grafana/secret`; it is ignored while the
stored record exists.

## Verify

`sluisctl clients show grafana` prints `created` and `generated in policy true`. The audit trail shows
`roster.client.secret.created`, or `.adopted` when an input secret existed. A sign-in to the relying party succeeds.

## Undo

Put the name back in `secret:`; the stored record is reported orphaned and kept. Rotate with `sluisctl clients rotate`.

Recipe: [Let the issuer generate a client's secret](../../how-to/let-the-issuer-generate-a-clients-secret.md); rotation:
[rotate a client secret](../../how-to/rotate-a-client-secret.md). The document is the `oidc/v1` shape in
[secrets](../../reference/secrets.md#the-external-documents).

Snippet source: `docs/how-to/let-the-issuer-generate-a-clients-secret.md`; `policy/clientsecret_test.go` holds the parser
cases for the object form.
