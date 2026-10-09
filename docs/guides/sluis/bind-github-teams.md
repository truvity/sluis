# Bind GitHub teams to internal groups

Make a GitHub organisation's teams and members match the holders of internal groups.

## Before you start

- Connect the organisation with an owner ([connect a GitHub organisation](connect/github-organisation.md)) and declare the groups in `groups`.

- Use each team's slug, not its display name.

- List in `members` only people with no team. Anyone no team and no `members` entry accounts for is removed.

- Use `ignore` for accounts nobody here controls, such as a partner or a break-glass owner.

## 1. Declare the binding

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

Render the policy. The load refuses a team with neither `members` nor `maintainers`, an organisation binding nothing, an undeclared group, a duplicate and a leftover `github.<org>.owner` ([bindings](../../reference/sluis/policy-bindings.md#github-teams)).

```sh
sluisctl policy render policy/ -o /tmp/policy.yaml
```

Verify: the console's Rules page lists the binding.

## 2. Roll out as a dry run

Roll out with the organisation's login absent from `controllers.github.enabledOrgs`. The pass lists who would be invited, added and removed.

## 3. Enable

Add the login to `enabledOrgs` and roll out, as in [enable a GitHub organisation](enable-github-organisation.md). A person in no group and no `members` is removed at the next pass, so tell them first.

Verify: each team's membership equals the holders of its groups. A holder of a maintainer group is a maintainer even when a member group also names them.

## Roll back

Remove the login from `enabledOrgs`. Nothing is undone, and this is the emergency stop. To remove a binding, delete the entry. Deleting an `ignore` line puts the account back under the bindings.
