# Declare a vocabulary

## Purpose

Make the policy refuse a grant name that is a typo, by declaring which scopes, things and roles exist.

## Preconditions

- The policy loads today without a `vocabulary` (it is optional, in force since v1.32.0).
- You know the scopes (environments, tenant ids, `all`), the things, and each thing's roles.

## Before you start

- **Strict once opted into.** The policy refuses to load if any concrete grant name anywhere (every `groups`, `claims`
  and `lifetimes` key, every `requires`, every GitHub binding) fails to fit. List the names in use first; a name you
  forgot fails the rollout.
- **`rung:` and `emp:` names are exempt**; they are not grants.
- **A wildcard key is refused without a vocabulary**, and a wildcard that expands to nothing is refused with one.
- **A policy change is a new instance.** Preview before every apply, and read the preview.
- **A build older than v1.32.0 refuses the key.** Deploy the service before the policy.

## Steps

### 1. Collect the names in use

**Run**: list every grant key and `requires` entry in the policy, for example
`grep -hoE '[a-z0-9]+:[a-z0-9-]+:[a-z-]+' policy/*.yaml | sort -u`.

**Expect**: the set of `<scope>:<thing>:<role>` names, with `rung:` and `emp:` names absent.

**Verify**: each name splits into a scope, a thing and a role you can place in the grammar ([taxonomy](../reference/taxonomy.md)).

**Rollback**: none, because this only reads.

### 2. Write the table

**Run**: add one file with the `vocabulary` table ([shape](../reference/policy-vocabulary.md)). Mark `core` and `prod`
(or your equivalents) `sensitive: true`. Use `all` only for a thing that exists once per installation. Declare roles as
implies lists (`admin: [operator]`); use `{ scopes: [...] }` for a role that makes sense on some scopes only.

**Expect**: every name from step 1 fits. `vocabulary` is declared in one file only; a second is refused.

**Verify**: `sluisctl policy render policy/ -o /tmp/policy.yaml` succeeds. A refusal names the offending name and why.

**Rollback**: remove the file.

### 3. Roll out

**Run**: roll the policy out as for any change.

**Expect**: the service starts. A refused load keeps the previous pods serving the previous policy while everything
reads Synced, so check the new pods' logs, not the sync status.

**Verify**: the new instance is ready, and the console's Rules page lists the same rules as before.

**Rollback**: revert the policy to the previous layers and roll out.

## Afterwards

Mapping wildcards (`*:k8s:admin`) become available; they never expand into a sensitive scope, so name those groups
outright. Add a render of the policy to CI ([test the policy](test-the-policy.md)) so a new name is checked in the pull
request.
