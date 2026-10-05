# Runner Apps

## Purpose

Create the GitHub App each self-hosted runner tier registers with, and hand its keys to the runners.

## Preconditions

- The chart declares the tiers: `config.github.runnerTiers: [preview, stable]` (the policy's
  `apps.github.runnerTiers`). The GitHub page's *Runners* tab then shows a row per bound organisation per tier.
- You are an owner of the organisation, for the install click. What a runner App is and why it is per tier:
  [connect a GitHub organisation](connect/github-organisation.md#runner-apps).

## Before you start

- **Until the App is installed its key is kept under `<tier>.<org>.pending_private_key`**, so a copy taken in between
  never replaces working runners with an App they cannot register with. A runners' copy that is empty means the App is not
  installed yet, or the copy runs before the keys exist.
- **Disconnect uninstalls the App and forgets its keys.** Runners registered with it stop taking jobs; create a new App
  for that tier and hand its keys to the runners.

## Steps

### 1. Create and install

**Run** press *Create* on the row: GitHub's create page, then its install page, by an owner of the organisation.
**Expect** the App lands in `Secret <release>-github-runner-apps` as `<tier>.<org>.github_app_id`, `.github_app_installation_id`
and `.github_app_private_key`, the keys a gha-runner-scale-set `githubConfigSecret` reads.
**Verify** a row that reads *created, not installed* means the owner stopped after Create: press *Install* on the App's
page.
**Rollback**: *Disconnect* on the row.

### 2. Hand the keys to the runners

**Run** copy the three keys to the runners, for example with a `PushSecret`.
**Expect** the runners' Secret holds all three.
**Verify** the scale set registers a runner for the organisation.
**Rollback**: remove the copy; runners already registered keep working until the App is disconnected.

## Afterwards

- Back the Secret up with the others ([back up and restore](back-up-and-restore.md)).
