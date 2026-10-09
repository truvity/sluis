# GitHub catalogue reference

States, drift checks and the objects a catalogue App leaves behind. To declare and connect an App see
[A catalogue of GitHub Apps](../../guides/sluis/connect/github-apps-catalogue.md).

## State and drift

Each App reads one of four states:

| State | Means |
|---|---|
| *not created* | declared, and nobody has created it on GitHub |
| *created, not installed* | the owner stopped after Create; *Install* finishes |
| *installed* | installed, and GitHub holds what the catalogue declares |
| *differs on GitHub* | GitHub holds something else; the page lists each difference |

The service asks GitHub, as the App, for the App (`GET /app`) and its
installation (`GET /app/installations/{id}`), and compares:

- **the App's permissions with the declaration.** A permission GitHub
  holds that the catalogue does not declare, one it lacks, and one at a
  different level are each a line. `metadata: read` is not drift: GitHub
  gives it to every App that touches repositories.
- **the App's events**, when the catalogue declares any.
- **the installation's accepted permissions with the App's.** When an
  owner adds a permission to an App, every installation keeps the old
  set until somebody approves the request on GitHub; until then the
  page says *approve the permission request on GitHub*.
- **the installation itself.** One uninstalled on GitHub, or suspended,
  is said as such.

What GitHub said is kept for a minute, so a page left open does not spend
the App's rate limit; *Re-check* asks again at once. A failure to ask —
GitHub down, egress closed, a damaged key — is shown as the App's reason,
and the rest of the GitHub page is unaffected.

**Why the fix is a human edit.** GitHub has no API to change an App's
permissions, events or name: a manifest creates an App once, and after
that only its owner can edit it, in the App's settings on GitHub. So the
console shows the difference and links to
`https://github.com/organizations/<org>/settings/apps/<slug>`; the owner
changes the App (or the catalogue changes to match it), approves the
installation's request if one appears, and presses *Re-check*.

## What it leaves behind

| Object | Holds | Written by |
|---|---|---|
| ConfigMap `<release>-github-apps-catalogue` | the declaration, `catalogue.yaml` | the chart, when `githubApps.catalogue` is not empty |
| Secret `<release>-github-catalogue-apps` | every catalogue App's keys and record, as above | the service, on Create and Install; created empty at start |
| PushSecret `<release>-github-app-<id>` | the copy instruction for one App's three property keys | the chart, for each entry carrying `push` |

Audit records: `roster.catalogue_app.created`, `.installed` and
`.disconnected`, each naming the App by catalogue id and carrying GitHub's
id; and `roster.github_token.minted` for every installation token asked for
([audit](../../guides/sluis/connect/github-app-tokens.md#verify)). Nothing is left behind by minting: tokens are never
kept.
