# Test a policy

Catch a policy the service would refuse, or one that reads correctly and behaves differently, before it ships.

## Before you start

- A refused rollout keeps the previous pods serving the previous policy while everything reads Synced. Test before the merge.
- No `sluisctl` subcommand evaluates a person. The Go package is that interface.

## 1. Load it in CI

```sh
sluisctl policy render policy/ -o policy.yaml
```

Run it on every render of the policy. Exit 0 and the one document mean the loader's own checks passed. They catch an unknown key, a client with no `requires`, a redirect that is also a landing page and a vocabulary violation. Verify: an unknown key fails the job.

## 2. Evaluate it in a Go test

Call `policy.Parse`, then `policy.NewSet`, then `Evaluate` with an `Input`: an account with its directory groups, a CI token's claims and a client id. The result is the groups, claims and lifetime a token would carry. Pin the expectation in a fixture, as the service's own tests do.

## 3. Ask the live console

Search for a person on the console. Their page shows the internal groups, what put them in each, the merged claims, the lifetime, and every client they reach. Rules lists every rule that grants a group. A group's page and a client's page list their current holders.

Only the directory knows who is in a directory group, so check the people who should reach a client do and nobody else does.

Keep the CI render and the evaluation tests. Reference: [validation at load](../../reference/sluis/policy-validation.md).
