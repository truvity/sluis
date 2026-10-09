# Groups in a token

Which of the groups a caller holds a token carries, and why. The keys are in [policy-clients.md](../../reference/sluis/policy-clients.md#groups-override);
to act on it, [read the report](../../guides/sluis/read-the-groups-scoping-report.md) and then [turn enforce on](../../guides/sluis/turn-enforce-on.md).

## The problem

Without scoping, every token's `groups` claim carries every internal group the caller holds, whatever the audience.
`groups` is the whole of the authorization, so a token that carries an installation's whole naming structure into one
narrow client's cookie is a cost worth ending. [ADR 0006](../../decisions/0006-groups-claim-scoped-per-audience.md) is the
decision; [ADR 0010](../../decisions/0010-a-declared-vocabulary.md) sharpened the rule once a declared vocabulary existed
to name a thing precisely. Scoping does not change what `requires` admits.

## The rule

A token carries every group the caller holds (after [inheritance](../../reference/sluis/taxonomy.md#inheritance) has expanded the
set) whose `<scope>:<thing>` pair appears among the `<scope>:<thing>` pairs of its audience's `requires`, in **any
role**. The audience is the client, or the [resource](policy.md#why-a-resource-is-not-a-client) a request named; for a
token exchange, the target the exchange was **granted**, never the client presenting it.

```yaml
clients:
  grafana: { kind: confidential, secret: grafana-oidc, requires: [devel:grafana:viewer] }
```

A caller holding `devel:grafana:editor`, `devel:grafana:viewer`, `devel:k8s:admin` and `prod:shop:deployer` gets a Grafana
token carrying `[devel:grafana:editor, devel:grafana:viewer]`. Both share Grafana's own pair, `devel:grafana`, so both
survive even though only `viewer` is named: the pair is checked, never the role. The other two are dropped:
`devel:k8s:admin` is a different thing, and `prod:shop:deployer` a different scope of a thing this audience never named.

**The override.** A client row, a resource row or the `client_documents` block may set `groups: all` (carry everything)
or `groups: [thing, ...]` (additionally carry every held group of each thing, in any scope). With a declared
vocabulary each name must be a declared thing, because a typo in an override is exactly the mistake a vocabulary exists
to catch.

**A self-described client is gated and scoped.** It has no row of its own, but every one shares the one gate
`client_documents.requires`, and its pairs are read from there. A `client_documents.groups` override widens it for every
document client at once.

**An audience that matches no gate** (not a declared client, not a declared resource, not a permitted document URL, and
every audience when no document client is admitted) keeps nothing by pair matching, because there is no `requires` to
read. Under `report`, every such audience's tokens log "would drop everything" until it is given a row, an allow-listed
origin or an override.

**`rung:` and `emp:` names** are not grants ([taxonomy](../../reference/sluis/taxonomy.md)) and have no `<scope>:<thing>` pair,
so pair matching never keeps them. An override keeps one either by the full two-segment name (`groups: [rung:sre]`) or
by its family, `rung` or `emp`, which keeps every held name of it. The family is what an installation with more than a
handful of people reaches for: a Kubernetes audience binds each person's namespace to their `emp:<slug>`, and no outright
entry could name every slug in advance.

```yaml
clients:
  k8s:devel:
    kind: public
    requires: [devel:k8s:viewer]
    groups: [emp]   # every emp:<slug> held, for the cluster's own binding
```

A `rung:` group's lifetime is read off the full held list before scoping runs, so a `rung:` group missing from a token
under `enforce` still shortens that token's life. Scoping narrows what a token **says**, never what sluis computes
from what a caller holds.

## `off`, `report`, `enforce`

`groupsScoping`, a key of the service's `config` ([configuration.md](../../reference/sluis/configuration.md)), is one of three.

| Mode | Does |
|---|---|
| `off` | computes and logs nothing |
| `report` (the default since v1.32.0) | computes the rule for every minted token and logs one line at INFO when it would have dropped something: audience, client, subject and the dropped names. The token is minted exactly as before |
| `enforce` | narrows the claim to what the rule keeps, and logs the same finding at DEBUG, because a dropped group is the steady state rather than news on every token |

Turning enforce on is opt-in and per installation. An installation runs report first, reads what it logs, and adds a
`groups:` override to any client or resource the log names, before setting `enforce`.

## What enforce narrows

Every place a token or `/userinfo` writes `groups` narrows: the ID token, the access token (authorization code, refresh
and token exchange alike), a token the console mints for itself (`Storage.MintFor`), and `/userinfo`. Nothing else
changes: `requires` still gates entry against the **full** evaluated set.

`/userinfo` (`SetUserinfoFromToken`) answers with the same `groups` a token's owner holds, keyed by the presented access
token. Narrowing only the token and leaving `/userinfo` unscoped would enforce nothing: a relying party that wanted the
fuller list could call it and read it there, which is the leak this design exists to close. So both narrow by the same
audience, read off the access token's stored record.

A relying party that used to read a group beyond what its own `requires` names, and stops seeing it under enforce, needs
a `groups:` override. The fix is never in code; [turn enforce on](../../guides/sluis/turn-enforce-on.md) shows how to find the
group.
