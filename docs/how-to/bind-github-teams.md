# Bind GitHub teams to internal groups

## Purpose

Make a GitHub organisation's teams and members match the holders of internal groups, from the policy.

## Preconditions

- The organisation is connected on the console, with an owner ([Connect a GitHub organisation](connect/github-organisation.md)).
- The internal groups you bind are declared in `groups` ([reference](../reference/policy-bindings.md#github-teams)).
- The teams exist in GitHub; you need each team's **slug**, not its display name.

## Before you start

- **Bind to groups, never to addresses.** A team is a consumer of an internal group; which accounts hold the group is
  answered once, in `groups`.
- **The organisation's `members` is for people with no team.** Being in a bound team implies organisation membership,
  so do not list everybody. What no team and no `members` accounts for is removed as unaccounted for.
- **`ignore` is for what nobody here controls**: a partner's address, a break-glass owner's login. Without it a bound
  group nobody can take an account out of keeps being reported.
- **Refused at load:** a team with neither `members` nor `maintainers`, an organisation binding nothing, a group nothing
  declares, a team or an organisation's `members` declared twice, and a leftover `github.<org>.owner`.
- **An organisation is born disabled.** Binding teams changes nothing until its login is in
  `controllers.github.enabledOrgs`.
- **A policy change is a new instance.** Preview before every apply, and read the preview.

## Steps

### 1. Declare the binding

**Run**: add to a policy file:

```yaml
github:
  globex:
    members: [all:globex:employee]
    teams:
      team-platform:
        members: [all:platform:engineer]
        maintainers: [all:platform:lead]
    ignore: [temp-owner]
```

**Expect**: `sluisctl policy render policy/ -o /tmp/policy.yaml` succeeds.

**Verify**: the console's GitHub pages and Rules page list the binding beside every other rule.

**Rollback**: remove the entry.

### 2. Dry run, then enable

**Run**: roll out with the organisation's login absent from `controllers.github.enabledOrgs`, read the report, then add
the login and roll out ([Enable a GitHub organisation](enable-github-organisation.md)).

**Expect**: the dry-run pass lists who would be invited, added and removed; after enabling the changes appear as
audit events.

**Verify**: a team's GitHub membership equals the holders of its groups; a holder of a maintainer group is a maintainer
even when a member group also names them.

**Rollback**: remove the login from `enabledOrgs`. Nothing is undone; that is also the emergency stop.

## Afterwards

Deleting an `ignore` line brings the account back under the bindings; `git log` says when. A person listed in no group
and in no `members` will be removed at the next pass after the organisation is enabled, so tell them first.
