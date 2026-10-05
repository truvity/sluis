# Exports and the export port

The policy document's `exports` copy the secrets the console keeps (they are in the Secrets port, and no Kubernetes
Secret holds them) into OpenBao, where the programs that act as an App and cannot ask the service read them, and where
the recovery bundles are kept ([ADR 0034](../decisions/0034-exports-go-to-openbao-directly.md)). The service document's
`ports.export` says which OpenBao and how to log in. It needs a `ports.adapter` other than `legacy`: on `legacy` the
Secrets still exist and the chart's `push` values (deprecated) copy them. Source: `internal/exportspec`,
`internal/exports`, `schemas/config/policy.schema.json`.

## The service document: `ports.export`

| Key | Default | Meaning |
|---|---|---|
| `ports.export.adapter` | unset: nothing is copied out | the adapter behind the Export port ([ports](ports.md#export)): `openbao` writes to a KV version 2 mount of an OpenBao; `memory` keeps the copies in the process, for a test. The policy document's `exports` need one |
| `ports.export.openbao.address` | **required with `openbao`** | the OpenBao server, `https://openbao.example`, with no path or credentials. Nothing is contacted at start |
| `ports.export.openbao.caFile` / `.mount` / `.namespace` | system authorities / `kv` / unset | a PEM bundle for the server's certificate in place of the system's; the KV version 2 mount; the OpenBao namespace an export that names none is written to |
| `ports.export.openbao.auth.method` | **required with `openbao`** | `kubernetes` (the Kubernetes auth method, with the pod's ServiceAccount token) or `jwt` (the JWT/OIDC method, with a token read from `tokenFile`). Both log in with `POST auth/<mount>/login {role, jwt}`, inside each namespace written to |
| `ports.export.openbao.auth.mount` / `.role` / `.tokenFile` | the method's name / **required** / the pod's ServiceAccount token for `kubernetes`, **required** for `jwt` | the auth mount path in each namespace; the role the login asks for; and where the JWT is read from, afresh on every login (a projected ServiceAccount token, or on AWS the web identity token of outbound federation) |

## The policy document: `exports[]`

| `source` | Fields | Copies | Written as | Mode |
|---|---|---|---|---|
| `slack-app` | `app` | a catalogue Slack App's bot token, once it is installed | `bot_token` | patch |
| `github-app` | `app` | a catalogue GitHub App, once it is installed | `app_id`, `installation_id`, `private_key` | patch |
| `runner-app` | `tier`, `org` | a runner App, once it is installed | `github-app-id`, `github-installation-id`, `github-private-key` | patch |
| `bundle` | `bundle` | `workspace-credentials`, `github-apps`, `github-links`, `github-runner-apps`, `github-catalogue-apps`, `slack-credentials` or `slack-records`, whole | one JSON document per entry, as the Secret of that name held them | replace |

`path` is the key under the KV mount and `namespace` the OpenBao namespace (an
estate may send one tier's runner App to one namespace and the rest to another). `properties`
maps a property of the App to the name it is written as and writes only those.
`interval` (default `1h`, at least `1m`) is how often the copy is made again with
nothing changed. `name` (default `<source>.<what>`, for instance
`slack-app.alerts`) identifies the export in the log, the metrics and its lease.

## An example

The blocks that reproduce, on the kernel cluster, the External Secrets `PushSecret`s an estate ran before
(`ports.adapter: dynamodb` with a secrets adapter, the `slackApps` and `apps.github.runnerTiers` declared as usual),
first in the service document, then in the policy document:

```yaml
# the service document
ports:
  export:
    adapter: openbao
    openbao:
      address: https://openbao.kernel.example
      caFile: /var/run/access-issuer/openbao-ca/ca.pem   # exports.openbao.caBundle
      mount: kv
      namespace: kernel
      auth:
        method: jwt                      # or kubernetes
        mount: jwt-kernel
        role: sluis-writer
        tokenFile: /var/run/openbao/token                # exports.openbao.token.audience
```

```yaml
# the policy document
exports:
  - {source: slack-app, app: alerts, path: slack-apps/alerts}
  - {source: slack-app, app: alerts-trustform, path: slack-apps/alerts-trustform}
  - {source: slack-app, app: deadman, path: slack-apps/deadman}
  - {source: runner-app, tier: preview, org: trust-form, namespace: devel, path: arc/trustform}
  - {source: runner-app, tier: preview, org: truvity, namespace: devel, path: arc/truvity}
  - {source: runner-app, tier: stable, org: trust-form, path: arc/trustform}
  - {source: runner-app, tier: stable, org: truvity, path: arc/truvity}
  - {source: bundle, bundle: workspace-credentials, path: sluis-backup/workspace-credentials}
  - {source: bundle, bundle: github-apps, path: sluis-backup/github-apps}
  - {source: bundle, bundle: github-links, path: sluis-backup/github-links}
  - {source: bundle, bundle: github-runner-apps, path: sluis-backup/github-runner-apps}
  - {source: bundle, bundle: github-catalogue-apps, path: sluis-backup/github-catalogue-apps}
  - {source: bundle, bundle: slack-credentials, path: slack-state/credentials}
  - {source: bundle, bundle: slack-records, path: slack-state/records}
```

## What the service does, and does not do

- **A copy, asynchronous, never a dependency.** An export runs out of band after
  the State write that changed its source, so sign-in, a tick and a console action
  neither wait for it nor learn of its failure. An OpenBao outage changes nothing
  live: the copy is stale until the next attempt, which is retried after 5 seconds,
  doubling to 5 minutes. The service starts with OpenBao down.
- **When.** Every export is made once at start, again within seconds of a change to
  its source (a watch of the State, at most once in 30 seconds per export), and again
  every `interval`. Where the State cannot be watched, only the interval remains.
- **Exactly one writer.** Each export runs under a lease on the State, so replicas
  divide the exports; an identical write changes nothing, so a State that is not
  shared costs nothing but a read.
- **Nothing to copy is not written.** An App created and not installed, and an empty
  bundle, are `skipped`: the key is not touched and a good copy is never emptied.
- **Never deleted.** Removing an export, or an App, leaves the copy where it is.
- **KV version 2 semantics.** `replace` is a `POST` of the whole key; `patch` is a
  `PATCH` with a JSON merge patch (the other properties of the key stay), and a
  `POST` when the key is absent. Both read the key first and write nothing when it
  already holds the data, so the reconcile makes no new KV version. External Secrets'
  vault provider does the same read-merge-write for a PushSecret with a `property`
  (it reads the key, sets the property and writes the key back), and a `POST` of the
  whole data for one without: assumed from its documented behaviour, and the reason a
  patch is the right mode for `runner-app`, whose key other properties may share.
- **No Kubernetes RBAC.** The service reads State and writes OpenBao; it holds no
  permission on Secrets or ConfigMaps for this, and the chart's Roles are unchanged.
  The `kubernetes` auth method has OpenBao itself review the pod's token.
- **Checked with the policy.** An export of an App the catalogues do not declare, or
  of a runner tier `apps.github.runnerTiers` does not list, is refused where the
  policy document is loaded, not at the first copy.
- **OpenBao policy.** The role needs `read`, `create`, `update` and `patch` on
  `kv/data/<prefix>/*` in each namespace it writes to. Never `list` or `delete`.

The metrics, the two alerts and the dashboard row are in [telemetry](../operations/telemetry.md#the-exports). The
chart's `exports.openbao.*` values mount the CA and the token: [chart values](chart-values.md).
