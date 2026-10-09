# Declare a vocabulary

Make the policy refuse a grant name that is a typo, by declaring which scopes, things and roles exist.

## Before you start

- Once declared, the policy refuses to load if any concrete grant name fails to fit. That covers every `groups`, `claims` and `lifetimes` key, every `requires` and every GitHub binding. List the names first.

- `rung:` and `emp:` names are exempt.

- Builds older than v1.32.0 refuse the key. Deploy the service before the policy.

## 1. Collect the names in use

```sh
grep -hoE '[a-z0-9]+:[a-z0-9-]+:[a-z-]+' policy/*.yaml | sort -u
```

Each name is a `<scope>:<thing>:<role>` that fits the [taxonomy](../../reference/sluis/taxonomy.md).

## 2. Write the table

Add one file with the `vocabulary` table ([shape](../../reference/sluis/policy-vocabulary.md)); a second file is refused. Mark `core` and `prod`, or your equivalents, `sensitive: true`. Use `all` only for a thing that exists once per installation. Declare roles as implies lists such as `admin: [operator]`, and use `{ scopes: [...] }` for a role that applies to some scopes only.

```sh
sluisctl policy render policy/ -o /tmp/policy.yaml
```

A refusal names the offending name. A wildcard key is refused without a vocabulary, and a wildcard that expands to nothing is refused with one.

## 3. Roll out

Preview, then roll out. A refused load keeps the previous pods serving while everything reads Synced, so read the new pods' logs, not the sync status. Verify: the Rules page lists the same rules as before.

Mapping wildcards such as `*:k8s:admin` now work. They never expand into a sensitive scope, so name those groups outright. Add a render to CI ([test the policy](test-the-policy.md)).

## Roll back

Revert the policy to the previous layers and roll out.

## Decided in

[ADR 0010](../../decisions/0010-a-declared-vocabulary.md).
