# Read the groups-scoping report

Find out, before enforcing [groups scoping](../../concepts/sluis/groups-in-a-token.md), which audiences would lose a group.

## Before you start

- Set `config.groupsScoping` to `report`, the default since v1.32.0. In `off` nothing is computed.

- Read the service's logs: the log store, `kubectl logs`, or CloudWatch on Lambda.

- Run report long enough to see every audience. A slow audience, such as a monthly job's token exchange, logs nothing until it runs.

- Report changes no token. The line is rate limited per audience, subject and dropped set, so lines are not tokens.

## 1. Find the lines

Filter the logs for the message below and group by `audience`.

```text
groups scoping (report mode): this token would drop groups under enforce
```

Each distinct audience, subject and dropped set gives one INFO line with `audience`, `client`, `subject` and `dropped`. Each `audience` is a client id or resource indicator you can find in the policy.

## 2. Decide for each audience

- If the audience's relying party reads the dropped groups, add a `groups:` override to its row: `groups: [thing, ...]`, or `groups: all` to keep today's shape ([keys](../../reference/sluis/policy-clients.md#groups-override)).

- If an audience logs a large, stable dropped set that nobody can explain, accept it. It is usually an unused permission.

- An audience in no finding needs nothing.

- An audience matching no gate logs "would drop everything". Give it a row, an allow-listed origin or an override.

Edit the policy and roll out. Verify: after the audience's next token, the same filter shows it gone or with a smaller set.

When every audience is accepted or overridden, [turn enforce on](turn-enforce-on.md). Read the report again after adding a client or resource.

## Roll back

Revert the policy change and roll out.
