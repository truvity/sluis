# Keep and back up a GitHub App's key

Back up a catalogue App's private key and hand one App to a consumer. Declare and create the App as in [A catalogue of GitHub Apps](github-apps-catalogue.md).

## Before you start

- No copy of a key exists outside the Secret: not in git, not in a password manager.
- A pushed key is a second durable copy of a real credential. Rotate both together; the store is in the App's blast radius.
- With a `ports.adapter` other than `legacy` and layout v4, read the `github/v1` document at `external/github/<id>` instead of the Secret. See [secrets](../../../reference/sluis/secrets.md#the-external-documents) and [ADR 0041](../../../decisions/0041-the-secret-contract.md).

## 1. Find the key

The service creates the Secret `<release>-github-catalogue-apps` empty at its first start.

| Key | Holds |
|---|---|
| `<id>.github_app_id` | The App's numeric id |
| `<id>.github_app_installation_id` | The installation on the organisation |
| `<id>.github_app_private_key` | The private key, PEM as GitHub issued it |
| `<id>.record.json` | `version`, `id`, `org`, `app_id`, `app_slug`, `installation_id`, `html_url`, `connected_at`, `connected_by`; with a webhook also `webhook_url`, `hook_rotated_at`. Nothing secret |
| `<id>.pending_private_key` | The key of an App created and not yet installed, instead of the three above |
| `<id>.webhook_secret` | Only for an App with a `webhook`: kept from creation through install ([Deliver events](github-apps-catalogue.md#3-deliver-events-preview)) |

The three property keys exist only once the App is installed.

## 2. Back it up

Copy the Secret to a store that travels with your backups. An External Secrets `PushSecret` writes to any provider External Secrets supports:

```yaml
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata:
  name: sluis-github-catalogue-apps
  namespace: sluis
spec:
  refreshInterval: 1h
  deletionPolicy: None            # a forgotten App's copy stays until removed by hand
  secretStoreRefs:
    - name: backup-store          # a SecretStore for your Vault, OpenBao or cloud manager
      kind: SecretStore
  selector:
    secret:
      name: sluis-github-catalogue-apps
  data:
    - match:
        secretKey: renovate.github_app_private_key
        remoteRef:
          remoteKey: sluis/github-apps/renovate
          property: private_key
    - match:
        secretKey: renovate.github_app_id
        remoteRef:
          remoteKey: sluis/github-apps/renovate
          property: app_id
    - match:
        secretKey: renovate.github_app_installation_id
        remoteRef:
          remoteKey: sluis/github-apps/renovate
          property: installation_id
    - match:
        secretKey: renovate.record.json
        remoteRef:
          remoteKey: sluis/github-apps/renovate
          property: record
```

Repeat the four entries per App. The restore is the matching `ExternalSecret`.

## 3. Restore

Put the Secret back with its label `access-roster.truvity.github.io/kind: github-catalogue-apps` before the service starts. The records are beside the keys. See [back up and restore](../operate/back-up-and-restore.md).

## 4. Project one App to a consumer (deprecated)

A program that must work while this service is upgraded or restored, such as a Pulumi or Terraform apply, cannot ask the issuer. Add `push` to the entry and the chart renders a `PushSecret` for that App's three property keys:

```yaml
githubApps:
  catalogue:
    - id: iac
      org: example-org
      permissions: {organization_administration: write, members: write, administration: write, contents: read}
      installation: all
      push:
        secretStore:
          name: example-store        # a store you already have
          kind: ClusterSecretStore   # SecretStore (default) | ClusterSecretStore
        remoteKey: platform/github-apps/iac
        refreshInterval: 1h          # optional; 1h
        deletionPolicy: None         # optional; None (default) | Delete
```

| At `remoteKey` | From the Secret |
|---|---|
| `app_id` | `<id>.github_app_id` |
| `installation_id` | `<id>.github_app_installation_id` |
| `private_key` | `<id>.github_app_private_key` |

Nothing is pushed unless you write `push`. The record is never pushed, and an App created but not installed pushes nothing. The chart refuses two entries pushing to one path in one store, and `push` without `config.store: kubernetes`.

## Verify

Read `remoteKey` in the store and check the three properties. For the worked consumer, rotation and the run-time exchange, see [Connect an infrastructure-as-code program](infrastructure-as-code.md).
