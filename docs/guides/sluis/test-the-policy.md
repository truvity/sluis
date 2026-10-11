# Test a policy

Catch a policy the service would refuse, or one that reads correctly and behaves differently, before it ships.

## Before you start

- A refused rollout keeps the previous pods serving the previous policy while everything reads Synced. Test before the merge.
- `sluisctl explain` evaluates a caller against a rendered policy, offline. The Go package is the interface for a test.

## 1. Load it in CI

```sh
sluisctl policy render policy/ -o policy.yaml
```

Run it on every render of the policy. Exit 0 and the one document mean the loader's own checks passed. They catch an unknown key, a client with no `requires`, a redirect that is also a landing page and a vocabulary violation. Verify: an unknown key fails the job.

## 2. Evaluate it in a Go test

Call `policy.Parse`, then `policy.NewSet`, then `Evaluate` with an `Input`: an account with its directory groups, a CI token's claims and a client id. The result is the groups, claims and lifetime a token would carry. Pin the expectation in a fixture, as the service's own tests do.

## 3. Explain one token

```sh
sluisctl explain --policy policy.yaml --audience logs \
    --email ana@example.test --directory-group dms-team@example.test
```

The output is the groups the token keeps for that client, the groups scoping drops with the reason, the claims the kept groups add, the lifetime and the required group that admits the caller. Use `--subject` for a workload. Verify: a client that is not declared is refused.

As an operator you can ask the running issuer instead: use `--live` in place of `--policy`, and leave out `--directory-group`.

```sh
sluisctl explain --live --audience logs --email ana@example.test
```

It answers from the directory snapshots the issuer holds, so a group joined a minute ago may not show yet. The ask is audited. Verify: the output matches the offline one for the same groups.

## 4. Ask the live console

Search for a person on the console. Their page shows the internal groups, what put them in each, the merged claims, the lifetime, and every client they reach. Rules lists every rule that grants a group. A group's page and a client's page list their current holders.

Only the directory knows who is in a directory group, so check the people who should reach a client do and nobody else does.

Keep the CI render and the evaluation tests. Reference: [validation at load](../../reference/sluis/policy-validation.md).
