# Enable a GitHub organisation

Switch the GitHub controller from dry run to acting in one organisation.

## Before you start

- Bind the organisation in the policy, connect it on the GitHub page ([connect a GitHub organisation](connect/github-organisation.md)), and keep it out of `policy.controllers.github.enabledOrgs`.

- Create the link App. Send people the link page the GitHub page shows ([account links](connect/github-account-links.md)). Until they link, their rows say `not linked` and their accounts are left alone.

## 1. Read the dry run

Open the organisation's section on the GitHub page after a pass. *Controller* says `dry run`. The table of people not synced is what enabling would do. `waiting on links` means only unlinked people remain, and enabling changes nothing for them. Look for unexpected removals and held rows, each with its reason.

## 2. Enable

Add the login to `policy.controllers.github.enabledOrgs` and roll out. The next pass acts, and changes appear as `roster.github_member.*` ([audit actions](../../reference/sluis/audit-actions.md)). The table shrinks.

Tell the organisation's owners that membership now follows the policy.

## 3. Handle what needs you

- A failed pass changes nothing and says why.

- *Not enough seats*: buy the seats the banner names. Nobody is invited past the last free seat.

- *Seats cannot be counted*: an owner adds organisation administration (read) to the App and accepts it for the installation.

- *Removals held*: more than half the organisation would leave in one pass. If the removals are right, press **Confirm** (`roster.github_removals.confirmed`). If they are a policy mistake, fix the policy.

- A link that is `unverifiable` adds and removes nobody. Ask the person to link again.

- A link that is `lost` was withdrawn on GitHub, and its account left the organisation. Linking again restores it at the next pass.

- A console on another policy than the controller makes the pass change nothing. The pass retries six times, up to a minute apart. Check that every console replica runs the policy the controller logged at start.

## 4. Import earlier pairings (optional)

Build `records[]{login, emails[], approved_by, approved_at}` and call `ImportGitHubLinks` as an operator with an `origin` label ([contracts](../../reference/sluis/contracts.md)). Skipped records come back with a reason. Imported links show as *imported*.

## Roll back

Remove the login from `enabledOrgs` and roll out. The organisation is left as it is, and nothing is undone. *Controller* says `dry run` again.
