# sluisctl

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

sluisctl render --installation installation.yaml --out rendered/   # the service and policy documents
sluisctl policy render policy/ -o policy.yaml   # the one policy document an installation reads
```

> **Renamed from `accessctl`.** The product is now sluis, and this command is
> `sluisctl`. For one or two releases the release also carries `accessctl_*`
> archives (and an `accessctl` Nix flake): the same program under its old name,
> which prints a deprecation notice on **stderr** (never stdout, which a
> credential helper's caller parses) and otherwise behaves identically. Move
> kubeconfigs and AWS profiles with `sluisctl kubeconfig` and
> `sluisctl aws-config`. Settings read from the environment are `SLUISCTL_*`;
> the `ACCESSCTL_*` names they replaced still work, and `SLUISCTL_*` wins when
> both are set.
>
> **What did not change**, because renaming it would sign people out or break a
> relying party: the OIDC client id (`accessctl`), the configuration and cache
> directory (`accessctl` under the OS config directory), the kubeconfig user
> names (`accessctl:<cluster>`), the managed known-hosts file
> (`~/.ssh/known_hosts.d/accessctl`) and the AWS role-session name fallback.

It exists for one reason: **the cloud CLI has no interactive login.**
kubectl has kubelogin for the same job; AWS has nothing that will open a
browser. So one small binary runs the flow once and then answers as a
credential process, and the rest share that login's cache.

`login` is authorization code with PKCE on a loopback port — the only
browser flow served. The client must be declared in the policy as
`kind: public` with a loopback redirect **and `sign_in_exchange: true`**:
that key is what lets the exchange take a sign-in's access token as a
proof, and without it every command after `login` is refused (exit 4).
The default id is `sluisctl`.

`version` (also `--version` and `-version`, as the first argument) prints
this build's own version — `sluisctl <version>`, e.g. `sluisctl
1.35.0`, or `sluisctl dev` for one built without the release workflow's
ldflags — plus the commit and build date when the release stamps those
too. `--json` prints `{"version", "commit", "date"}`, including only
whichever of those this build actually carries. It makes no network call
and needs no config, session or `HOME`.

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
| `render` | an installation in, the service and policy documents out | [render](#render-an-installation-in-the-two-documents-out) |
| `policy render` | the one policy document an installation reads, from its layers | [policy render](#policy-render-the-one-policy-document) |

Source: `usage` in `cmd/sluisctl/main.go`.

Every command that names an audience takes `--audience`, `--issuer` and
`--client`, defaulting to what `login` wrote; `aws` also takes `--role`
for a role the audience does not encode.

`github-token` names a [catalogue App](../how-to/connect/github-app-tokens.md)
instead: `--app <id>`, `--repository <name>` (repeatable, without the
owner; none asks for a token not narrowed to any, which only a grant of
every repository allows), `--permission <name>=<level>` (repeatable; none
asks for exactly what the grant allows), and `--json` to print
`{"token", "expires_at", "repositories", "permissions"}` — what GitHub
granted — instead of the bare token. It takes `--issuer` and `--client`
like the rest.

### `cloudflare token` and `cloudflare r2`

`sluisctl cloudflare token <preset> [--format env|json] [--lifetime <duration>]`
asks the issuer for an account token of the preset, minted for you from its
prototype ([how-to](../how-to/cloudflare-tokens.md)). `--format env` (the
default) prints `CLOUDFLARE_API_TOKEN=<token>`; `--format json` prints
`{"token", "expires_on"}`. A preset that hands out R2 credentials is refused
with the command to use instead.

`sluisctl cloudflare r2 <preset> [--lifetime <duration>] [--file <path>]` prints
what the AWS SDKs read from `credential_process`:
`{"Version":1,"AccessKeyId","SecretAccessKey","Expiration"}`, no
`SessionToken`. `--file` reads the `cloudflare/v1` document a secrets operator
projected, signs in to nothing, and refuses an expired one.

Both take `--issuer` and `--client`, and answer from a job's own identity in CI
and from the sign-in on a laptop. `--lifetime` is at most the preset's (the
default). Exit codes as the rest: 4 when the preset is not granted (or does not
exist, which is the same answer), 5 when the issuer cannot be reached.

`aws-config` adds a profile `<preset>@r2` for each granted R2 preset, with
`credential_process = sluisctl cloudflare r2 <preset> --issuer <issuer>`,
`endpoint_url`, `region = auto`, `request_checksum_calculation = when_required`,
`response_checksum_validation = when_required` and `s3 =` with
`addressing_style = path`. `whoami` lists the granted presets.

**`sluisctl credential ssh|db|client` was removed in v1.34.0**
([ADR 0013](../decisions/0013-openbao-access-through-the-bao-cli.md)):
`bao` replaces `ssh` and `client`, and `pg`/`psql` replace `db` ([wrappers](sluisctl-wrappers.md)). Running any of the three now names its replacement.

## `render`: an installation in, the two documents out

```sh
sluisctl render --installation <file> --out <dir> [--check]
```

An estate states what it knows about an installation once, as an **installation**
([configuration](installation-document.md):
`apiVersion: sluis.truvity.github.io/installation/v1`, held to
`schemas/config/installation.schema.json`), and `render` writes the two documents an
instance is started with into `<dir>`: `sluis.yaml`, the service document
(`sluis/v3`), and `policy.yaml`, the policy document (`policy/v2`). It is the function
the Pulumi library and the Go package `github.com/truvity/sluis/config` call
([0038](../decisions/0038-estates-render-through-sluis.md)), so every estate renders
the same way.

The output is deterministic (sorted keys, `apiVersion` first, a fixed layout), derives
what the shape fixes (the preset, `policy.file`, the public URLs, the adapters the AWS
resources and the OpenBao stand for, the names layout v3 gives the secrets sluis
writes) and refuses an installation that disagrees with it, naming the key. Both
documents are held to the loader the service runs at start, so what `render` writes is
what the service accepts.

| Flag | Meaning |
|---|---|
| `--installation <file>` | the installation document (required) |
| `--out <dir>` | the directory the two documents are written to (required) |
| `--check` | write nothing: compare, print a line diff, exit `1` if there is any |

`--check` writes nothing: it compares what would be written with the files in
`<dir>`, prints a line diff, and exits `1` if there is any (a missing file counts). Run
it in the estate's CI to hold the committed documents to the installation. Like
`policy render`, it needs no network, no session and no `HOME`, and it holds no secret.
A failed render exits `1` with the reason; a bad command line exits `2`.

## `policy render`: the one policy document

```sh
sluisctl policy render [-o <file>] <file or directory>
```

An installation is decided by **one policy document**
([configuration](policy-document.md):
`apiVersion: sluis.truvity.github.io/policy/v2`, held to
`schemas/config/policy.schema.json`), and every process reads exactly that and
merges nothing. `policy render` is the one place layering happens: it builds the
document from the layers a deployment declares, which are, alone or as a directory
of them read in name order:

- a policy file of v1 (`version: 1`);
- an access document (`access:` and `overlay:`), reshaped into tables;
- a policy document fragment (`apiVersion: sluis.truvity.github.io/policy/v2`):
  tables and any of the sections `exchange`, `apps` and `controllers`.

Tables merge by key, and a key declared twice is an error naming the file. Of the
sections, a list concatenates and a list of names unions; a scalar declared by two
layers is an error. The result is held to every check the service runs at start
(a catalogue grant naming an undeclared group, an enabled organisation the policy
does not bind, and the rest), so a document this
writes is one the service accepts, and the same layers give the same bytes.

`-o <file>` writes the document there; without it, it goes to stdout. Name the
result in each service document's `policy.file`, or pass it to the chart as
`policy` (without its `apiVersion`) or to the Pulumi library as `Policy`. It
needs no network, no session and no `HOME`, and it holds no secret: a policy
document is reviewed in git. A failed render exits `1` with the reason; a bad
command line exits `2`.

## Installing it

Each release carries `sluisctl_<version>_nix-flake.tar.gz`,
a Nix flake over that release's own archives; a repository adds its URL
with `#sluisctl` to `devbox.json`
([why and how](../explanation/sluisctl.md#installing-it)). The release's archives
are there too for a plain download.

## Where things are kept

`<config>/config.yaml` (`~/.config/sluisctl/config.yaml` on Linux; `<config>` is the OS's
user configuration directory) holds the
**default** issuer and the client id, written by `login`.

The sign-in itself lives in `<config>/sessions/<issuer>-<hash>.json`,
mode `0600`: the refresh token, and since 1.25.1 the **access token of
the last refresh with its expiry** (`access_token`, `access_expires`).

**One file per issuer**, because a laptop belongs to more than one estate: a single file
made a sign-in at a second issuer replace the first one's refresh token, and `--issuer` then
handed the right endpoint the wrong token, which the issuer refuses as `subject_token is
invalid`, a message that reads as expiry and is not. The name is derived from the issuer so the
directory is readable; the issuer is also stored **inside** the file and that is what a read
checks, so a name that collides fails closed as *not signed in*. A `session.json` from before
the split is adopted by whoever asks first and replaced by the next `login`.

Nothing else changes: `config.yaml` still names the default issuer, so a
bare `sluisctl whoami` behaves as it always did, and every context
`kubeconfig` writes already passes its own `--issuer`.

The session is a file and not the OS keyring, deliberately: a keyring is a platform-specific
dependency and, on some laptops, a prompt in the middle of a `kubectl` call. It is the same
secret a browser keeps in a cookie jar, and `login` replaces it in one command if it leaks.

A rotated refresh token is written back. A refused refresh — revoked,
expired, or the account suspended — reads as *not signed in*, because
signing in again is the only answer.

The access token is kept too, because a refresh **spends** the refresh token (the issuer
rotates it) and two commands refreshing at once would leave one with a dead token. A command
with a live access token presents it; a refresh is taken under the lock
`<issuer>-<hash>.json.lock` beside that issuer's file, one per issuer. It is kept only when
its lifetime is known, and cleared otherwise.

**Two token caches, one file per credential**, for the same race one
level down — `kubectl` runs its exec plugin once per process, and every
provider process of a tool like Pulumi runs the credential process:

| Cache | Path | Keyed by | Offered until |
|---|---|---|---|
| `kube-token` | `<config>/kube/<hash>.json` | the issuer, the client id and the audience, hashed together — the same audience at another issuer is a different credential | a minute before the token's expiry, which is when client-go re-runs the plugin |
| `aws` | `<config>/aws/<hash>.json` | the audience and the role ARN, hashed — an account id is not something to scatter across a filesystem | five minutes before the credential's expiry, when the AWS SDK would refresh its own copy |
| `cloudflare` | `<config>/cloudflare/<hash>.json` | the issuer, the client id, the preset and the lifetime asked for, hashed together | with a third of the credential's lifetime left (the lifetime is stored beside it) |
| `r2` | `<config>/r2/<hash>.json` | the issuer, the client id and the audience, hashed together — the same key `kube-token` uses, because an R2-broker token is the same kind of credential | a minute before the token's expiry, the same margin `kube-token` uses |

Each file is `0600` in a directory made `0700`, written to a temporary
name and renamed so a reader never sees half a token, with a lock file
`<hash>.json.lock` beside it that turns a cold start by many callers
into one exchange. **Every cache is advisory in every direction**: a
file that is absent, truncated, unreadable, expired, from an older
version of this command, or unwritable means *mint afresh*, and none of
them is ever an error. `login` does not touch them — it writes
`config.yaml` and that issuer's session file and nothing else — so a token cached
under the previous sign-in is offered until its expiry margin; a revoked
audience is refused by the relying party until then, and deleting the
`kube`, `aws` and `r2` directories is how to force a fresh exchange. In a
job the same files are written wherever `kube-token`, `aws` or `r2`
runs, keyed the same way; only the login cache is absent there.

## What it writes into files that are not its own

**The kubeconfig, through `kubectl config`** and never by rewriting the
file. A person's other contexts are none of this tool's business.

It writes the credential and the context; the **cluster entry is not
ours** — the API server's address and its CA come from the platform's own
kubeconfig or from `aws eks update-kubeconfig`, and inventing one would
be inventing an address to trust.

**The AWS config, between two markers.** Everything between
`# >>> sluisctl >>>` and `# <<< sluisctl <<<` is rewritten each run;
everything outside is left exactly as it was. Rewriting rather than
appending matters: a profile for a role somebody no longer holds must not
survive as an entry that fails only when used.

Each profile is `<role>@<account>` with
`credential_process = sluisctl aws --audience aws:<account>:<role>`, and
holds no secret.

## In a job

The same files work unchanged in a GitHub Actions job granted
`id-token: write`. When `ACTIONS_ID_TOKEN_REQUEST_URL` and
`ACTIONS_ID_TOKEN_REQUEST_TOKEN` are set, `kube-token`, `aws`, `token`,
`github-token`, `bao`, `r2`, `pg` and `psql` ask GitHub for the job's identity
token — for the issuer's URL, the one
audience it accepts — and exchange that, presenting the audience as the
client, exactly as the GitHub Action does. There is no `login` in a job
and no login cache: every proof is the job's own token, exchanged afresh,
though `kube-token`, `aws` and `r2` keep their per-credential caches
(see the table above) there as anywhere. A repository that would rather
download nothing of ours uses the action, which is `curl` and `jq`
([../connect/github-actions.md](../how-to/connect/github-actions.md)).

## clients: rotate, show, purge

```sh
sluisctl clients rotate <id> [--overlap 24h]   # a new secret; the old one stays valid for the overlap
sluisctl clients show <id>                     # metadata only, never a secret
sluisctl clients purge <id>                    # delete the record of a client no longer generated
```

The id and the flags may come in either order; all three take `--issuer` and `--client`. `--overlap` is a duration from
`0` to `168h` (default `24h`; `0` cuts the old secret at once). The caller must be an operator, with a token issued to
`accessctl` or `console`. `rotate` and `purge` print what they did; `purge` also says to delete
`clients/<id>/secret` by hand. How: [rotate a client secret](../how-to/rotate-a-client-secret.md). Endpoints:
[`/.access/client-secrets`](endpoints.md#client-secrets).

These commands exit `2` for a bad command line or overlap, `3` when not signed in (a `401`), `4` when refused (a `403`:
not an operator, or the wrong audience), `5` when the issuer cannot be reached, and `1` for everything else, including
a client that is not generated, has no record, is still declared, or is busy.

## Exit codes

They are a contract: a wrapper should be able to tell *sign in again*
from *the issuer is down* without parsing English, and should know not to
retry a refusal.

| Code | Means |
|---|---|
| `0` | ok |
| `1` | anything the codes below do not name: read the message |
| `2` | usage: something in the command line is wrong |
| `3` | not signed in — run `sluisctl login` |
| `4` | that audience, or that App's token, is not granted to you (for `bao`, `pg` and `psql`, also OpenBAO's `403`); retrying will not help |
| `5` | the issuer could not be reached (for `bao`, `pg` and `psql`, also OpenBAO); retrying might |
