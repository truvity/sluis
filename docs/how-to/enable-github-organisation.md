# Enable a GitHub organisation

## Purpose

Switch the GitHub controller from dry run to acting in one organisation, and know how to stop it.

## Preconditions

- The organisation is bound in the policy, **connected** on the GitHub page ([connect a GitHub
  organisation](connect/github-organisation.md)), and the controller runs with the organisation *not* in
  `policy.controllers.github.enabledOrgs`.
- The **link App** is created, and the people who belong in the organisation have linked their accounts
  ([account links](connect/github-account-links.md)): send them the link page the GitHub page shows. Until somebody
  links, their rows say `not linked` and their accounts are left alone.

## Before you start

- **A console answering under a different policy than the controller loaded is a pass that changes nothing.** The two
  restart at different moments during a rollout, so the pass is tried again after 5 seconds, then twice as long each
  time, up to a minute, six times. A difference that outlasts those retries is not a rollout: check that every console
  replica runs the policy the controller logged at start (`policy` on "the GitHub controller is assembled").
- **A failed pass changes nothing** and is reported over the last report that had rows, so the page does not blank. It
  says why: an organisation not connected, an App GitHub refuses, a console that did not answer.
- **Three things need you:**
  - *Not enough seats*: buy the seats the banner names in the organisation's billing on GitHub. Nobody is invited past
    the last free seat.
  - *Seats cannot be counted*: the organisation's App lacks organisation administration (read). An owner adds it in the
    App's settings on GitHub and accepts it for the installation. An App created from 1.5.0 on asks for it already.
  - *Removals held*: more than half the organisation would leave in one pass. Read the removals; if they are right, press
    **Confirm** (`roster.github_removals.confirmed`). If they are a policy mistake, fix the policy: the set changes and
    the confirmation would not cover it anyway.
- **A link that is `unverifiable`** adds and removes nobody: its token pair was lost in an interrupted renewal, or the
  link App was replaced. Ask the person to open the link page and link again. **A link that is `lost`** was withdrawn on
  GitHub, and its account left the organisation (`roster.github_link.lost`, then `roster.github_member.removed`);
  linking again brings it back on the next pass.

## Steps

### 1. Read the dry run

**Run** open the organisation's section on the GitHub page after a pass.
**Expect** *Controller* says `dry run`; the table of people not synced is exactly what enabling would do. *Controller*
says `waiting on links` when the only thing left is people who have not linked: that is not in sync, and enabling changes
nothing for them.
**Verify** look for anybody you did not expect to be removed, and for held rows: each carries its reason.
**Rollback**: none, because a dry run changes nothing.

### 2. Enable

**Run** add the login to `policy.controllers.github.enabledOrgs` and roll out.
**Expect** the next pass acts. Its changes appear in the audit trail as `roster.github_member.*`
([audit actions](../reference/audit-actions.md)).
**Verify** *Controller* says it acts, and the table of people not synced shrinks.
**Rollback**: step 3.

### 3. Stop

**Run** remove the login from `enabledOrgs` and roll out.
**Expect** the organisation is simply left as it is. Nothing is undone.
**Verify** *Controller* says `dry run`.
**Rollback**: add the login again.

### 4. Optional: import github-roster 0.x pairings

**Run** once, from a machine that can read `/roster/people/*` in SSM, build `records[]{login, emails[], approved_by,
approved_at}` and call `ImportGitHubLinks` with `origin: "github-roster 0.x"` as an operator
([contracts](../reference/contracts.md)).
**Expect** every skipped record comes back with its reason; imported ones show as *imported* on the GitHub page.
**Verify** the GitHub page's links list.
**Rollback**: each imported link can be removed on its page.

## Afterwards

- Watch the first passes and the breaker. Tell the organisation's owners that membership now follows the policy.
