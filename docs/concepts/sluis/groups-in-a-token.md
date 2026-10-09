# Which groups does a token carry?

A token carries the groups its audience names, not every group the caller holds. The keys are in [policy clients](../../reference/sluis/policy-clients.md#groups-override). To act on it, [read the report](../../guides/sluis/read-the-groups-scoping-report.md) and then [turn enforce on](../../guides/sluis/turn-enforce-on.md).

Scoping never changes what `requires` admits.

## The rule

A token carries each held group whose `<scope>:<thing>` pair appears among the pairs of its audience's `requires`, in any role. [Inheritance](../../reference/sluis/taxonomy.md#inheritance) expands the set first.

The audience is the client, or the [resource](policy.md#why-a-resource-is-not-a-client) a request named. For a token exchange it is the target the exchange was granted, never the client presenting it.

```yaml
clients:
  grafana: { kind: confidential, secret: grafana-oidc, requires: [devel:grafana:viewer] }
```

A caller holding `devel:grafana:editor`, `devel:grafana:viewer`, `devel:k8s:admin` and `prod:shop:deployer` gets a Grafana token carrying `[devel:grafana:editor, devel:grafana:viewer]`. Both share Grafana's pair, `devel:grafana`. The service checks the pair, never the role. The other two groups name other things.

## Overrides

A client row, a resource row or the `client_documents` block may set `groups: all` to carry everything. It may set `groups: [thing, ...]` to carry every held group of each thing, in any scope. With a declared vocabulary, each name must be a declared thing.

A self-described client has no row of its own. It shares the gate `client_documents.requires`, and a `client_documents.groups` override widens it for every document client.

An audience that matches no gate keeps nothing, because there is no `requires` to read. That covers an undeclared client, an undeclared resource, an unpermitted document URL, and every audience when no document client is admitted. Under `report`, its tokens log "would drop everything" until it gets a row, an allow-listed origin or an override.

## `rung:` and `emp:` names

`rung:` and `emp:` names are not grants ([taxonomy](../../reference/sluis/taxonomy.md)) and have no `<scope>:<thing>` pair, so pair matching never keeps them. An override keeps one by its full two-segment name (`groups: [rung:sre]`) or by its family, `rung` or `emp`, which keeps every held name of it.

A Kubernetes audience that binds each person's namespace to their `emp:<slug>` needs the family form.

```yaml
clients:
  k8s:devel:
    kind: public
    requires: [devel:k8s:viewer]
    groups: [emp]   # every emp:<slug> held, for the cluster's own binding
```

The service reads a `rung:` group's lifetime from the full held list before scoping runs. A `rung:` group missing from a token under `enforce` still shortens that token's life.

## `off`, `report` and `enforce`

`groupsScoping`, a key of the service's `config` ([configuration](../../reference/sluis/configuration.md)), takes one of three values.

| Mode | Does |
|---|---|
| `off` | computes and logs nothing |
| `report` (the default) | computes the rule for every minted token and logs one INFO line when it would drop something: audience, client, subject and the dropped names. The token is minted unchanged |
| `enforce` | narrows the claim to what the rule keeps and logs the same finding at DEBUG |

Enforce is opt-in per installation. Run `report` first, read the log, and add a `groups:` override to every client or resource it names before you set `enforce`.

## What enforce narrows

Enforce narrows every place that writes `groups`. That covers the ID token, the access token from code, refresh and exchange, a token the console mints for itself (`Storage.MintFor`), and `/userinfo`.

`/userinfo` (`SetUserinfoFromToken`) is keyed by the presented access token and scopes by the audience stored on that token's record. Narrowing the token alone would leave `/userinfo` as a way to read the fuller list.

`requires` still gates entry against the full evaluated set. A relying party that read a group beyond its own `requires` needs a `groups:` override under enforce. The fix is never in code: see [turn enforce on](../../guides/sluis/turn-enforce-on.md).

## Decided in

- [ADR 0006: Groups claim scoped per audience](../../decisions/0006-groups-claim-scoped-per-audience.md)
- [ADR 0010: A declared vocabulary](../../decisions/0010-a-declared-vocabulary.md)
