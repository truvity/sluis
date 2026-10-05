# Policy: the vocabulary

The optional `vocabulary` table, in force since v1.32.0: which scopes and things exist, each thing's role ladder, and
which role implies which. Part of [the policy](policy.md). The grammar of the names is [taxonomy.md](taxonomy.md); to
write one, [how-to/declare-a-vocabulary.md](../how-to/declare-a-vocabulary.md).

```yaml
vocabulary:
  scopes:
    kernel: { sensitive: true }
    prod:   { sensitive: true }
    devel: {}
    stage: {}
    all:   {}          # only for a thing that exists once per installation
  things:
    k8s:
      scopes: [kernel, devel, stage, prod]
      roles: { viewer: [], operator: [viewer], admin: [operator] }
    argocd:
      scopes: [kernel, devel, stage, prod]
      roles: { viewer: [], deployer: [viewer], operator: [viewer], admin: [deployer, operator] }
    ssh:
      scopes: [kernel, devel, stage, prod]
      roles:
        admin: []                    # list form: valid on every scope ssh declares
        user: { scopes: [devel] }    # object form: valid on devel alone
```

| Key | Holds |
|---|---|
| `vocabulary.scopes.<name>` | `sensitive` (optional, default false) |
| `vocabulary.things.<name>` | `scopes` (the declared scopes this thing exists in) and `roles` (role name to `policy.RoleSpec`: a list of implied roles, or an object with `implies` and/or `scopes`) |

## Opt-in, and strict once opted into

No `vocabulary` table means nothing here applies, and every existing policy loads as it did. Declare one, and the
policy **refuses to load** when any concrete grant name anywhere in the file fails to fit it, naming the name and why:

- the scope is not declared under `vocabulary.scopes`;
- the thing is not declared under `vocabulary.things`;
- the scope is not one of the thing's declared `scopes`;
- the role is not one of the thing's declared `roles`;
- the role does not cover the scope (see [per-role scopes](#per-role-scopes)).

"Anywhere" means every `groups` key, every `claims` key, every `lifetimes` key except `default`, every `requires` of a
client, a resource and `client_documents`, and every GitHub binding. `rung:` and `emp:` names are exempt.

## Roles imply explicitly, and the graph may not cycle

`admin: [operator]` means holding admin also holds operator directly. What operator itself implies is operator's own
entry, so `admin: [deployer, operator]` means admin implies both: branching, not chaining. `implies` may only name
another declared role of the **same** thing, and a cycle is refused, naming the role it closes at. How implied roles
are granted (once, in evaluation, on the same `<scope>:<thing>`) is [taxonomy.md](taxonomy.md#inheritance).

## Per-role scopes

A role's value is either the plain list of implied roles, or an object naming `implies` and/or `scopes`. Both keys are
optional; the list form parses as it always did. A role's own `scopes`, when declared, must be a non-empty subset of its
thing's. Refused at load, naming which: a scope not declared under `vocabulary.scopes` at all, a scope the thing does
not have, or an explicit empty list.

A concrete grant the role does not cover is refused, distinctly from a scope the *thing* lacks: `kernel:ssh:user` gives
`role "user" of thing "ssh" is valid only on scopes [devel]`. A mapping wildcard skips instead of refusing:
`*:ssh:user` expands to `devel:ssh:user` alone.

**Inheritance must not lose scope coverage.** An `implies` edge is refused when its target role does not cover every
scope its source does: `admin: [user]` with `admin` valid everywhere and `user` on `devel` alone. See
[ADR 0012](../decisions/0012-per-role-scopes-in-the-vocabulary.md).

## Mapping wildcards

`*` in the scope and/or thing position of a `groups` key (`*:k8s:admin`, `devel:*:viewer`) needs a declared vocabulary
and is refused without one. A role wildcard (`S:T:*`) and `*:*:*` are always refused. The grammar is
[taxonomy.md](taxonomy.md#mapping-wildcards); a wildcard key is expanded and unioned with a caller's concrete groups in
evaluation ([policy-groups.md](policy-groups.md#groups-to-token-by-deep-merge)).

**A wildcard that expands to no group is refused**, because a key nobody is ever in is a grant that never took effect:

- a concrete, `sensitive` scope: `"prod:*:viewer": scope "prod" is sensitive and is never reached by a wildcard; name
  the concrete groups (e.g. "prod:k8s:viewer") instead`;
- any other empty expansion: `"<key>" expands to no group: <why>`. The reasons: no declared thing has the role
  (`devel:*:admins`), no thing with the role declares the scope, no such thing's role allows the scope
  (`kernel:*:user`), or every scope a swept-in thing declares is sensitive (`*:k8s:viewer`).

## Explainability

`policy.Result`'s `Held` carries, per held group, `Key` (the `groups` key that matched directly: the group's own name
for an ordinary key, the wildcard for a wildcard's) and `Implies` (the concrete group whose role, one hop at a time,
implied this one; empty when held directly). A group held for more than one reason has one entry per reason. It is data
for a console page; nothing renders it yet.
