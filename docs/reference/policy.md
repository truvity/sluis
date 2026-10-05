# The policy

The policy is the access model of one installation: who is in which internal group, what a group adds to a token, how
long a token lives, and which client may be issued one. Everything that shapes a token is derivable from this file by
reading it. This page is the hub: the shape of the file and one short section per key, with the details in the pages it
links. Why it is shaped this way is [explanation/policy.md](../explanation/policy.md).

The access model's tables are written in a policy file of `version: 1`. They are also the tables of the **policy
document** (`apiVersion: sluis.truvity.github.io/policy/v2`) that a process loads, which carries four more sections
beside them (`exchange`, `apps`, `controllers`, `exports`; see [policy-document.md](policy-document.md)).
`sluisctl policy render` writes the document from the layers a deployment declares.

## The tables

```yaml
version: 1

groups:                        # internal groups, the vocabulary, named <scope>:<thing>:<role>
  prod:k8s:admin:
    members: [role-sre@a.example, role-sre@b.example]   # directory groups, any workspace
  prod:k8s:auditor:
    members: [role-security@a.example]
  prod:shop:deployer:
    members: [team-shop@a.example]
  rung:sre:                                             # two segments: a lifetime carrier, not a grant
    members: [role-sre@a.example, role-sre@b.example]
  ci:platform:deployer:
    matchers:                                           # matched, not listed
      - github: { repository: acme/platform, ref: refs/heads/master }
  all:access-roster:viewer:
    matchers: [{ email_domain: a.example }]              # the escape hatch
  all:access-roster:operator:
    members: [directory-admins@a.example]

claims:                        # what a group adds beyond its own name: sparse, usually empty
  prod:k8s:auditor: { tailnet: { tiers: [vpc] } }

lifetimes:                     # how long: default plus the rungs
  default: 4h
  rung:sre: 8h
  ci:platform:deployer: 1h

clients:                       # who may be issued a token for what; the id is the audience,
                               # unless the request names a resource below
  k8s:prod:          { kind: public,       requires: [prod:k8s:admin, prod:k8s:auditor] }
  aws:1111:power:    { kind: exchange,     requires: [prod:k8s:admin] }
  argocd:            { kind: confidential, secret: argocd-oidc-client, redirects: [https://argocd.example/auth/callback], requires: [prod:k8s:admin], ttl_cap: 12h, display_name: Argo CD }
  local-dev:         { kind: public,       redirects: [http://localhost:8000/callback], requires: [prod:shop:deployer] }

resources:                     # what a token may be minted FOR, when that is not the client asking
  https://mcp.example/:        { requires: [prod:k8s:admin], ttl_cap: 5m, display_name: Telemetry }

client_documents:              # clients that describe themselves; empty means the mechanism is off
  origins:  [clients.example]
  requires: [prod:shop:deployer]
  ttl_cap:  5m
```

<!-- generated: policy-keys -->

Source: `schemas/config/policy.schema.json` (the policy document) and `policy.Policy` (the v1 tables).

| Key | Keyed by | Holds | Details |
|---|---|---|---|
| `version` / `apiVersion` | | `version: 1` for a policy file; `apiVersion: sluis.truvity.github.io/policy/v2` for the document | [configuration.md](configuration.md#documents-and-apiversion) |
| `vocabulary` | | `scopes`, `things`: the names a grant may use. Optional | [policy-vocabulary.md](policy-vocabulary.md) |
| `groups` | internal group name | `members` (directory groups) or `matchers`. A group with neither is one nobody is in yet | [policy-groups.md](policy-groups.md) |
| `claims` | internal group name | a claim fragment merged into the token | [policy-groups.md](policy-groups.md#groups-to-token-by-deep-merge) |
| `lifetimes` | internal group name, or `default` | a duration | [policy-groups.md](policy-groups.md#groups-to-token-by-deep-merge) |
| `clients` | client id | `kind`, `secret`, `redirects`, `signed_out`, `requires`, `ttl_cap`, `sign_in_exchange`, `display_name`, `description`, `backchannel_logout_uri`, `groups`, `groups_delimiter`, `signing_alg` | [policy-clients.md](policy-clients.md) |
| `resources` | resource indicator (an absolute URI) | `requires`, `ttl_cap`, `read_only`, `absolute_cap`, `display_name`, `description`, `groups`, `groups_delimiter`, `signing_alg` | [policy-clients.md](policy-clients.md#resources) |
| `client_documents` | | `origins`, `requires`, `ttl_cap`, `groups` | [policy-clients.md](policy-clients.md#clients-that-describe-themselves) |
| `github` | organisation login | the organisation's `members`, `ignore`, and `teams` by slug, each with `members` and `maintainers` | [policy-bindings.md](policy-bindings.md#github-teams) |
| `people` | a name you choose | the addresses that are one person | [policy-bindings.md](policy-bindings.md#people) |
| `slack` | workspace key | the channels bound in it, by name | [policy-bindings.md](policy-bindings.md#slack-channels) |
| `exchange`, `apps`, `controllers`, `exports` | | document sections, not access-model tables | [policy-document.md](policy-document.md) |

<!-- /generated -->

There is no `memberships` table and no console-written layer: the console writes nothing into the policy, and the key is
refused like any other unknown one. A directory group that should feed an internal group is named in that group's
`members`, here, in git.

## Layers

The policy may be one file or a directory of them. In a directory, every `*.yaml` file says `version: 1`. The keyed
tables (`groups`, `claims`, `lifetimes`, `clients`, `resources`, and each organisation's `teams` under `github`) merge by
key, and a key declared in two files is refused. `vocabulary` and `client_documents` are installation-wide: one file
declares each, and a second is refused. `slack` workspaces merge field by field, and a Slack channel comes from one
file. `people` is a keyed table, and a person declared twice is a clash. Under `github`, one organisation's own `members`
come from one file, and its `ignore` entries add up across files. Validation runs once, on the merged policy
([policy-validation.md](policy-validation.md)).

`sluisctl policy render <file or directory>` does the merge and writes the one document a process reads.
A file with an `access` key is an access document, which the loader reshapes into these tables
([policy-bindings.md](policy-bindings.md#the-access-document)).

## Naming

Every grant is named **`<scope>:<thing>:<role>`**, *role, on thing, in scope*. `scope` is an environment, a tenant id,
or `all`; `thing` is what the role is on (a subsystem such as `k8s`, a project such as `shop`, an application such as
`sluis`); `role` is from that thing's own ladder. The two exceptions are not grants and are two segments on purpose:
`rung:<name>` carries a session lifetime, `emp:<slug>` is a person. The loader warns on a name in neither shape. The
grammar, `all`, sensitive scopes and the anti-patterns are [taxonomy.md](taxonomy.md); the reasoning is
[explanation/trust.md](../explanation/trust.md#naming).

## Claims

The token's claims are the fixed identity claims plus the deep merge of the `claims` fragments of every group the caller
holds. A group's name is usually all it adds; `claims` is for the rare relying party that reads something that is not a
group. The merge rules, and the claim table, are in [policy-groups.md](policy-groups.md#groups-to-token-by-deep-merge).

## Vocabulary

An optional table, in force since v1.32.0, that declares which scopes and things exist, each thing's role ladder and
which role implies which. Absent, nothing is checked. Declared, the policy refuses to load when any concrete grant name
fails to fit it. Keys, load refusals, per-role scopes and wildcards: [policy-vocabulary.md](policy-vocabulary.md).
To declare one: [how-to/declare-a-vocabulary.md](../how-to/declare-a-vocabulary.md).

## Resources — what a token is for

A resource is declared under `resources`, a client names it with the `resource` parameter (RFC 8707), and `aud` is the
resource. A caller must satisfy both the client's `requires` and the resource's, and the shorter `ttl_cap` wins.
A resource that only reads may carry `read_only: true` and an `absolute_cap` up to 168h. Keys and refusals:
[policy-clients.md](policy-clients.md#resources). Why: [explanation/policy.md](../explanation/policy.md#why-a-resource-is-not-a-client).

## Groups in a token (scoping)

`groupsScoping` is `off`, `report` or `enforce`. With `enforce`, a token carries the groups the caller holds whose
`<scope>:<thing>` pair appears among the pairs of its audience's `requires`, in any role, plus what a `groups:` override
on a client row, a resource row or `client_documents` adds: `all`, a list of things, or the families `rung` and `emp`.
`report` (the default since v1.32.0) logs what enforce would drop and changes nothing. `requires` and lifetimes still
read the full set. The rule and its reasoning: [explanation/groups-in-a-token.md](../explanation/groups-in-a-token.md).
The `groups:` key: [policy-clients.md](policy-clients.md#groups-override). To act on it:
[read the report](../how-to/read-the-groups-scoping-report.md), then [turn enforce on](../how-to/turn-enforce-on.md).

## Groups delimiter (per audience, opkssh interop)

`groups_delimiter` on a client or resource row rewrites every `:` in that audience's `groups` claim to another string,
after scoping has decided which groups survive. It exists for one relying party, opkssh, whose policy line cannot match
a name containing `:`. Refused at load: empty or `:`, whitespace, `"` or `,`, a letter, digit or `-`, and a delimiter
that would make two of the policy's own declared groups collide. Details:
[policy-clients.md](policy-clients.md#groups_delimiter); the reasoning: [explanation/policy.md](../explanation/policy.md#why-a-groups-delimiter-exists);
the use: [how-to/connect/ssh.md](../how-to/connect/ssh.md).

## Signing algorithm per audience

The service signs with every algorithm it has a key for (RS256, ES256, ES384) and picks one per token from the audience.
A client row or resource row may pin one with `signing_alg`; rows without it get the installation default (ES384 in the
chart). A pin naming an algorithm with no key is refused at start. Details:
[policy-clients.md](policy-clients.md#signing_alg); the reasoning:
[explanation/policy.md](../explanation/policy.md#why-an-audience-can-pin-a-signing-algorithm).

## Clients that describe themselves

`client_documents` admits a client that this installation does not deploy, by an HTTPS `client_id` whose URL serves a
document, from an allow-listed origin only. Off unless `origins` is written. Keys and refusals:
[policy-clients.md](policy-clients.md#clients-that-describe-themselves); the reasoning:
[explanation/policy.md](../explanation/policy.md#why-a-self-described-client-is-proportionate).

## Where the rest lives

| Topic | Page |
|---|---|
| Proofs, matchers (including `aws`), claims, merge rules | [policy-groups.md](policy-groups.md) |
| Clients, resources, delimiter, signing algorithm, self-described clients | [policy-clients.md](policy-clients.md) |
| Vocabulary, per-role scopes, wildcards | [policy-vocabulary.md](policy-vocabulary.md) |
| `github`, `people`, `slack`, the access document | [policy-bindings.md](policy-bindings.md) |
| The service's own two groups, scoping them, who owns an organisation or workspace | [policy-ownership.md](policy-ownership.md) |
| What is refused at load, the unconsumed-group warning | [policy-validation.md](policy-validation.md) |
| Grammar of grant names | [taxonomy.md](taxonomy.md) |
| Why | [explanation/policy.md](../explanation/policy.md) |
| Bind Slack channels, bind GitHub teams, test a policy | [how-to/bind-slack-channels-in-git.md](../how-to/bind-slack-channels-in-git.md), [how-to/bind-github-teams.md](../how-to/bind-github-teams.md), [how-to/test-the-policy.md](../how-to/test-the-policy.md) |
