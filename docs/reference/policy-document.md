# The policy document

The service loads one policy document, from the file the service document's `policy.file` names. There is one layer: the
console is read-only, so nothing it does can add to what is declared here. It says what the installation decides. Its
tables are the access model's ([policy](policy.md): `vocabulary`, `groups`, `claims`, `lifetimes`, `resources`,
`clients`, `github`, `slack`, `people`, ...), unchanged; beside them are four sections that say whom the exchange
trusts, which Apps an operator may make, what the controllers may change and what is exported. Held to
`schemas/config/policy.schema.json`; `apiVersion: sluis.truvity.github.io/policy/v2`.

| Section | Holds | Was |
|---|---|---|
| `exchange.clusters[]` | `{name, issuer, jwksUri}` per federated cluster | `exchange.clustersFile` |
| `exchange.aws` | `audience`, `maxAge` and `accounts[]` (`{account, name, issuer, jwksUri, orgId, algs}`) | `exchange.awsFile` |
| `exchange.github.owners[]` | the organisations whose CI tokens are verified. None verifies none | `github.owners` |
| `apps.github.runnerTiers[]` | the tiers an operator may create a runner App for | `github.runnerTiers` |
| `apps.github.catalogue[]` | the GitHub App catalogue ([connect/github-apps-catalogue.md](../how-to/connect/github-apps-catalogue.md)) | `github.catalogueFile`, the GitHub controller's `catalogueFile` |
| `apps.slack.catalogue[]` | the Slack App catalogue ([connect/slack-apps-catalogue.md](../how-to/connect/slack-apps-catalogue.md)) | `slack.catalogueFile` |
| `controllers.github.enabledOrgs[]` | the organisations the GitHub controller changes; every other bound organisation is a dry run | the controller's `enabledOrgs` |
| `controllers.slack.enabledWorkspaces[]` | the workspaces the Slack controller changes | the controller's `enabledWorkspaces` |
| `exports[]` | the secrets copied out of the service ([exports](exports.md)) | `exports` of `serve` |

There is no secret in any of them: every row is a name and a URL.

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

Whatever the service checked at start across these belongs to the document, and runs wherever it is loaded (the binary,
`sluisctl policy render`, the Pulumi library before it publishes one): a catalogue grant naming an undeclared group, a
Slack App for an undeclared workspace, an enabled organisation or workspace the policy does not bind, an export of an
undeclared App, two clusters for one issuer.

## Rendering

A process loads exactly one document. `sluisctl policy render <file or directory> [-o <file>]` writes it from the layers
a deployment declares: a policy file of v1 (`version: 1`), an access document (`access:` and `overlay:`, reshaped into
tables) or a policy document fragment, alone or a directory of them merged in name order
([sluisctl](sluisctl.md#policy-render-the-one-policy-document)). Tables merge by key and a key declared twice is an error
naming the file; of the sections, a list concatenates and a list of names unions. The result is held to every check
above, and is the same bytes for the same layers.

Name the result in the service document's `policy.file`. On Kubernetes the chart takes the policy in `policy:` in values
and the Apps' catalogues, or the finished document in `documents.policy` ([chart values](chart-values.md)); on AWS Lambda
it is the policy of the configuration layer. An installation renders both documents at once from
[the installation document](installation-document.md).
