# How does the GitHub controller decide?

The GitHub controller is a loop inside the sluis process, enabled by `controllers.github` in the service document ([one process](design.md#one-process)). It has no listener and no console. It holds the GitHub App keys and writes to GitHub.

Each pass makes an organisation's teams and membership match the policy's `github` table. To connect an organisation, see [connect a GitHub organisation](../../guides/sluis/connect/github-organisation.md). For the Secrets and records, see the [GitHub controller reference](../../reference/sluis/github-roster.md).

## Who is who

The controller asks the console who holds each group in the policy's `github` table. A GitHub account belongs to the person who [linked it](../../guides/sluis/connect/github-account-links.md), by the work addresses GitHub verified on it. On an Enterprise Cloud organisation, addresses in its verified domains count as well.

Two matches never displace a person's link: a [public profile address](../../guides/sluis/connect/github-account-links.md#where-a-link-comes-from) the directory has, and a pairing imported by an operator RPC after three checks.

The controller re-checks a self-link every pass. One account with addresses in two workspaces is one member. An address two accounts claim links to neither and is held.

## What a pass does

For each organisation the policy binds, a pass runs these steps:

1. **Ask who should be where.**

2. **Read GitHub whole.** Members with role, pending invitations, teams and each bound team's members in both roles: a partial read fails the pass.

3. **Change.**

    - Invite a wanted non-member who linked an account, once, into every team that wants them. Somebody who has not linked shows as *not linked*.

    - Add a member to a team in the wanted role and fix a wrong role. A maintainer group wins over a member group.

    - Remove a member nobody wants from a bound team.

    - Remove a member the directory no longer has, or has suspended, from the organisation.

    - Remove a member whose link GitHub reports gone from the organisation, without asking the directory.

4. **Hold what fails.**

    - An account that let two invitations expire since it last linked gets no third. Linking again starts over.

    - Nobody is invited without a free seat: seats minus taken seats minus pending invitations. The controller never buys a seat.

    - Removals over half the organisation's members wait for an operator to confirm that set.

5. **Act** where the organisation is in `policy.controllers.github.enabledOrgs`, and **report**. The audit trail records each change, each newly held action and each owner the policy would change.

A new organisation starts disabled, and its report is the dry run you read before adding it to `enabledOrgs`.

Before a removal, the controller asks the console about that one address and acts only on an answer the directory vouches for. An unreadable workspace or truncated list holds the removal, with the reason on the row.

## What waits for a person

| Held | What to do |
|---|---|
| no free seat | buy seats in the organisation's billing |
| seats cannot be read | approve organisation administration (read) for the App |
| removals over half the organisation | read them, then **Confirm** on the GitHub page; a confirmation covers that set and lapses after a day |
| anything in a team GitHub does not have | create the team where the organisation's structure is managed |
| an address two accounts claim | the person unlinks one |

A removal the directory cannot vouch for, and a change GitHub refused, retry every pass.

The controller adds an owner to wanted teams and promotes them where the policy says. It never removes, demotes or evicts an owner (`roster.github_owner.reported`, once). It never manages outside collaborators, a member nobody linked, a team no binding names, or anything on the organisation's `ignore` list.

The policy render refuses these:

| Refused | Meaning |
|---|---|
| a group nothing declares | the binding would read as working |
| a team with neither `members` nor `maintainers` | to remove everyone, delete the team's binding |
| an organisation binding no group and no team | to stop managing an organisation, remove it |
| the same team, or the same organisation's `members`, declared twice across merged files | the second would replace the first |

The same team name in two organisations is two teams. The service uses the App key only to uninstall on Disconnect. A workflow's identity runs the other way ([GitHub Actions](../../guides/sluis/connect/github-actions.md)).

## The loop

A pass runs every interval and when the watch sees a change. Every 30 seconds the controller hashes its credentials and records and reads the `_pass.<organisation>.json` markers that **Refresh** leaves. A changed hash or newer marker wakes the loop. Refresh is limited to one a minute per organisation and audited.

Every console answer carries the digest of its policy. The controller changes nothing on an answer under another policy. It retries within seconds, a bounded number of times, then falls back to the interval.

A failed pass is reported over the last report with rows, and a restart is not news in the audit trail.

## Reconciler rails

GitHub organisations and [Slack workspaces](slack-reconciler.md) run on `internal/rails`. It owns the pass loop, the policy-digest gate on directory answers, the held-once ledger and the journal of each target's last good report. It also owns the `Confirm` loop and the removal breaker with its dry-run switch.

Each system's package owns deriving, deciding, the change calls, status documents and audit events. Reports live in [Kubernetes objects](../../reference/sluis/kubernetes-objects.md) and [the store](store.md).

## Decided in

- [ADR 0024: Reconciler rails are shared pieces, not a framework](../../decisions/0024-reconciler-rails-are-shared-pieces-not-a-framework.md)
- [ADR 0029: Ticks per target under a lease](../../decisions/0029-ticks-per-target-under-a-lease.md)
