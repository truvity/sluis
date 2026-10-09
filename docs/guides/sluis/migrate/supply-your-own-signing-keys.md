# Supply your own signing keys

Move a stack whose `NewLambda` created its signing keys to keys you supply with `LambdaArgs.Keys`, without the next apply deleting a key. The aliases the library created keep pointing at the same keys, so supplying them adopts the keys. The inputs are in [keys the estate supplies](../../../reference/sluis/pulumi-library.md#keys-the-estate-supplies).

## Before you start

- `Keys.Sign` is a symmetric key's alias. To keep an existing key, use the wrapped-signing alias, `alias/sluis-signing-wrapped` unless you changed it. Asymmetric keys of remote signing do not fit.
- The key policy admits the function's role. While `LegacySigningContext` is true, it carries `WrappedKeyPolicyStatements` for the older ring entries.

## Steps

### 1. Unprotect the keys

```sh
pulumi stack --show-urns
pulumi state unprotect 'urn:pulumi:<stack>::<project>::sluis:aws:Lambda$aws:kms/key:Key::<l>-signing-key-wrapped'
```

Repeat for each key you keep: `-signing-key`, `-signing-key-rs256`, `-signing-key-wrapped`. `<l>` is the Lambda component's name.

### 2. Drop keys and aliases from state

```sh
pulumi state delete --target-dependents 'urn:…::aws:kms/alias:Alias::<l>-signing-alias-wrapped'
pulumi state delete 'urn:…::aws:kms/key:Key::<l>-signing-key-wrapped'
```

Do the same for `-signing-alias` and `-signing-alias-rs256`. The keys stay in AWS. A role policy that depends on a key blocks the delete: add `--force` or follow the [state surgery](../upgrade/v1.62.md#6-retire-the-asymmetric-signing-keys-only-when-moving-from-kms-to-wrappedsigning).

### 3. Set `Keys`

Remove `WrappedSigning`, `SigningKeyAlias`, `SigningKeyRS256Alias` and `DisableSigningKeyRS256`.

### 4. Preview

Expect the role policy updated and the configuration layer republished (`instance`, `keys:`). A `delete` of a `kms:Key` means a key is still in state: stop.

### 5. Apply

Run `pulumi up`. Once the ring has rotated past the entries written before the runtime wrapped locally, set `LegacySigningContext` to false.
