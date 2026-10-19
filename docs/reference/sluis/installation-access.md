# Build the installation's access section

`config.FromAccess` turns the derived part of an access document ([policy document](policy-document.md)) and the installation's bootstrap rows into the `access` section of the [installation document](installation-document.md).

```go
access, err := config.FromAccess(doc.Access, config.Bootstrap{
	Recovery: &config.BootstrapRecovery{
		ServiceAccount: config.BootstrapServiceAccount{Namespace: "sluis", Name: "sluis-recovery"},
		Groups:         []string{"all:roster:operator"},
	},
	Viewers: &config.BootstrapViewers{Group: "all:roster:viewer", EmailDomain: "example.com"},
})
in.Access = &access
```

## Rows

Each row is optional. A row folds into its groups after the matchers the access document derived.

| Field | Admits | Into |
|---|---|---|
| `Recovery` | one ServiceAccount; leave the row out on Lambda, where recovery is the password | each group in `Groups`, which may be derived groups |
| `Viewers` | `EmailDomain`, `ServiceAccounts`, `RoleARNs` | `Group`, which may be a derived group |
| `Controllers` | `ServiceAccounts`, `RoleARNs` | the viewers' group and, when `Group` is set, `Group` |
| `Probe` | one ServiceAccount, cluster required | `Group`, plus the exchange client `Client` that requires it |

`GenerateSecrets` writes every confidential client's secret as `{generate: true}`. `Claims` is copied to the `claims` table, which the access document does not have.

## Refusals

`FromAccess` refuses:

- a namespace, name, cluster, group or role ARN that is malformed;
- a workload listed twice in one row;
- a `Controllers.Group` or `Probe.Group` that the access document also declares, or that two rows share;
- a `Probe.Client` that the access document also declares;
- an access document that declares a group, client or person twice.

## Fields

No field of the access part is dropped, except seven client fields that describe the relying party's own deployment: `secretKey`, `hostname`, `prefix`, `mount`, `cluster`, `proxy` and `deliver`. `FromAccess` does not validate the policy; [`config.Render`](installation-document.md) does.
