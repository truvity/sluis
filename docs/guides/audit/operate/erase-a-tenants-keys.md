# Erase a tenant's pseudonymisation keys

## Purpose

Crypto-shred a tenant's pseudonyms for the purposes not under a legal duty, when a
tenant asks for erasure.

## Preconditions

- The installation configured a key provider. With `keys.provider: none` (the default)
  there is nothing to destroy: erasure of an end user is the application's, in its own
  database, and after that the archive's opaque identifier resolves to nobody
  ([0055](../../../decisions/0055-no-pseudonymisation-keys-by-default.md)).
- You act as the person allowed to erase: under `transit` a human role whose policy
  reaches `transit/keys/<prefix>.*`; the writer's cannot. Under the storage port
  (`keys.adapter`) the person needs the rights the port's wrapped-key store gives to delete
  (`kms`: delete on the state store's objects under the wrapped-key prefix; the KMS key
  itself needs nothing more than the writer has).
- **The port erases on `kms` (and `local`, for tests) only.** The `transit` adapter has one
  key for the installation, not one per tenant, so `audit key destroy` refuses with
  "operation not supported" and destroys nothing; that is not an erasure. Put the
  `pseudonym` purpose on `kms` to erase, or use the older `--key-provider transit`
  (a key per tenant) ([transit adapter](../../../../storage/keys/transit/doc.go)).
- A writer to record through (`--sink`).

## Before you start


- **Check holds.** The command checks them itself and refuses while one covers the
  tenant's copies, naming the hold and why it was placed; `audit hold list` is still how
  you look before you start. Crypto-shredding a tenant under legal hold destroys
  evidence that may not be destroyed.
- **The order is deliberate: the key is destroyed, then the erasure is recorded.** A
  record written first could claim an erasure that then failed. If the record cannot be
  written the command says, loudly, that the key is already gone and must be accounted
  for by hand.
- **Billing and evidence copies stay** under GDPR Art. 17(3)(b); the security and
  history copies become unlinkable.
- **Keys are never rotated on a schedule**, so there is no "rotate instead" answer
  ([key custody](../../../concepts/audit/key-custody.md#never-rotated)).

## Steps

1. **List holds.** `audit hold list --bucket <b>`.

2. **Destroy.**

   ```
   audit key destroy --tenant <id> --purpose <p> --by <who> --reason <why> \
       --bucket <b> --sink <writer> \
       --writer-config <writer.yaml>           # the storage port: the keys block of the writer's own configuration
       # or: --key-provider transit            # BAO_ADDR, BAO_NAMESPACE, BAO_TOKEN from your shell
       # or: --key-root <file> --key-dir <dir> # the local provider
   ```

   With `--writer-config` the command opens the same keys as the writer does and calls
   the port's `Key.Destroy` for the tenant and the profile (the `--purpose`): the
   per-tenant secret is deleted from the state store with all its versions and a
   tombstone is left, so a later pseudonym for that tenant is refused with "destroyed"
   and never minted anew. Running it again succeeds.

   Expected: the key is gone and `audit.key.destroyed` is recorded automatically.
   Verify: resolve for that tenant and purpose is refused. Roll back: **none**; a destroyed
   key cannot be restored, which is the point.

   Two things to know under the port. A writer replica that already holds the tenant's
   secret in memory stops within a minute (it asks the store again); restart replicas
   if the erasure must hold at once. And a value sealed under the `conceal` key is
   refused by `resolve` after the erasure, but its ciphertext is not shredded: the
   conceal key is one for the installation. Leave `conceal` unconfigured where sealed
   identifiers must be unreadable by cryptography.

## Afterwards

Tell the tenant what was destroyed and what stays and why. Run the destroy per purpose
that is not under a duty.
