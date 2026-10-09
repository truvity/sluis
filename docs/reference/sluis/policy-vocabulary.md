# Policy: the vocabulary

The optional `vocabulary` table declares scopes, things, role ladders and implied roles, for [the policy](policy.md). Grammar: [taxonomy.md](taxonomy.md). To write one, [declare a vocabulary](../../guides/sluis/declare-a-vocabulary.md).

```yaml
vocabulary:
  scopes:
    core: { sensitive: true }
    prod:   { sensitive: true }
    devel: {}
    stage: {}
    all:   {}          # only for a thing that exists once per installation
  things:
    k8s:
      scopes: [core, devel, stage, prod]
      roles: { viewer: [], operator: [viewer], admin: [operator] }
    argocd:
      scopes: [core, devel, stage, prod]
      roles: { viewer: [], deployer: [viewer], operator: [viewer], admin: [deployer, operator] }
    ssh:
      scopes: [core, devel, stage, prod]
      roles:
        admin: []                    # list form: valid on every scope ssh declares
        user: { scopes: [devel] }    # object form: valid on devel alone
```

| Key | Holds |
|---|---|
| `vocabulary.scopes.<name>` | `sensitive` (optional, default false) |
| `vocabulary.things.<name>` | `scopes` (declared scopes the thing exists in) and `roles` (role name to `policy.RoleSpec`: a list of implied roles, or an object with `implies` and/or `scopes`) |

## Strict once declared

Without a `vocabulary` table nothing here applies. With one, a concrete grant name that does not fit is refused at load, with the reason. `rung:` and `emp:` names are exempt.

| Subject | Refused when |
|---|---|
| scope | not under `vocabulary.scopes` |
| thing | not under `vocabulary.things` |
| scope | not one of the thing's `scopes` |
| role | not one of the thing's `roles` |
| role | does not cover the scope ([per-role scopes](#per-role-scopes)) |

The check covers every `groups` key, every `claims` key, every `lifetimes` key except `default`, every `requires` of a client, a resource and `client_documents`, and every GitHub binding.

## Implied roles

| Rule | Behaviour |
|---|---|
| `admin: [operator]` | admin holds operator directly; operator's own entry says what it implies |
| `admin: [deployer, operator]` | admin implies both: branching, not chaining |
| target | another declared role of the same thing |
| cycle | refused, naming the role it closes at |

Granting of implied roles: [taxonomy.md](taxonomy.md#inheritance).

## Per-role scopes

A role's value is a list of implied roles, or an object with `implies` and/or `scopes`; both are optional. A role's `scopes` is a non-empty subset of its thing's.

| Refused at load | Message or example |
|---|---|
| a role `scopes` entry | not under `vocabulary.scopes`, or not a scope of the thing |
| a role `scopes` list | empty |
| a concrete grant the role does not cover | `core:ssh:user` gives `role "user" of thing "ssh" is valid only on scopes [devel]` |
| an `implies` edge whose target covers fewer scopes than its source | `admin: [user]`, `admin` everywhere, `user` on `devel` alone |

A mapping wildcard skips instead of refusing: `*:ssh:user` expands to `devel:ssh:user` alone. Decided in [ADR 0012](../../decisions/0012-per-role-scopes-in-the-vocabulary.md).

## Mapping wildcards

`*` in the scope or thing position of a `groups` key (`*:k8s:admin`, `devel:*:viewer`) needs a declared vocabulary. A role wildcard `S:T:*` and `*:*:*` are always refused. Grammar: [taxonomy.md](taxonomy.md#mapping-wildcards). Evaluation unions the expansion with concrete groups ([policy-groups.md](policy-groups.md#groups-to-token-by-deep-merge)).

A wildcard that expands to no group is refused.

| Case | Message or example |
|---|---|
| a concrete `sensitive` scope | `"prod:*:viewer": scope "prod" is sensitive and is never reached by a wildcard; name the concrete groups (e.g. "prod:k8s:viewer") instead` |
| no declared thing has the role | `devel:*:admins` |
| no thing with the role declares the scope | `"<key>" expands to no group: <why>` |
| no such thing's role allows the scope | `core:*:user` |
| every swept-in scope is sensitive | `*:k8s:viewer` |

## Explainability

`policy.Result.Held` carries one entry per reason a group is held. No page renders it yet.

| Field | Holds |
|---|---|
| `Key` | the `groups` key that matched: the group's own name, or the wildcard |
| `Implies` | the concrete group whose role implied this one, one hop; empty when held directly |
