# Test a policy

## Purpose

Catch a policy the service would refuse, or one that reads correctly and behaves differently, before it ships.

## Preconditions

- The policy layers, as files or a directory.
- For the second way, a Go test in your repository; for the third, a running installation.

## Before you start

- **A refused rollout is the worst case.** The previous pods keep serving the previous policy while everything reads
  Synced, so a bad policy looks healthy. Test before it merges.
- **There is no `sluisctl` subcommand that evaluates a person.** The Go package is that interface.

## Steps

### 1. Before it ships: load it

**Run**: `sluisctl policy render policy/ -o policy.yaml` in CI, on every render of the policy.

**Expect**: exit 0 and the one document. The render runs the loader's own checks: an unknown key, a client with no
`requires`, a redirect that is also a landing page, a vocabulary violation.

**Verify**: a deliberate error (an unknown key) fails the job.

**Rollback**: none, because it writes only the output file.

### 2. In a test: evaluate it

**Run**: in a Go test, `policy.Parse`, then `policy.NewSet`, then `Evaluate` with an `Input`: an account with its
directory groups, a CI token's claims, a client id.

**Expect**: the groups, claims and lifetime a token would carry.

**Verify**: pin the expectation in a fixture, as the service's own tests do. A change to the file then changes a test.

**Rollback**: none, because it changes no state.

### 3. Live: ask the console

**Run**: on the console, search for a person.

**Expect**: their page shows the internal groups, what put them in each, the merged claims, the lifetime, and every client
with whether they reach it. Rules lists every rule that grants a group. A group's page and a client's page list the
people who hold them now.

**Verify**: the people who should reach a client do, and nobody else. The file says which directory groups count; only
the directory knows who is in them.

**Rollback**: none, because the console writes nothing into the policy.

## Afterwards

Keep the CI render and the evaluation tests. Reference: [validation at load](../../reference/sluis/policy-validation.md).
