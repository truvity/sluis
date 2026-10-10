# A catalogue of GitHub Apps

Declare Apps in values, create them from the console. See [Mint a GitHub App token](github-app-tokens.md), [Keep and back up a GitHub App's key](github-app-keys.md) and the [catalogue reference](../../../reference/sluis/github-apps.md).

## Before you start

- The service refuses to start on an unknown key, duplicate id, bad level, name or glob, a grant above the App, or an undeclared group.

- A created App's permissions change only by an owner's edit on GitHub.

- `workflows: write` is required to push under `.github/workflows`. `metadata: read` is never drift.

- Give an approving App no administration, and keep an administering App out of the approving path.

## 1. Declare an App

```yaml
githubApps:
  catalogue:
    - id: renovate                 # [a-z0-9-], at most 32, unique; never changes
      org: example-org             # the organisation the App is created under
      name: example-org-renovate   # optional; default <org>-<id>, at most 34
      description: Dependency updates for example-org
      public: false                # a private App installs only on example-org
      permissions:                 # GitHub's permission names: read | write | admin
        contents: write
        pull_requests: write
        issues: write
        workflows: write
      events: []                   # optional; delivered only if `webhook` is set
      export: true                 # set it with a `webhook`: consumers read its secret from the exported document
      # webhook:                   # optional; see "Deliver events"
      #   url: https://argocd.example/api/webhook
      installation: all            # all | selected (default): what the installer is expected to pick
      grants:
        - group: all:platform:engineer
          repositories: ["*"]
          permissions: {contents: read, pull_requests: read}
```

| Field | Meaning |
|---|---|
| `id` | Where the App is kept and what a token request names. Renaming it forgets the App |
| `org` | The organisation login. An App created under another is refused |
| `name` | The global GitHub name. A declared name over 34 characters is refused |
| `description` | Shown on GitHub and in the console |
| `public` | Set `true` only when something outside the organisation installs it |
| `permissions` | REST permission names at `read`, `write` or `admin`. At least one |
| `events` | Webhook events. Delivered only to an App that declares a `webhook` |
| `webhook` | A `url` or a `kargo` receiver. Needs non-empty `events` |
| `installation` | The choice the deployment expects on GitHub's install page. The console shows both |
| `grants` | Who may mint a token: [Mint a GitHub App token](github-app-tokens.md) |
| `push` | Optional: [project the key to a store](github-app-keys.md#4-project-one-app-to-a-consumer-deprecated) |

The chart renders each entry as `purpose: catalogue` in the policy's `apps.github.apps`. Copy the default set from `charts/sluis/examples/github-apps.yaml` and add `grants` and the [`push` block for `iac`](infrastructure-as-code.md).

## 2. Create and install

1. An operator presses **Create** on the App's page in the *Apps* tab. An owner confirms the manifest. GitHub returns to `/connect/github/catalogue/callback` and the service exchanges the code for the id and key.
2. The owner picks repositories on the install page. GitHub returns to `/connect/github/catalogue/setup` and the service asks GitHub where the App is installed.

After a stop between the steps, press **Install**. Operators of the owning directory, or the installation-wide operator, may create, install, re-check and disconnect ([ownership](github-organisation.md#2-connect-the-organisation)).

## 3. Deliver events (preview)

Declare `webhook:` to enable delivery. It is untested against real GitHub. Disconnect and recreate an existing App to add one.

The service generates the secret. Set `export: true`, and consumers read `webhook_secret` from `external/github/<id>` after install. For Argo CD:

```yaml
githubApps:
  catalogue:
    - id: argocd-webhook
      org: example-org
      permissions: {contents: read}
      events: [push]
      export: true
      webhook:
        url: https://argocd.example/api/webhook
```

```yaml
data:
  - secretKey: webhook.github.secret      # key in argocd-secret
    remoteRef: {key: github/argocd-webhook, property: webhook_secret}
```

Kargo derives the receiver path from the secret: `<base>/github/` plus the hex SHA-256 of project, receiver name and secret joined with nothing. Give the receiver the same `webhook_secret`:

```yaml
      webhook:
        kargo:
          base: https://kargo-webhooks.example
          receiver: github            # the receiver's name in the project
          project: apps               # empty for a cluster-scoped receiver
```

Rotate with the RPC `RotateGitHubAppWebhook` ([contract](../../../reference/sluis/contracts.md)); the console has no button yet. If the target does not accept the new secret within two minutes, the old one is restored. 

## Verify

The App reads *installed* and *Check* shows no difference.

## Roll back

**Disconnect** uninstalls the App and forgets its record and keys, even if the uninstall fails. Its owner deletes the registration in the App's settings; the console links there. A removed entry stays listed as *no longer declared*.
