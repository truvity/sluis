# Create runner Apps

Create the GitHub App each self-hosted runner tier registers with, and hand its keys to the runners. What a runner App is: [connect a GitHub organisation](connect/github-organisation.md#5-add-runner-apps).

## Before you start

- Declare the tiers: one `{purpose: runner, tier: preview}` entry in the policy's `apps.github.apps` for each. The GitHub page's *Runners* tab shows a row per bound organisation per tier.

- Be an owner of the organisation, for the install click.

- Until the App is installed, its key waits under `<tier>.<org>.pending_private_key`. An empty copy on the runners means the App is not installed yet.

## 1. Create and install

Press *Create* on the row. GitHub's create page and then its install page open. The App lands in `Secret <release>-github-runner-apps` as `<tier>.<org>.github_app_id`, `.github_app_installation_id` and `.github_app_private_key`, the keys a gha-runner-scale-set `githubConfigSecret` reads.

A row that reads *created, not installed* means the owner stopped after Create. Press *Install* on the App's page.

## 2. Hand the keys to the runners

Copy the three keys to the runners, for example with a `PushSecret`. Verify: the scale set registers a runner for the organisation.

Back the Secret up with the others ([back up and restore](operate/back-up-and-restore.md)).

## Roll back

Press *Disconnect* on the row. It uninstalls the App and forgets its keys, and runners registered with it stop taking jobs. Create a new App for that tier and hand its keys over.
