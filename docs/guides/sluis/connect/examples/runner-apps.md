# Example: a GitHub App for each self-hosted runner tier

## Goal

Self-hosted runners register with one GitHub App per tier. The console creates it and hands it to the runners as a Secret. Nobody types or pastes a key.

## What you need

- A deployment whose chart declares the tiers, and an owner of the GitHub organisation for the install click.
- A secrets operator in the runners' cluster, such as External Secrets, or any way to copy three keys.

## The policy snippet

The tiers are values of the chart; the policy gets them as `apps.github.runnerTiers`:

```yaml
config:
  github:
    runnerTiers: [preview, stable]
```

## The exchange / command

1. Open the console's GitHub page, *Runners* tab. Each bound organisation shows a row per tier. Press *Create* on the row,
   then confirm GitHub's create page and its install page as an owner of the organisation.
2. The App lands in `Secret <release>-github-runner-apps` as `<tier>.<org>.github_app_id`, `.github_app_installation_id` and `.github_app_private_key`. A gha-runner-scale-set `githubConfigSecret` reads these keys. Copy them to the runners, for example with a `PushSecret`.

## Verify

A row reading *created, not installed* means the owner stopped after Create: press *Install* on the App's page. Until the App is installed, its key is kept as `<tier>.<org>.pending_private_key`. A copy never replaces working runners with an App they cannot register with. The scale set then registers a runner for the organisation.

## Undo

*Disconnect* on the row uninstalls the App and forgets its keys; runners registered with it stop taking jobs. Remove the
copy in the runners' namespace.

Full recipe: [Runner Apps](../../runner-apps.md). Why a runner App is per tier:
[connect a GitHub organisation](../github-organisation.md#5-add-runner-apps).

Snippet source: `tests/golden/sluis/full.yaml` carries `runnerTiers`; the snippet is accepted by the chart's values schema.
