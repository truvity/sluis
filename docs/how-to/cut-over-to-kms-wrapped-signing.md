# Cut over to KMS-wrapped signing without signing everyone out

> The key that wraps the ring is now named `keys.sign` (an alias; an ARN or key id is refused), and `signingKey.kmsWrapped.keyId`
> is its deprecated spelling. Where this page says `kmsWrapped.keyId`, set `keys.sign` instead. The ring generates its key
> pairs locally and wraps them under `{instance, purpose: sign}`; see [signing on AWS](../explanation/signing-on-aws.md).

## Purpose

Move an installation's signing from cert-manager file keys to `signingKey.kmsWrapped` while every token the old keys
signed keeps verifying until it expires.

## Preconditions

- An installation signing with the chart's file keys (the default ES384 key, and any `signingKey.additional` key such as
  RS256), reachable with `kubectl` and `helm`.
- The KMS symmetric key and the role that may use it, as the `kmsWrapped` fields describe them
  ([configuration](../reference/configuration.md#the-service-document): `signingKey.kmsWrapped`; the example in the
  [chart README](../../charts/sluis/README.md#kubernetes-on-eks-aws-storage-kms-wrapped-signing-openbao-secrets)).
- The sign-in state secret `issuer/state-secret` ([secrets](../reference/secrets.md#the-names)): at least 32 random bytes,
  the same in every replica.

## Before you start

- **When the file mounts go, so do the old keys.** Tokens signed by the old keys carry their `kid`. Without the overlap
  below, the old keys leave the JWKS with the mounts and every token they signed stops verifying.
- **Publish only public keys.** A private key in `verifyOnly` stops the start in any encoding, and the chart refuses it at
  render. A private key that ever reached a rendered ConfigMap (a values file, a Helm release, git) must be treated as
  leaked and rotated.
- **Preview before every apply, and read the preview.** `helm template` the new values and check that the Certificate and
  the signing Secret are gone, the `verify-keys` ConfigMap is present and the Deployment's checksum annotations changed.

## Steps

### 1. Extract the old public keys

**Run**

```sh
kubectl -n <namespace> get secret <release>-signing-key -o jsonpath='{.data.tls\.key}' | base64 -d | openssl pkey -pubout
```

and the same for the additional key's Secret (`<release>-signing-key-rs256`). Never extract the private half.

**Expect** a `-----BEGIN PUBLIC KEY-----` block per key.

**Verify** the `kid` the old tokens carry is the key's RFC 7638 thumbprint, which is what the file signer used. Read the
current key ids before the cutover: `curl -s https://<issuer>/keys | jq -r '.keys[].kid'` (the discovery document's
`jwks_uri`).

**Rollback**: none needed, because nothing changed.

### 2. Put them in the chart as verify-only keys, and switch the signer

**Run** in the values:

```yaml
signingKey:
  verifyOnly:
    - pem: |
        -----BEGIN PUBLIC KEY-----
        ...
config:
  signingKey:
    file: null
    kmsWrapped: {keyId: alias/sluis-signing, region: eu-west-1, stateSecret: issuer/state-secret}
    verifyOnly:
      - {file: /var/run/access-issuer/verify-keys/0.pem, until: "2026-12-01T00:00:00Z"}
```

Name each key in `config.signingKey.verifyOnly[]` by the index it has under `signingKey.verifyOnly` (the chart mounts it
from a ConfigMap at `/var/run/access-issuer/verify-keys/<index>.pem`). Remove `signingKey.existingSecret` and
`signingKey.additional`: with KMS signing the chart refuses them. Set `until` to the last expiry of a token the old keys
signed (`lifetimes.refresh` or `lifetimes.absolute`, whichever is longer, counted from the cutover) plus the verifiers'
cache time.

**Expect** `helm template` to render no signing Certificate and a `<release>-verify-keys` ConfigMap.

**Verify** after the upgrade the pods are ready, and the JWKS holds both the old `kid`s and the new KMS-wrapped keys:
`curl -s https://<issuer>/keys | jq -r '.keys[].kid'`. A token minted before the cutover still verifies.

**Rollback**: `helm rollback` to the previous revision. The old Secrets still exist until you delete them, so the file
signer returns; delete nothing until step 3 has passed.

### 3. Remove the verify-only entries

**Run** after `until` has passed, remove `signingKey.verifyOnly` and `config.signingKey.verifyOnly`, then upgrade. The
keys are not published after `until` whether or not you remove the entries.

**Expect** the old `kid`s absent from the JWKS.

**Verify** `curl -s https://<issuer>/keys | jq -r '.keys[].kid'`; sign in once.

**Rollback**: none, because the old tokens have expired.

## Afterwards

- Delete the old signing Secrets and the cert-manager Certificates the chart no longer renders, once nothing mounts them.
- Check the signing alerts in [telemetry](../reference/telemetry.md#alerts): the rotation threshold follows `rotateEvery`.
- Tell the owners of relying parties that pin an algorithm (`signing_alg: RS256`) that RS256 is now a KMS-wrapped key too.
