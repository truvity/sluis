# GitHub catalogue reference

States, drift checks and the objects a catalogue App leaves behind. Declared in `apps.github.apps` of the [policy document](policy-document.md). Setup: [A catalogue of GitHub Apps](../../guides/sluis/connect/github-apps-catalogue.md).

## State

| State | Means |
|---|---|
| *not created* | Declared; nobody has created it on GitHub |
| *created, not installed* | The owner stopped after Create; *Install* finishes |
| *installed* | Installed, and GitHub holds what the catalogue declares |
| *differs on GitHub* | GitHub holds something else; the page lists each difference |

## Drift

The service asks GitHub, as the App, for `GET /app` and `GET /app/installations/{id}`, and compares:

| Check | Reported |
|---|---|
| App permissions against the declaration | Each extra, missing or different-level permission is a line. `metadata: read` is not drift. |
| App events | Compared when the catalogue declares any |
| Installation permissions against the App's | *approve the permission request on GitHub* until an owner approves the new permission |
| The installation itself | Uninstalled or suspended is said as such |

GitHub's answer is kept for one minute. *Re-check* asks again at once.
A failure to ask, such as GitHub down or a damaged key, is shown as the App's reason. The rest of the GitHub page is unaffected.

GitHub has no API to change an App's permissions, events or name. To fix drift, edit the App at `https://github.com/organizations/<org>/settings/apps/<slug>` or change the catalogue, approve the installation's request, and press *Re-check*.

## Objects

| Object | Holds | Written by |
|---|---|---|
| ConfigMap `<release>-policy` | The declaration, `apps.github.apps` | The chart, when `githubApps.catalogue` is not empty |
| Secret `<release>-github-catalogue-apps` | Every catalogue App's keys and record | The service, on Create and Install; created empty at start |
| PushSecret `<release>-github-app-<id>` | The copy instruction for one App's three property keys | The chart, for each entry carrying `push` |

Minting leaves nothing behind: tokens are never kept.

## Audit

| Action | Records |
|---|---|
| `roster.catalogue_app.created`, `.installed`, `.disconnected` | The App by catalogue id, with GitHub's id |
| `roster.github_token.minted` | Every installation token asked for: [audit](../../guides/sluis/connect/github-app-tokens.md) |
