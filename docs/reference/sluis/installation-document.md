# The installation document

One installation, written once, from which both documents are rendered ([ADR 0038](../../decisions/0038-estates-render-through-sluis.md)): `apiVersion: sluis.truvity.github.io/installation/v1`, held to `schemas/config/installation.schema.json`. It holds no secret.

`sluisctl render --installation <file> --out <dir>` writes `sluis.yaml` and `policy.yaml` ([sluisctl](sluisctl.md#render-an-installation-in-the-two-documents-out)). `--check` exits 1 when the files differ. The Pulumi library takes it as `LambdaArgs.Installation`. Go programs call `config.Render` from `github.com/truvity/sluis/config`.

It is a superset of the service and policy documents: each section sits under its document's key with that document's schema.

| Key | What it is |
|---|---|
| `instance`, `shape` | the installation's name (its SSM root is `/sluis/<instance>`) and where it runs: `lambda`, `kubernetes` or `server`. Required. |
| `preset`, `release`, `cluster`, `policyFile` | the service document's own keys. `preset` follows the shape when unset: `aws-hybrid` for `lambda`, `k8s-aws` for `kubernetes` with `aws`. The other defaults, `k8s-openbao` (with `openbao`), `k8s-minimal` and `server`, are unavailable: loading one fails naming the missing adapters ([adapters](adapters.md#presets)). `policyFile` follows the shape too (`lambda`: `/opt/sluis/policy.yaml`, in the layer; `kubernetes`: `/var/run/access-issuer/policy/policy.yaml`, where the chart mounts it; `server`: `/etc/sluis/policy.yaml`). |
| `issuer` | `url` (required), `consoleURL` (default `<url>/console`), `rootURL`, `secureCookies`, `groupsScoping`. |
| `log`, `lifetimes`, `freshness`, `secrets`, `recovery`, `login`, `console`, `oauthClient`, `valkey`, `directory`, `audit`, `signingKey` | the service document's, unchanged. For a KMS signer the state secret is named `issuer/state-secret` when left out. |
| `aws` | `account`, `region` (required for `lambda`), `functionName`, and the resources `table`, `bucket`, `auditQueueURL`, each becoming the setting of the adapter that uses it (`state: dynamodb`, `blobs: s3`, `audit: sqs`). |
| `openbao` | the `openbao` secrets adapter's settings (`address`, `caFile`, `mount`, `namespace`, `root`, `auth`); it chooses that adapter for `secrets`. |
| `adapters` | names single concerns, over what the preset and the resources above give. |
| `exchange` | `audience` (the service document's) and the policy document's `clusters`, `aws` and `github.owners`. |
| `apps` | the policy document's, unchanged. |
| `controllers` | `github` and `slack`, each present when the controller is on: its own keys go to the service document's `controllers`, `enabledOrgs` and `enabledWorkspaces` to the policy document's. |
| `access` | the access model's tables (`groups`, `clients`, `resources`, `github`, `slack`, ...), the policy document's, unchanged. |

For shape `lambda` the renderer writes what the library owns (`secrets` as `ssm` with the instance's root and region, `recovery.passwordSecret`, the `invoke` trigger's function) and refuses a different value. A named adapter replaces what a resource stands for. The output is deterministic and held to the service's loader. Start checks adapter platform answers and OpenBao reachability.
