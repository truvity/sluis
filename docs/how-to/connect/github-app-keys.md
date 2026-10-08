# Keep and back up a GitHub App's key

Where a catalogue App's private key lives, how to back it up, and how to hand one App to a consumer. The App itself is
declared and created as in [A catalogue of GitHub Apps](github-apps-catalogue.md).

## Where the key is kept

Every catalogue App is in one Secret, `<release>-github-catalogue-apps`,
created empty at the service's first start:

| Key | Holds |
|---|---|
| `<id>.github_app_id` | the App's numeric id |
| `<id>.github_app_installation_id` | the installation on the organisation |
| `<id>.github_app_private_key` | the App's private key, PEM, exactly as GitHub issued it |
| `<id>.record.json` | the App's record: `version`, `id`, `org`, `app_id`, `app_slug`, `installation_id`, `html_url`, `connected_at`, `connected_by` |
| `<id>.pending_private_key` | the key of an App created and **not yet installed**, instead of the three above |

The three property keys exist only once the App is installed, so a copy
taken between the two clicks never hands anything an App that cannot
mint a token. No copy of a key exists anywhere else — not in git, not in
a password manager.

### Backing it up

A deployment copies the Secret to a store that travels with its backups.
An External Secrets `PushSecret` does that for any provider External
Secrets writes to — a Vault or OpenBao, a cloud secret manager — and the
restore is the matching `ExternalSecret`:

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

One block of four entries per App. The same keys are what anything else
that needs the App — a job that mints its own tokens today — reads, under
names that do not change for the life of the App.

**Restoring** is putting the Secret back, with its label
`access-roster.truvity.github.io/kind: github-catalogue-apps`, before the
service starts. The records are beside the keys, so nothing else is
needed ([configuration](../back-up-and-restore.md)).

That is a **backup**: every App, every key, one place, read by nobody
until a restore. Handing one App to a consumer is the next section, and
it is deliberately a different object.

### Projecting one App to a secret store

> **On a State adapter, read the document.** `push` is **deprecated**: it renders a
> PushSecret over the Kubernetes Secret only the `legacy` storage writes. With
> `ports.adapter` other than `legacy` and layout v4 the installed App is the `github/v1` document at
> `external/github/<id>`, which the consumer reads with its own grant
> ([secrets](../../reference/secrets.md#the-external-documents),
> [0041](../../decisions/0041-the-secret-contract.md)).

Some consumers cannot ask the issuer at the moment they run. The one this
was built for is the program that manages the estate — a Pulumi or
Terraform apply that must work while this service is being upgraded,
replaced or restored. For those, an entry may carry `push`, and the chart
renders a `PushSecret` that copies **that App's three property keys** to
a store and a path the operator names:

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

| At `remoteKey` | From the Secret | Is |
|---|---|---|
| `app_id` | `<id>.github_app_id` | the App's numeric id |
| `installation_id` | `<id>.github_app_installation_id` | its installation on the organisation |
| `private_key` | `<id>.github_app_private_key` | the App's private key, PEM |

- **Off unless written.** No entry pushes anything by default, and the
  chart invents neither the store nor the path.
- **Three keys, never the Secret.** The record is not pushed, and no
  other App's keys are in the object. The three exist only once the App
  is installed, so an App created and left uninstalled pushes nothing.
- **Refused at render:** two entries pushing to one path in one store
  (one would overwrite the other, and the reader could not tell which
  App's key it held), and `push` without `config.store: kubernetes`
  (there would be no Secret to push from).
- **What lands there is a real credential** — the App's private key, a
  second durable copy, to be rotated as one, and the store that holds it
  is in the App's blast radius.

The worked consumer, the rotation procedure and when to prefer the
run-time exchange instead are in
[Connect an infrastructure-as-code program](infrastructure-as-code.md).
