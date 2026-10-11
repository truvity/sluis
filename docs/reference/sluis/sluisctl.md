# sluisctl

Commands, files and exit codes of `sluisctl`. Wrappers: [sluisctl-wrappers](sluisctl-wrappers.md). Concepts: [sluisctl](../../concepts/sluis/sluisctl.md).

```sh
sluisctl version                            # sluisctl 1.35.0 (also --version, -version)
sluisctl login   --issuer https://access.example
sluisctl whoami                             # who you are, and what it opens
sluisctl setup                              # both of the next two

sluisctl kubeconfig                         # a context per granted cluster
sluisctl aws-config                         # a profile per granted cloud role

kubectl --context staging get nodes          # exec plugin: sluisctl kube-token
aws --profile deployer@111122223333 sts get-caller-identity   # credential_process: sluisctl aws

sluisctl token --audience openbao              # one token for one audience, on stdout
sluisctl github-token --app publisher --repository app --permission contents=read
sluisctl exchange --audience k8s:staging < subject-token

sluisctl bao --address https://openbao.example:8200 kv get -format=env secret/app > .env
sluisctl bao --address https://openbao.example:8200 ssh -mode=ca -role=user ci@build-worker.example
sluisctl bao --address https://openbao.example:8200 write -field=signed_key \
    ssh/sign/user public_key=@key.pub > key-cert.pub

sluisctl psql --address https://openbao.example:8200 -ns staging -- -h db.example -d orders
sluisctl pg   --address https://openbao.example:8200 -ns staging -- pg_dump orders > orders.sql

sluisctl r2 --service-url https://r2-broker.example.com -- credentials --bucket example-bucket --prefix nix/

sluisctl backup status                          # the backup function, with your AWS credentials
sluisctl restore start 20261010T020000Z-3fa9c1 --confirm example --wait

sluisctl render --installation installation.yaml --out rendered/   # the service and policy documents
sluisctl policy render policy/ -o policy.yaml   # the one policy document an installation reads
```

## Commands

| Command | Does | Reference |
|---|---|---|
| `version` | this build's own version (also `--version`, `-version`) | below |
| `login` | sign in at the issuer, once, in a browser | below |
| `whoami` | who you are, and what your groups open | below |
| `setup` | `kubeconfig` and `aws-config` together | below |
| `kubeconfig` | a context per cluster you are granted | [what it writes](#what-it-writes-into-files-that-are-not-its-own) |
| `aws-config` | a profile per cloud role you are granted | [what it writes](#what-it-writes-into-files-that-are-not-its-own) |
| `token` | a token for one audience, on stdout | below |
| `github-token` | a GitHub App installation token, under the catalogue's grants | below |
| `kube-token` | a Kubernetes exec credential (run by `kubectl`) | [caches](#where-things-are-kept) |
| `aws` | an AWS credential-process answer (run by the AWS SDKs) | [caches](#where-things-are-kept) |
| `cloudflare token\|r2` | a Cloudflare API token, or R2 credentials as an AWS credential-process answer, minted for you | [below](#cloudflare-token-and-cloudflare-r2) |
| `exchange` | the raw exchange: a token in, a token for an audience out | below |
| `bao`, `r2` (deprecated), `psql`, `pg`, `ssh known-hosts` | authenticate, then run another program | [wrappers](sluisctl-wrappers.md) |
| `clients rotate\|show\|purge` | look after a generated client's secret | [clients](#clients-rotate-show-purge) |
| `backup run\|status\|list`, `restore preview\|start\|resume\|status` | invoke the backup and restore functions | [backup](#backup-and-restore) |
| `render` | an installation in, the service and policy documents out | [render](#render-an-installation-in-the-two-documents-out) |
| `policy render` | the one policy document an installation reads, from its layers | [policy render](#policy-render-the-one-policy-document) |

| Detail | Behavior |
|---|---|
| Source | `usage` in `cmd/sluisctl/main.go` |
| Shared flags | `--audience`, `--issuer`, `--client`; the client defaults to what `login` wrote. `aws` also takes `--role` |
| Issuer choice | `--issuer`, else `$SLUISCTL_ISSUER`, else the `issuer:` key of `config.yaml` (these three pin the issuer), else the one issuer with a live session. When none is pinned and more than one issuer has a live session, the command exits 2 and lists them, rather than using the most recent login. A session counts as live if its file parses, holds a refresh token and does not record an expiry in the past. With no live session the last login is used, so the error is "not signed in". `login` without `--issuer` uses the pin, else the last login |
| `login` | Authorization code with PKCE on a loopback port. The policy client is `kind: public` with a loopback redirect and `sign_in_exchange: true`, else later commands exit 4 |
| `version` | `sluisctl <version>`, or `sluisctl dev` without release ldflags, with commit and date when stamped. `--json` prints `{"version","commit","date"}`. No network, config or `HOME` |
| `github-token` | `--app <id>`, `--repository <name>` (repeatable; none needs an all-repositories grant), `--permission <name>=<level>` (repeatable; none asks for the grant), `--json` prints `{"token","expires_at","repositories","permissions"}`. See [GitHub App tokens](../../guides/sluis/connect/github-app-tokens.md) |
| `credential ssh\|db\|client` | Removed in v1.34.0; the error names the replacement |
| Legacy identifiers | Legacy identifiers, renamed in v1.75–v1.76: the alias `accessctl` (release archives, Nix flake, deprecation notice on stderr), default OIDC client id `accessctl`, env `ACCESSCTL_*` (`SLUISCTL_*` wins), config directory `accessctl` (copied once), kubeconfig user `accessctl:<cluster>`, `~/.ssh/known_hosts.d/accessctl`, AWS session-name fallback |

## cloudflare token and cloudflare r2

How-to: [Cloudflare tokens](../../guides/sluis/cloudflare-tokens.md).

| Command | Output |
|---|---|
| `cloudflare token <preset> [--format env\|json] [--lifetime <duration>]` | `env` (default): `CLOUDFLARE_API_TOKEN=<token>`. `json`: `{"token","expires_on"}`. An R2 preset is refused with the command to use |
| `cloudflare r2 <preset> [--lifetime <duration>] [--file <path>]` | `credential_process` JSON `{"Version":1,"AccessKeyId","SecretAccessKey","Expiration"}`, no `SessionToken`. `--file` reads a projected `cloudflare/v1` document, signs in to nothing and refuses an expired one |

| Detail | Behavior |
|---|---|
| Flags | Both take `--issuer` and `--client`; CI uses the job identity. `--lifetime` is at most the preset's |
| Exit 4 | Not granted, or nonexistent |
| `aws-config` | Adds profile `<preset>@r2` per granted R2 preset: `credential_process = sluisctl cloudflare r2 <preset> --issuer <issuer>`, `endpoint_url`, `region = auto`, `request_checksum_calculation` and `response_checksum_validation = when_required`, `s3 =` with `addressing_style = path` |
| `whoami` | Lists granted presets |

## `render`: an installation in, the two documents out

```sh
sluisctl render --installation <file> --out <dir> [--check]
```

`render` writes `sluis.yaml` (`sluis/v3`) and `policy.yaml` (`policy/v2`) from an [installation](installation-document.md). The Pulumi library calls the same function ([ADR 0038](../../decisions/0038-estates-render-through-sluis.md)).

| Flag | Meaning |
|---|---|
| `--installation <file>` | The installation document. Required |
| `--out <dir>` | Output directory. Required |
| `--check` | Write nothing; print a line diff; exit `1` if any (a missing file counts) |

Output is deterministic and needs no network, session, `HOME` or secret. A failed render exits `1`, a bad command line `2`.

## `policy render`: the one policy document

```sh
sluisctl policy render [-o <file>] <file or directory>
```

`policy render` builds the one [policy document](policy-document.md) from layers, alone or as a directory read in name order. Without `-o` it writes to stdout. Name it in `policy.file`, or pass it as chart `policy` (without `apiVersion`) or Pulumi `Policy`.

| Layer | Form |
|---|---|
| Policy file | `version: 1` |
| Access document | `access:` and `overlay:`, reshaped into tables |
| Fragment | `apiVersion: sluis.truvity.github.io/policy/v2`: tables and the sections `exchange`, `apps`, `controllers` |

| Merge | Rule |
|---|---|
| Tables | By key; a key declared twice is an error naming the file |
| Lists, name lists | Concatenate, union |
| Scalars | Declared twice is an error |

The result passes every check the service runs at start. Exit codes as `render`.

## Where things are kept

`<config>` is the OS user configuration directory (`~/.config/sluisctl` on Linux). Files are `0600` in `0700` directories, written then renamed. A cache that is absent, expired or unwritable means mint afresh.

| File | Holds |
|---|---|
| `<config>/config.yaml` | `clientId` and `lastIssuer` (the last sign-in, not a pin), written by `login`; `issuer:` pins the issuer and is only ever set by hand. A file written by an older release holds the last login under `issuer:`, which therefore counts as a pin until that line is removed |
| `<config>/sessions/<issuer>-<hash>.json` | One per issuer: refresh token, last access token and expiry. A stored issuer that disagrees with the lookup reads as not signed in. Rotated tokens are written back; a refused refresh reads as not signed in |
| `<config>/sessions/<issuer>-<hash>.json.lock` | A refresh holds it, so concurrent commands do not spend the refresh token twice |
| `<config>/session.json` | The single-issuer session of older releases, read for any issuer without its own file. Never deleted; a `login` writes that issuer's file, which wins |

| Cache | Path | Keyed by | Offered until |
|---|---|---|---|
| `kube-token` | `<config>/kube/<hash>.json` | the issuer, the client id and the audience, hashed together | a minute before the token's expiry, which is when client-go re-runs the plugin |
| `aws` | `<config>/aws/<hash>.json` | the audience and the role ARN, hashed | five minutes before the credential's expiry, when the AWS SDK would refresh its own copy |
| `cloudflare` | `<config>/cloudflare/<hash>.json` | the issuer, the client id, the preset and the lifetime asked for, hashed together | with a third of the credential's lifetime left (the lifetime is stored beside it) |
| `r2` | `<config>/r2/<hash>.json` | the issuer, the client id and the audience, hashed together | a minute before the token's expiry, the same margin `kube-token` uses |

A lock beside each file makes many callers one exchange. `login` touches no cache; delete a cache directory to force a fresh exchange.

## What it writes into files that are not its own

| Target | Behavior |
|---|---|
| kubeconfig | Through `kubectl config`. Writes the credential and context, not the cluster entry; the address and CA come from the platform's kubeconfig or `aws eks update-kubeconfig` |
| AWS config | Rewrites everything between `# >>> sluisctl >>>` and `# <<< sluisctl <<<` on each run. A profile is `<role>@<account>` with `credential_process = sluisctl aws --audience aws:<account>:<role>`; no secret |

## In a job

In a job granted `id-token: write`, `kube-token`, `aws`, `token`, `github-token`, `bao`, `r2`, `pg` and `psql` exchange the job's identity token ([GitHub Actions](../../guides/sluis/connect/github-actions.md)). No `login` or session cache; per-credential caches still apply.

## clients rotate, show, purge

```sh
sluisctl clients rotate <id> [--overlap 24h]   # a new secret; the old one stays valid for the overlap
sluisctl clients show <id>                     # metadata only, never a secret
sluisctl clients purge <id>                    # delete the record of a client no longer generated
```

| Detail | Behavior |
|---|---|
| Flags | `--issuer`, `--client`; `--overlap` is `0` to `168h`, default `24h`; `0` cuts the old secret at once |
| Caller | An operator with a token for the default client or `console` |
| `purge` | Reminds you to delete `clients/<id>/secret` by hand |
| Docs | [rotate a client secret](../../guides/sluis/operate/rotate-a-client-secret.md), [`/.access/client-secrets`](endpoints.md#client-secrets) |

## backup and restore

```sh
sluisctl backup run [--resume] | status | list [--limit N]
sluisctl restore preview <backup-id>
sluisctl restore start <backup-id> --confirm <instance> [--overwrite] [--note TEXT] [--wait]
sluisctl restore resume [--wait]
sluisctl restore status
```

| Detail | Behavior |
|---|---|
| Credentials | The caller's own AWS credentials, the SDK's default chain, so a profile from `aws-config` works. No issuer, session or console. `--profile` and `--region` override |
| Function | `--function`, else `$SLUISCTL_BACKUP_FUNCTION` or `$SLUISCTL_RESTORE_FUNCTION`, else `backup.function` or `backup.restoreFunction` in `config.yaml`; `backup.region` and `backup.instance` too. Addresses, no secret |
| Class | `--as admin` (default) or `breakglass`: every call goes through the alias `live-<class>` |
| Backup | Module calls `backup.run` (synchronous), `backup.status`, `backup.list`. See [backup](backup.md) |
| `restore preview` | A synchronous restore event with `preview`: counts per module and section, names in `--json`, never values |
| `restore start` | Refuses unless `--confirm` equals the instance (`--instance`, `$SLUISCTL_INSTANCE`, `backup.instance`). Sends the event asynchronously with `by` set to the caller's `sts:GetCallerIdentity` ARN, then looks up the run and prints its id. `--wait` polls `restore.status` every 5 seconds until `completed`, or `failed` (exit 1) |
| `restore resume` | Does nothing when no restore is unfinished |
| IAM | `lambda:InvokeFunction` on the function's `live-admin` or `live-breakglass` alias; a refusal exits 4 |
| Output | Text, or `--json`; never a secret |

## Exit codes

| Code | Means |
|---|---|
| `0` | ok |
| `1` | anything else: read the message. For `clients`: a client not generated, without a record, still declared, or busy |
| `2` | usage: bad command line or overlap |
| `3` | not signed in: run `sluisctl login` (a `401`) |
| `4` | audience or App token not granted; for `backup` and `restore`, AWS or the function refused the caller; for `bao`, `pg`, `psql` also OpenBAO's `403`. Do not retry |
| `5` | issuer unreachable; for `bao`, `pg`, `psql` also OpenBAO. Retry may help |

## Installing it

Add the release's `sluisctl_<version>_nix-flake.tar.gz` URL with `#sluisctl` to `devbox.json` ([concepts](../../concepts/sluis/sluisctl.md#installing-it)).
