# Erase a tenant's pseudonymisation keys

Crypto-shred a tenant's pseudonyms for the purposes not under a legal duty, when the tenant asks for erasure.

## Before you start

- The installation needs a key provider. With no `keys` block (the default) there is nothing to destroy: the application erases the end user in its own database and the archive's opaque identifier then resolves to nobody ([0055](../../../decisions/0055-no-pseudonymisation-keys-by-default.md)).

- Act as a person allowed to erase. Under the storage port (`keys.adapter`) `kms` needs delete on the state store's objects under the wrapped-key prefix.

- The port erases on `kms`, `transit` and, for tests, `local`. On `transit` act as the eraser role of [OpenBao keys](configure-openbao-keys.md).

- You need a writer to record through (`--sink`).

- The command refuses while a hold covers the tenant's copies and names the hold. Look first with `audit hold list`: destroying keys under a legal hold destroys evidence.

- The key is destroyed, then the erasure is recorded. If the record cannot be written, the command says the key is already gone and you must account for it by hand.

- Billing and evidence copies stay (GDPR Art. 17(3)(b)). Security and history copies become unlinkable.

- Keys are never rotated: [key custody](../../../concepts/audit/key-custody.md#never-rotated).

## Steps

1. List holds.

   ```
   audit hold list --bucket <b>
   ```

2. Destroy the key once per purpose that is not under a duty.

   ```
   audit key destroy --tenant <id> --purpose <p> --by <who> --reason <why> \
       --bucket <b> --sink <writer> \
       --writer-config <writer.yaml>           # the keys block of the writer's own configuration
   ```

   With `--writer-config` the command opens the writer's keys and calls the port's `Key.Destroy`. It deletes the tenant's secret with all versions and leaves a tombstone, so a later pseudonym is refused with "destroyed". Running it again succeeds.

   A writer replica that holds the tenant's secret in memory stops within a minute. Restart replicas if the erasure must hold at once.

   A value sealed under the `conceal` key is refused by `resolve` after erasure, but its ciphertext is not shredded: the conceal key is one per installation. Leave `conceal` unconfigured where sealed identifiers must be cryptographically unreadable.

## Verify

`audit.key.destroyed` is recorded automatically and `resolve` for that tenant and purpose is refused.

## Roll back

None. A destroyed key cannot be restored.

## Afterwards

Tell the tenant what was destroyed and what stays.
