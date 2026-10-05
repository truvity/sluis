# How a GitHub pass decides

Each pass of the [GitHub controller](github-controller.md) makes an organisation's teams and membership match the
policy's `github` table. This page is why it does what it does: what a pass reads, what it changes, what it holds for a
person and what it never touches. To connect an organisation see
[Connect a GitHub organisation](../how-to/connect/github-organisation.md); for the Secrets and records see
[GitHub roster reference](../reference/github-roster.md).

## What it does every pass

For each organisation the policy binds:

1. **Who should be where.** It asks the console who holds each bound
   group. A suspended account is not somebody a team should contain.
2. **What GitHub holds.** Through the organisation's App: members with
   their role, pending invitations, teams, and each bound team's members
   in both roles. All of it, or the pass fails: a partial read acted on is
   how the wrong people get removed.
3. **Who is who.** A GitHub account belongs to the person who
   [linked it](../how-to/connect/github-account-links.md), by the work addresses GitHub verified
   on it. On an organisation on GitHub's Enterprise Cloud plan, members'
   addresses in its verified domains count as well; on every other plan
   GitHub discloses no member's address, and a link is the only way. A
   member nobody linked whose public profile shows a work address the
   directory has, live, is [linked from the profile](../how-to/connect/github-account-links.md#where-a-link-comes-from).
   One account with addresses in two workspaces is one member. An address
   two accounts claim is linked to neither, and held.
4. **What to change.**
   - somebody wanted and not a member, who linked an account, is
     **invited** as that account, straight into every team that wants
     them — once. Somebody who has not linked is *not linked*: waiting on
     them, not held, because there is nobody to invite;
   - a member wanted in a team is **added** in the role wanted, and a
     wrong role is **changed**; a maintainer group wins over a member
     group;
   - a member of a bound team whom no bound group wants is **removed**
     from that team;
   - a member the directory **no longer has** — not found, or suspended —
     is **removed from the organisation**, and so from every team;
   - a member whose link **GitHub says is gone** — the address removed or
     unverified, or the authorization revoked — is **removed from the
     organisation** at once. It is the one removal that does not ask the
     directory: the account is no longer shown to be anybody's.
5. **Check the organisation as a whole**, and hold what fails:
   - an account that let **two invitations expire** since it last linked is
     not invited a third time; linking again starts over;
   - nobody is invited without a **free seat** — seats minus taken seats
     minus pending invitations — and nobody at all while the seats cannot
     be read. The controller never buys a seat, and GitHub would either
     buy one or refuse;
   - if the removals concern **more than half the organisation's members**,
     nobody is removed until an operator confirms exactly that set.
6. **Act** where the organisation is in `enabledOrgs`, and **report**: the
   GitHub page shows every person's state and what comes next, and each
   change, each newly held action and each owner the policy would change
   is recorded in the audit trail.

**A removal never rests on absence.** Before anybody is removed, the
controller asks the console about that one address, and acts only on an
answer the directory vouches for. An unreadable workspace, a truncated
list, a directory mid-outage: each holds the removal, with the reason on
the row, and removes nobody.

**Held until a person acts** — shown as *needs you*:

| Held | What to do |
|---|---|
| no free seat | buy seats in the organisation's billing |
| seats cannot be read | approve organisation administration (read) for the App |
| removals over half the organisation | read them, then **Confirm** on the GitHub page — for exactly that set; a different set needs confirming again, and a confirmation lapses after a day |
| anything in a team GitHub does not have | create the team where the organisation's structure is managed |
| an address two accounts claim | the person unlinks one |

**Retried every pass**, because it clears on its own: a removal the
directory cannot vouch for right now, and a change GitHub refused (its
words are on the row).

**Owners are added, never taken away.** Owners and billing are managed
outside, and an organisation's break-glass seat must keep what it has. An
owner is added to the teams the policy wants them in and promoted to
maintainer where it wants that, like anybody; an owner is never removed
from a team, never demoted in one, and never removed from the
organisation. Each of those is said on the page and in the audit trail
(`roster.github_owner.reported`), once.
**Outside collaborators** are listed and never managed.

**Never touched:** a member nobody linked (listed as *not linked*), a
team no binding names, anybody's owner status, and anything the
organisation's `ignore` list names — an address in a bound group that
nobody here can take out of it, a temporary owner. The organisation's page
lists what is ignored.

## What the render refuses, and why each is silent otherwise

| Refused | Because |
|---|---|
| a group nothing declares | the binding would name something with no meaning, and read as though it worked |
| a team with neither `members` nor `maintainers` | *remove everyone from platform* is not something to express by leaving a list out |
| an organisation binding no group and no team | *stop managing this organisation* is expressed by removing it |
| the same team declared twice across two merged files | the second would replace the first, with nothing to see |
| the same organisation's `members` declared twice | the same reason |

The same team name in two **organisations** is fine — `team-platform` on
two orgs is two different teams.

## What the service never does here

The service never acts on GitHub with the key it keeps: the controller
does, from its own process. The one use the service makes of it is
uninstalling on Disconnect. It mints no token for GitHub. A workflow's identity is the other direction
entirely and is [GitHub Actions](../how-to/connect/github-actions.md): GitHub proves a
job to us, and we never prove anything to GitHub.
