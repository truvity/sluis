# Cut over to KMS-wrapped signing

Move signing from cert-manager file keys to `signingKey.kmsWrapped` while every token the old keys signed keeps verifying until it expires. The ring wraps its keys under `keys.sign`: see [signing on AWS](../../../concepts/sluis/signing-on-aws.md).

## Before you start

- Provide the symmetric KMS key and its role ([configuration](../../../reference/sluis/configuration.md#the-service-document), [example](../../../reference/sluis/openbao-secrets-adapter.md#example-kubernetes-on-eks-with-kms-wrapped-signing-and-openbao-secrets)).

- Create `issuer/state-secret` ([secrets](../../../reference/sluis/secrets.md#the-names)): at least 32 random bytes, the same in every replica.

- Extract only public keys. A private key in `verifyOnly` stops the start, and the chart refuses it at render. Treat a private key that reached a ConfigMap, values file, Helm release or git as leaked.

- Run `helm template` on the new values first. The Certificate and the signing Secret must be gone, the `verify-keys` ConfigMap present.

## Steps

### 1. Extract the old public keys

```sh
kubectl -n <namespace> get secret <release>-signing-key -o jsonpath='{.data.tls\.key}' | base64 -d | openssl pkey -pubout
curl -s https://<issuer>/keys | jq -r '.keys[].kid'
```

Repeat the first command for `<release>-signing-key-rs256`. Record the current `kid`s: they are RFC 7638 thumbprints.

### 2. Publish them as verify-only and switch the signer

```yaml
signingKey:
  verifyOnly:
    - pem: |
        -----BEGIN PUBLIC KEY-----
        ...
config:
  keys: {sign: alias/sluis-signing}
  signingKey:
    file: null
    kmsWrapped: {region: eu-west-1, stateSecret: issuer/state-secret}
    verifyOnly:
      - {file: /var/run/access-issuer/verify-keys/0.pem, until: "2026-12-01T00:00:00Z"}
```

Number each `config.signingKey.verifyOnly[]` entry by its index in `signingKey.verifyOnly`. Remove `signingKey.existingSecret` and `signingKey.additional`: the chart refuses them with KMS signing. Set `until` to the longer of `lifetimes.refresh` and `lifetimes.absolute` from the cutover, plus the verifiers' cache time.

Upgrade. The JWKS holds the old `kid`s and the new keys, and a token minted before the cutover verifies. To undo, `helm rollback`: the old Secrets remain until you delete them.

### 3. Remove the verify-only entries

After `until`, delete `signingKey.verifyOnly` and `config.signingKey.verifyOnly` and upgrade. Keys are unpublished after `until` either way. The old `kid`s leave the JWKS.

## Afterwards

- Delete the old signing Secrets and Certificates once nothing mounts them.

- Check the signing [alerts](../../../reference/sluis/telemetry.md#alerts): the rotation threshold follows `rotateEvery`.

- Tell owners of relying parties that pin `signing_alg: RS256` that RS256 is now a KMS-wrapped key.
