# Read the groups-scoping report

## Purpose

Find out, before enforcing [groups scoping](../../concepts/sluis/groups-in-a-token.md), which audiences would lose a group.

## Preconditions

- `config.groupsScoping` is `report`, the default since v1.32.0. In `off` nothing is computed.
- You can read the service's logs (the log store, or `kubectl logs` on Kubernetes, CloudWatch on Lambda).
- Report has run long enough to see every audience the installation serves (see Before you start).

## Before you start

- **Silence is not proof for a slow audience.** Anything on a slow cycle, such as a monthly job's token exchange, logs
  nothing until it runs. Run report for long enough to see every audience the installation actually serves before
  treating its silence as complete.
- **Report changes no token.** The line says what enforce would do; the token is minted exactly as before, so a finding
  is information, not an incident.
- **The line is rate limited** per audience, subject and dropped set, so the count of lines is not the count of tokens.

## Steps

### 1. Find the lines

**Run**: filter the logs for the message `groups scoping (report mode): this token would drop groups under enforce`,
and group by the `audience` field.

**Expect**: one INFO line per distinct audience, subject and dropped set, with `audience`, `client`, `subject` and
`dropped` (the group names the token would stop carrying).

**Verify**: each distinct `audience` is a client id or a resource indicator you can find in the policy.

**Rollback**: none, because this only reads.

### 2. Decide for each audience

Each distinct audience is a client or resource whose consumers read a group beyond what its own `requires` names. For
each one:

- if the dropped groups are read by that audience's relying party (a console mapping a role, a workload reading a claim
  beyond membership), add a `groups:` override to its policy row: `groups: [thing, ...]` for the things it needs, or
  `groups: all` to keep today's shape while you work out which
  ([keys](../../reference/sluis/policy-clients.md#groups-override));
- if an audience logs a large, stable dropped set for every subject and nobody can say why, that is usually an unused
  permission a caller was never meant to see. The report finding it is the feature working, not a bug to route around
  with an override;
- an audience that appears in no finding already carries what scoping would keep and needs nothing.

An audience that matches no gate at all (not a declared client, resource or permitted document origin) logs "would drop
everything". Give it a row, an allow-listed origin or an override.

**Run**: edit the policy and roll it out. A policy change is a new instance: a rollout on Kubernetes, a new
configuration layer on Lambda.

**Expect**: the audience stops appearing in the report, or appears with a smaller dropped set.

**Verify**: the same filter, after the rollout and after the audience's next token.

**Rollback**: revert the policy change and roll out again.

## Afterwards

When every audience is accepted or given an override, [turn enforce on](turn-enforce-on.md). Re-read the report after any
new client or resource is added.
