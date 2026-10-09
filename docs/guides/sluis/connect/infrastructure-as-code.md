# Connect an infrastructure-as-code program

A Pulumi or Terraform program that manages a GitHub organisation acts as a [catalogue GitHub App](github-apps-catalogue.md), the `iac` App in the [default set](github-apps-catalogue.md#1-declare-an-app). It does not act as a person or as sluis. The program owns structure: repositories, teams, rulesets and settings. sluis owns the identities and credentials.

## Before you start

- Prefer the exchange ([mint a token](github-app-tokens.md#2-mint-a-token)). Use the store only when an apply must run while sluis is upgraded, replaced or restored.

- The store holds the App's private key. Whoever reads it acts as the App, outside the audit trail.

- `push` needs `config.store: kubernetes` and an External Secrets store you already have.

| | From a secret store | Exchanged at run time |
|---|---|---|
| Program reads | a path in OpenBao, Vault or a cloud manager | an installation token from `/token` |
| Credential | the App's private key, durable | a token of at most an hour |
| Audit trail | the store's own log | one `roster.github_token.minted` record per run |

## Steps

### 1. Declare the App and its push

```yaml
githubApps:
  catalogue:
    - id: iac
      org: example-org
      description: The program that manages the organisation
      permissions:
        organization_administration: write
        members: write
        administration: write
        contents: read
        metadata: read
      installation: all
      push:
        secretStore:
          name: example-store
          kind: ClusterSecretStore # SecretStore (default) | ClusterSecretStore
        remoteKey: platform/github-apps/iac
        refreshInterval: 1h        # optional
        deletionPolicy: None       # optional; None (default) | Delete
```

`push` is off unless an entry carries it. The chart renders one External Secrets [`PushSecret`](https://external-secrets.io/latest/api/pushsecret/) per entry, `<release>-github-app-<id>`. It copies three keys and nothing else:

| In the store at `remoteKey` | From | Is |
|---|---|---|
| `app_id` | `<id>.github_app_id` | the App's numeric id |
| `installation_id` | `<id>.github_app_installation_id` | its installation on the organisation |
| `private_key` | `<id>.github_app_private_key` | the App's private key, PEM |

The keys exist once the App is installed. Two entries pushing to one path in one store are refused at render. Roll out the release.

### 2. Create and install

On the console's GitHub page, *Apps* tab, press *Create* on the App. An owner confirms GitHub's manifest, then installs on the organisation. The service writes the keys and the PushSecret copies them within its refresh interval. Roll back by removing the entry.

### 3. Read it in the program

Pulumi in Go:

```go
package main

import (
	"github.com/pulumi/pulumi-github/sdk/v6/go/github"
	"github.com/pulumi/pulumi-vault/sdk/v6/go/vault/kv"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		app, err := kv.LookupSecretV2(ctx, &kv.LookupSecretV2Args{
			Mount: "kv",
			Name:  "platform/github-apps/iac",
		})
		if err != nil {
			return err
		}

		// Mark the key secret, or the stack state holds it in the clear.
		pem := pulumi.ToSecret(pulumi.String(app.Data["private_key"])).(pulumi.StringOutput)

		gh, err := github.NewProvider(ctx, "github", &github.ProviderArgs{
			Owner: pulumi.String("example-org"),
			AppAuth: &github.ProviderAppAuthArgs{
				Id:             pulumi.String(app.Data["app_id"]),
				InstallationId: pulumi.String(app.Data["installation_id"]),
				PemFile:        pem,
			},
		})
		if err != nil {
			return err
		}

		_, err = github.NewRepository(ctx, "docs", &github.RepositoryArgs{
			Name:       pulumi.String("docs"),
			Visibility: pulumi.String("private"),
		}, pulumi.Provider(gh))
		return err
	})
}
```

Terraform:

```hcl
data "vault_kv_secret_v2" "iac" {
  mount = "kv"
  name  = "platform/github-apps/iac"
}

provider "github" {
  owner = "example-org"
  app_auth {
    id              = data.vault_kv_secret_v2.iac.data["app_id"]
    installation_id = data.vault_kv_secret_v2.iac.data["installation_id"]
    pem_file        = data.vault_kv_secret_v2.iac.data["private_key"]
  }
}
```

Terraform state holds data-source results in the clear, so use an encrypted backend. Keep the PEM's newlines: a flattened key fails with *could not parse private key*.

### 4. Use the exchange in CI instead

A job that can reach the issuer should not read the store:

```yaml
permissions:
  id-token: write
steps:
  - id: access
    uses: truvity/sluis@v1.11.0
    with:
      issuer: https://access.example.com
      github-app: ci-automation
      repositories: docs
      permissions: pull_requests:write
  - run: gh pr review --approve "$PR"
    env:
      GH_TOKEN: ${{ steps.access.outputs.github-token }}
```

A laptop or script uses `sluisctl github-token`.

## Rotate and remove

- GitHub has no API to replace an App's key. To rotate, press *Disconnect* on the App's page and create the App again. The new key goes to the same path.

- A key generated in the App's GitHub settings invalidates sluis's copy too.

- Give the path its own store policy.

- `deletionPolicy: None` leaves the copy when you remove the entry. `Delete` removes it.

- Push only Apps whose consumers cannot exchange a token.

To back up the whole Secret, see [backing it up](github-app-keys.md#2-back-it-up).
