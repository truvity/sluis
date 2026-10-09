# How does the archive show it is intact?

The archive shows that nothing was removed, nothing was changed, and the operator did not choose what was signed. The byte-level contract is the [bucket contract](../../reference/audit/bucket-contract.md#seals). The check is [`audit verify`](../../reference/audit/verify-command.md).

## Three layers

1. **Hashes.** Every object names its `sha256` and each record line carries its `hash`. `audit verify` recomputes both from the archive alone. This shows each object is the one written. It cannot show that an object was removed or added.

2. **The lock.** On the record tier the store is written in Object Lock compliance mode. Nobody, the account root included, can overwrite or delete an object before its retention ends.

3. **Seals.** The notary writes one signed seal per profile, tenant and hour, empty hours included. Seals chain through `prev` and carry a Merkle root over the hour's record hashes. A seal that disagrees with the bucket shows an object added, removed or changed after sealing.

The current hour is not sealed yet. A seal is due after the settle window and a grace. Until then the lock protects the objects.

## Seals come from a job

A signature per record proves only that the signer chose it. Four properties need a job:

- **Removal.** A deleted signed record takes its signature with it. Only a signed list of the hour, empty for a quiet hour, makes a missing object visible.

- **Emitter signatures.** They say only that the application said so. The writer already verifies the workload token and stamps the observer, and `origin_hash` covers the accepted record.

- **The writer holds no signing key.** The notary runs as its own identity. It may use the key and write only under `seals/` and `keys/`.

- **Quiet hours.** Nothing wakes the writer when nothing happens. A scheduled job seals every hour, and seals the hours it missed.

## Two tiers

| tier | store | integrity |
|---|---|---|
| record | Object Lock API, compliance mode | lock plus seals |
| attested | same archive and seals on a store without the lock, such as an S3-compatible service from another provider | seals plus compensating controls |

On the attested tier, use a managed signing key and grant no component delete permission. Add an administrative no-delete rule where the store offers one.

Each framework profile states the least lock its framework demands. `pci-dss`, `nen-7513`, `dora` and `evidence-etsi` demand `compliance`. `security`, `history` and `billing-nl` demand `none`. The writer refuses to start when a profile's preset has a weaker lock than the profile demands. The preset lock is compliance for `attested` and none for the others.

## Who holds the key

A managed key in KMS or a transit engine never leaves its provider, so seals also prove the operator did not choose what to sign. Use one when you must answer an assessor. A key file suits tests and a deployment that accepts the weaker claim.

An auditor trusts a key because they pinned its thumbprint, not because the key is in the bucket. See [key custody](key-custody.md#signing-key).

## Time

Emitters and writers need synchronised clocks. `audit clock-sync` checks the offset against UTC daily and records `audit.clock.synchronised`, which the evidence and PCI framework profiles require. It never sets the clock.

`offset_ms` is the correction the clock needs per RFC 5905 §8: positive means the clock is behind. With several references it trusts the quickest to answer. A clock outside the tolerance fails the run and is still recorded. Nothing is recorded when no reference answered.

## Anchoring

Framework profiles may require or recommend anchoring the seal chain head with an RFC 3161 or ETSI time-stamp. This is not built.

## Decided in

- [0045 S3 Object Lock as the record](../../decisions/0045-s3-object-lock-as-the-record.md).
- [0050 Digest chain and verification](../../decisions/0050-digest-chain-and-verification.md).
- [0056 Lock modes and store tiers](../../decisions/0056-lock-modes-and-store-tiers.md).
- [0061 Seals](../../decisions/0061-seals.md).
- [0068 Storage is configured per preset](../../decisions/0068-storage-is-configured-per-preset.md).
