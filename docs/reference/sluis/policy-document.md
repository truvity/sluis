# The policy document

The policy document is the one file a process loads, named by the service document's `policy.file`. It holds the tables of [the policy](policy.md) plus three sections, held to `schemas/config/policy.schema.json`, `apiVersion: sluis.truvity.github.io/policy/v2`.

| Section | Holds |
|---|---|
| `exchange.clusters[]` | `{name, issuer, jwksUri}` per federated cluster |
| `exchange.aws` | `audience`, `maxAge` and `accounts[]` (`{account, name, issuer, jwksUri, orgId, algs}`) |
| `exchange.github.owners[]` | organisations whose CI tokens are verified; none verifies none |
| `apps.github.runnerTiers[]` | tiers an operator may create a runner App for |
| `apps.github.catalogue[]` | the GitHub App catalogue ([GitHub Apps catalogue](../../guides/sluis/connect/github-apps-catalogue.md)) |
| `apps.slack.catalogue[]` | the Slack App catalogue ([Slack Apps catalogue](../../guides/sluis/connect/slack-apps-catalogue.md)) |
| `controllers.github.enabledOrgs[]` | organisations the GitHub controller changes; other bound organisations are a dry run |
| `controllers.slack.enabledWorkspaces[]` | workspaces the Slack controller changes |

No section holds a secret: every row is a name and a URL.

```yaml
apiVersion: sluis.truvity.github.io/policy/v2
groups:
  all:access-roster:operator: { members: [platform-admins@example.com] }
  all:access-roster:viewer:   { members: [all@example.com] }
lifetimes: { default: 12h }
github:
  example-org: { members: [all:access-roster:viewer] }
exchange:
  clusters:
    - {name: devel, issuer: "https://kubernetes.default.svc", jwksUri: "https://devel.example/openid/v1/jwks"}
  aws:
    audience: sluis-exchange
    accounts:
      - {account: "111122223333", name: apps, issuer: "https://example-id.tokens.sts.global.api.aws"}
  github: {owners: [example-org]}
apps:
  github: {runnerTiers: [preview, stable]}
controllers:
  github: {enabledOrgs: [example-org]}
```

The document checks run wherever it is loaded: the binary, `sluisctl policy render` and the Pulumi library.

| Refused |
|---|
| a catalogue grant naming an undeclared group |
| a Slack App for an undeclared workspace |
| an enabled organisation or workspace the policy does not bind |
| two clusters for one issuer |

## Rendering

`sluisctl policy render <file or directory> [-o <file>]` writes one document ([sluisctl](sluisctl.md#policy-render-the-one-policy-document)). The same layers give the same bytes.

| Input layer | Form |
|---|---|
| policy file | `version: 1` |
| access document | `access:` and `overlay:`, reshaped into tables |
| policy document fragment | any section |

A directory merges in name order. Tables merge by key and a key declared twice is an error naming the file. A list of sections concatenates; a list of names unions.

| Target | Where the document goes |
|---|---|
| any | `policy.file` in the service document |
| Kubernetes | `policy:` in values, or the finished document in `documents.policy` ([chart values](chart-values.md)) |
| AWS Lambda | the policy of the configuration layer |

[The installation document](installation-document.md) renders both documents at once.
