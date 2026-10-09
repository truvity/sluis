# Taxonomy

The grammar of a grant name. What a name is checked against is [policy vocabulary](policy-vocabulary.md).

## Grammar

A grant is `<scope>:<thing>:<role>`: three lowercase segments, read as *role, on thing, in scope*.

| Segment | Is | Examples |
|---|---|---|
| `scope` | an environment, a tenant id, or `all` | `core`, `prod`, `devel`, `C0north`, `all` |
| `thing` | a subsystem, project or application | `k8s`, `argocd`, `grafana`, `shop`, `sluis` |
| `role` | a role from that thing's ladder | `viewer`, `operator`, `admin`, `deployer`, `editor` |

Two families of two segments are not grants and are exempt from this page: `rung:<name>` carries a session lifetime, and `emp:<slug>` is a person.
Any other shape is refused at load: see [validation](policy-validation.md#refused-at-load).

## Rules

| Rule | Behaviour |
|---|---|
| `all` | Use it for a thing that exists once per installation, such as `all:grafana:viewer`. A per-environment thing is `devel:k8s:viewer` and `prod:k8s:viewer`, never `all:k8s:viewer`. |
| Sensitive scope | A [declared vocabulary](policy-vocabulary.md) marks a scope `sensitive`, typically `core` and `prod`. A mapping wildcard never expands into it. A concrete name such as `core:k8s:admin` is unaffected. |
| Coverage | A role may not cover more scopes than a role it implies. `admin` on every scope implying `user` on `devel` is refused at load. |

## Inheritance

A ladder is explicit implies: `admin: [operator]`. Holding `S:T:admin` holds every role it implies, transitively, on the same `S:T`.
The expansion happens once at [evaluation](policy-groups.md#groups-to-token-by-deep-merge).
There is no scope inheritance: `all:x:admin` never implies `devel:x:admin`.

## Per-role scopes

A role value `user: { scopes: [devel] }` narrows the role inside its thing's scopes.
`core:ssh:user` is refused when `user` excludes `core`. Syntax: [per-role scopes](policy-vocabulary.md#per-role-scopes).

## Mapping wildcards

`*` is allowed in the scope or thing position of a `groups` key only.
It is refused in `requires`, GitHub bindings, a Slack channel's `from`, `claims`, `lifetimes` and the role position.
`*:k8s:admin` and `devel:*:viewer` are valid. `devel:k8s:*` and `*:*:*` are not.
Without a declared vocabulary, `*` is refused.

A wildcard key stands for every concrete `(scope, thing)` that declares the scope and the role, where the role allows the scope. Sensitive scopes are excluded.
Whoever matches the key is in all those groups, unioned with matching concrete keys, and then inheritance applies.
Later stages see only concrete names.

A wildcard that expands to nothing fails the load:

| Key | Reason |
|---|---|
| `prod:*:viewer`, `prod` sensitive | names a sensitive scope directly; write the concrete group |
| `devel:*:admins`, no thing has `admins` | expands to no group |
| `*:k8s:viewer`, every `k8s` scope sensitive | expands to no group |
| `core:*:user`, every `user` role excludes `core` | expands to no group |

## Examples

| Name | Reads as |
|---|---|
| `prod:k8s:admin` | admin of prod's Kubernetes |
| `all:access-roster:operator` | operator of sluis, installation-wide |
| `C0north:access-roster:viewer` | viewer of one directory only |
| `C0north:access-roster:operator` | operator of one directory and the GitHub organisations and Slack workspaces it owns |
| `*:k8s:admin` (`groups` key only) | admin of every non-sensitive environment's Kubernetes |
| `devel:*:viewer` (`groups` key only) | viewer of everything devel has with a viewer role |
| `devel:ssh:user` | user of devel's ssh, a role scoped to `devel` |
| `rung:sre` | a session lifetime, not a grant |
| `emp:alice` | a person, not a grant |

The thing segment of the sluis groups is a legacy identifier, renamed in v1.75–v1.76.

## Anti-patterns

| Anti-pattern | Fix |
|---|---|
| A name that is not `<scope>:<thing>:<role>`, `rung:` or `emp:` | Use the grammar. |
| A provider's vocabulary in the name, such as `cluster-prod:cluster:admin` | Name the thing: `prod:k8s:admin`. See [naming](../../concepts/sluis/trust.md#naming). |
| An application role under a tier scope, such as `cluster-prod:roster:operator` | Give the application its own `thing`. |
| A per-environment thing named with `all`, or a role on `all` expected to reach an environment | Name the environment. |
| A wildcard outside a `groups` key | Write one concrete name. |
| A role wildcard, `S:T:*` or `*:*:*` | State the role. |
