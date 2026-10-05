# sluisctl: the wrappers (`bao`, `r2`, `pg` / `psql`, `ssh known-hosts`)

The commands that authenticate and then run another program, or write a file the person owns.
The other commands, the exit codes and where `sluisctl` keeps its files are in
[sluisctl](sluisctl.md). Why these four exist: [sluisctl and the GitHub Action](../explanation/sluisctl.md#bao-psql--pg-r2-couriers-for-what-a-store-mints).

## `bao`: authenticate, then run `bao` unchanged

`sluisctl bao <args…>` exists only to put a valid OpenBAO token in front
of the real `bao` binary — it does not parse OpenBAO's own syntax, and
everything after sluisctl's own flags is `bao`'s, unchanged
([ADR 0013](../decisions/0013-openbao-access-through-the-bao-cli.md)).

**The separation rule:** sluisctl's own flags go BEFORE the bao
subcommand; `bao`'s own flags — including its `-namespace` (or bao's own
documented shortcut, `-ns`) — go AFTER it, exactly where `bao` has
always accepted them (`bao kv get -ns=dev secret/foo`). Parsing stops at
the first argument that is not one of sluisctl's declared flags, which
for this command is the subcommand itself (`kv`, `ssh`, `write`,
`login`, ...) — an ordinary word, never a flag.

sluisctl's own flags: `--address`, `--ca-cert`, `--mount`
(`jwt-roster`), `--login-role` (`roster`), `--login-ns` (default: the
target namespace; then `$SLUISCTL_BAO_LOGIN_NAMESPACE`), `--audience`
(`openbao`), `--issuer`, `--client` — the same resolution order (flag,
then `BAO_*`, then `VAULT_*`) `pg`/`psql`, below, use for the ones they
share. `--forget` revokes the cached token and removes it, needing no bao
command at all — with `--login-ns`, it clears the entry cached at that
login namespace.

**The login happens in the SAME namespace `bao` is about to operate
in, by default** — read out of `bao`'s own `-namespace`/`--namespace`, or
its shortcut `-ns`/`--ns` (wherever either appears among the arguments,
last one wins regardless of spelling), then `BAO_NAMESPACE`, then
`VAULT_NAMESPACE`, then root — because a token minted by logging in to
one OpenBAO namespace is only valid there and in its children, never in
a sibling.

**`--login-ns` (or `$SLUISCTL_BAO_LOGIN_NAMESPACE`) logs in at a PARENT
namespace instead**, for an installation that keeps its logins at one
namespace while data lives in per-project children — see
[connect/openbao.md#logins-at-a-parent-namespace](../how-to/connect/openbao.md#logins-at-a-parent-namespace).
The target namespace must be `--login-ns` itself or a descendant of it
(a path-segment prefix, not a string prefix: `dev` is not a parent of
`devel`), checked before any exchange is made — a target the login
namespace does not cover is refused as a usage error rather than left to
fail later at OpenBAO as a permission-denied that reads as an outage.
`bao` itself still runs against its own target namespace unchanged;
`BAO_NAMESPACE` is never rewritten.

**The token is cached**, one file per OpenBAO address, LOGIN namespace and
subject, under `<config>/bao/<hash>.json`, `0600` — never
`~/.vault-token` and never bao's own token helper file. Keying by the
login namespace rather than the target is what lets `bao -ns=devel/a` and
`bao -ns=devel/b` share one login when `--login-ns=devel` covers both: it
is the same login either way. Offered until a
margin before its own expiry (the same margin the session's own access
token uses, `commands.go`'s `sessionTokenMargin`); a login whose answer
carries no lease at all is used for that one command and never cached,
the same rule the kubectl and AWS caches already keep for a credential
with no visible expiry.

**`bao` is run with `BAO_ADDR`, `BAO_TOKEN` and (when named) `BAO_CACERT`
set in the child's environment alone** — everything else the caller's
shell already has, `BAO_NAMESPACE` included, flows through untouched.
Wherever the platform allows it (every platform but Windows), the
process image is replaced (`syscall.Exec`) rather than run as a child, so
an interactive `bao ssh -mode=ca` or `bao login` gets the terminal
exactly as if it had been run directly; on Windows a child process
propagates the exit code the same way.

### `-format=env` on `kv get`

The one exception to "unchanged": `bao kv get ... -format=env` (also
`--format=env`, `-format env`, or `BAO_FORMAT=env`) does not exist
upstream yet. Until it does, sluisctl runs `bao` once in JSON and
renders the dotenv itself:

- a string with none of `'`, CR or LF is `KEY='value'`;
- any other string is `KEY="value"`, with backslash, `"`, `$`, CR and LF
  escaped;
- a number or boolean is written as its own JSON text;
- `null` is `KEY=''` — an explicit empty value, not a dropped line;
- a nested object or array is refused, naming the field: there is no
  dotenv syntax for either, and flattening one would invent a shape the
  secret does not have;
- a key outside `[A-Za-z_][A-Za-z0-9_]*` is refused, naming the key: it
  is never renamed to make it fit;
- `-field` combined with `-format=env` is a usage error;
- bao's own errors and exit code pass through unchanged.

Detected once, cheaply, against the installed `bao`'s own `-format`
help: the day it lists `env` on its own, this stops intercepting and the
call is bao's own answer, unchanged.

### What each failure exits with

| Code | When |
|---|---|
| `2` | a flag sluisctl does not recognise before the subcommand; no OpenBAO address; a CA bundle that cannot be read or holds no certificate; an empty `--audience`; no bao command and no `--forget`; `-field` combined with `-format=env`; `--login-ns` (or `$SLUISCTL_BAO_LOGIN_NAMESPACE`) that does not cover the target namespace |
| `3` | not signed in (on a laptop): run `sluisctl login` |
| `4` | the issuer refused the exchange for `openbao`, or OpenBAO refused the login |
| `5` | no `bao` on `PATH`; the issuer or OpenBAO could not be reached |
| bao's own | whatever `bao` itself exits with, once it is run — sluisctl adds nothing on top and prints nothing of its own |

## `r2`: authenticate, then run the real `r2broker` CLI unchanged

`sluisctl r2 [flags] [-- <r2broker args…>]` exists only to put a valid
bearer token in front of the real `r2broker` binary — the CLI for an R2
credential broker (temporary, prefix-scoped object-storage credentials).
It parses none of `r2broker`'s own syntax; everything after sluisctl's
own flags is `r2broker`'s, unchanged
([ADR 0014](../decisions/0014-minting-third-party-credentials-only-where-membership-is-governed.md),
the same shape [ADR 0013](../decisions/0013-openbao-access-through-the-bao-cli.md)
already ships for `bao`, above). sluis holds no R2 logic at all —
no bucket, no prefix, no permission is ever named by this tool.

```sh
sluisctl r2 --service-url https://r2-broker.example.com -- credentials \
    --bucket example-bucket --prefix nix/ --permission object-read-only
```

**Flags:** `--audience` (default: `$SLUISCTL_R2_AUDIENCE`, then
`r2-broker`), `--service-url` (default: `$SLUISCTL_R2_SERVICE_URL`),
`--issuer`, `--client` — the same resolution order every other command
uses.

**The subcommand defaults to `credentials`** when the command names none
— the shape a `credential_process` line wants, since `credentials` is the
one subcommand a caller of this wrapper ever needs (`serve` runs the
broker service itself, holding its own parent key, which a sign-in
exchange is never for). Naming a subcommand explicitly needs nothing
special; omitting it needs a `--` first, exactly as any other use of
Go's flag package requires, so that `--bucket` is not parsed as one of
sluisctl's own flags. **`--service-url`, when configured, is injected
as `r2broker`'s own `--service-url`** right after the subcommand, unless
the command already names one — an explicit `--service-url` on the
command always wins.

**The token is cached**, one file per issuer, client and audience, under
`<config>/r2/<hash>.json`, `0600` — the same shape and margin
`kube-token`'s own cache uses ([where things are kept](sluisctl.md#where-things-are-kept)), because
an R2-broker token is the same kind of thing a `kube-token` credential
is: a bearer token for one audience. There is no `--forget`: unlike
`bao`'s OpenBAO login, nothing here is a login this tool minted and must
explicitly revoke — it is an ordinary exchanged access token that simply
expires.

**`r2broker` is run with `R2BROKER_TOKEN` set in the child's environment
alone** — never on argv, never in a temp file. `r2broker`'s own
documented token precedence (`--token-file`, then
`$R2BROKER_TOKEN_FILE`, then `$R2BROKER_TOKEN`) names this exact
fallback for a caller with no file to hand it, and it is the one shape
that survives `runChild` replacing this process's image
(`syscall.Exec`, wherever the platform allows it): a temp file written
before an exec that never returns is a temp file this tool could never
clean up. The process image is replaced exactly as `bao`'s own is, so an
interactive `r2broker` invocation gets the terminal exactly as if it had
been run directly; on Windows a child process propagates the exit code
the same way.

Used as an AWS `credential_process` — the primary use, one CI cache tool
at a time — see
[docs/connect/r2-storage.md](../how-to/connect/r2-storage.md).

### What each failure exits with

| Code | When |
|---|---|
| `2` | a flag sluisctl does not recognise before the subcommand (commonly: `--` was left out before `r2broker`'s own flags); an empty `--audience` |
| `3` | not signed in (on a laptop): run `sluisctl login` |
| `4` | the issuer refused the exchange for the R2 broker's audience |
| `5` | no `r2broker` on `PATH`; the issuer could not be reached |
| `r2broker`'s own | whatever `r2broker` itself exits with, once it is run — sluisctl adds nothing on top and prints nothing of its own (`r2broker`'s own contract: `0` ok, `2` usage, `3` refused, `4` upstream) |

## `pg` / `psql`: a Postgres client certificate, then a command

`sluisctl pg [flags] -- <command> [args…]` authenticates, mints (or
reuses) a Postgres client certificate, and runs `<command>` with libpq's
own environment variables pointed at it. `sluisctl psql [flags]
[psql args…]` is the shorthand for `sluisctl pg -- psql [psql args…]`.
This replaces `sluisctl credential db`, removed in v1.34.0
([ADR 0013](../decisions/0013-openbao-access-through-the-bao-cli.md)).

**The separation rule is the same as `bao`'s.** sluisctl's own flags go
before `--` (or before the first argument that is not one of them);
everything after is the command's own, unchanged. `psql`'s arguments
usually start with a flag (`-h`, `-d`), so `--` is needed there too
whenever the first one does; a bare positional argument (a database
name, `service=name`) does not.

```sh
sluisctl psql --address https://openbao.example:8200 -ns staging -- -h db.example -d orders
sluisctl pg   --address https://openbao.example:8200 -ns staging -- pg_dump orders > orders.sql
```

### Flags

<!-- generated: cli-sluisctl -->
| Flag | Default | |
|---|---|---|
| `--address` | `$BAO_ADDR`, then `$VAULT_ADDR` | the OpenBAO API |
| `--ca-cert` | `$BAO_CACERT`, then `$VAULT_CACERT` | a PEM bundle to trust, added to the system's roots |
| `-ns` | `$BAO_NAMESPACE`, then `$VAULT_NAMESPACE` | the OpenBAO namespace the PKI mount lives in, and where the certificate is signed |
| `--login-ns` | `-ns` itself; then `$SLUISCTL_BAO_LOGIN_NAMESPACE` | the namespace to log in at, when it differs from `-ns` — must be `-ns` or a parent of it |
| `-role` | `db-client` | the OpenBAO PKI role to sign with |
| `-mount` | `pki` | the OpenBAO PKI mount |
| `--common-name` | the signed-in identity | the common name to ask for |
| `--audience` | `openbao` | the exchange client OpenBAO accepts |
| `--issuer`, `--client` | what `login` wrote | as for every other command |
<!-- /generated -->

`-ns` and `-mount` are spelled short, mirroring `bao`'s own
`-namespace`. **The authentication step is `bao`'s own**
(`openBAOLogin` in the source): the sign-in (or a job's own identity)
exchanged for `--audience`, then logged in to the JWT mount, in
`--login-ns` (`-ns` itself by default) — and it shares `bao`'s cache,
keyed by the LOGIN namespace, so a `bao` call and a `pg`/`psql` call that
agree on the address, login namespace, mount and login role reuse the
same login, even when their own target `-ns` differ (one a descendant of
the other, both descendants of the shared login namespace). The
certificate itself is always signed, and cached, in `-ns` — the target —
regardless of where the login happened; see
[connect/openbao.md#logins-at-a-parent-namespace](../how-to/connect/openbao.md#logins-at-a-parent-namespace).

### The certificate

**Reused while it has enough life left AND was minted for the same
common name, minted under a lock otherwise.** Cached at
`<config>/credentials/<address-hash>/<ns>/<role>/client.{crt,key}` (and
`client-ca.crt` when the role returns a chain), `0600` in a `0700`
directory — the private key never leaves this process except into that
file, and no TTL is ever sent, exactly like `bao`'s own login: the PKI
role's `max_ttl` is the only answer. The address is hashed into the
path so two OpenBAO installations sharing a namespace and role name
never share a leaf. The margin before reuse stops is five minutes (the
same one `aws_cache.go`'s `awsCacheMargin` uses), wider than the login
token's own minute-scale margin because a certificate is handed to a
connection that may keep using it for a while, not spent in one round
trip. The common name is resolved (`--common-name`, else the signed-in
identity) BEFORE the cache is read, and a cached leaf whose own
`CommonName` no longer matches is never reused — `--common-name other`,
or simply signing in as someone else, mints fresh rather than silently
handing over the previous identity's certificate.

The certificate returned is checked against the key before anything is
written, exactly as `sluisctl credential` used to. A role that will
not sign the name asked for refuses, which is the right place for that
decision.

### The environment

**`PGSSLCERT` and `PGSSLKEY` are always set**, pointing at the
certificate above — there is no caller value for them that would make
sense to keep instead. **`PGSSLROOTCERT`, `PGSSLMODE=verify-full` and
`PGUSER` (the certificate's own common name) are set ONLY when not
already in the environment** — libpq treats every one of these strictly
as a default: an explicit connection-string keyword (`-U`, `user=`) or a
libpq service file's own setting (`PGSERVICEFILE`, or
`~/.pg_service.conf`, `service=<name>`) both outrank it, so a
repository's own committed service file, or a plain `-U`, still wins.
`PGSSLROOTCERT` is also skipped entirely when the role returned no
chain and no issuing certificate (no `client-ca.crt` was written), and
in general is a deliberate default rather than an authority: the PKI's
CA is not necessarily the CA that signed the database SERVER's own
certificate, so a server behind a different CA needs its root named a
different way (a repository's service file `sslrootcert=`, or the
caller's own `PGSSLROOTCERT`) — this only supplies what nothing else
already decided.
[connect/postgresql.md](../how-to/connect/postgresql.md) is the how-to,
including the server side and a committed, secret-free service file as
the recommended repo pattern.

### What each failure exits with

| Code | When |
|---|---|
| `2` | a flag sluisctl does not recognise before `--`; no OpenBAO address; a CA bundle that cannot be read or holds no certificate; an empty `--audience`; `pg` with no command; `--login-ns` (or `$SLUISCTL_BAO_LOGIN_NAMESPACE`) that does not cover `-ns` |
| `3` | not signed in (on a laptop): run `sluisctl login` |
| `4` | the issuer refused the exchange for `openbao`, or OpenBAO refused the login or the sign |
| `5` | no `<command>` (or no `psql`) on `PATH`; the issuer or OpenBAO could not be reached |
| the command's own | whatever `psql` or the command itself exits with, once it is run |

## `ssh known-hosts`: trust configured SSH host CAs before the first connect

`sluisctl ssh known-hosts` writes ONE file it owns,
`~/.ssh/known_hosts.d/sluisctl` by default, so a laptop trusts a
fleet's SSH host certificate authorities before the first connection
instead of being prompted for one — see
[connect/ssh.md#hosts-host-certificates-from-openbaos-ssh-ca](../how-to/connect/ssh.md#hosts-host-certificates-from-openbaos-ssh-ca)
for the shape of what it replaces, and
[docs/decisions/0016](../decisions/0016-a-managed-known-hosts-file-for-ssh-host-cas.md)
for why this is a laptop-configuration command rather than an OpenBAO
one, despite one of its two sources being OpenBAO. `sluisctl login`
runs it automatically, but only when something is configured — most
installations name nothing here at all, and a fresh sign-in never fails
or prints anything over a feature it never opted into.

**Nothing here ships in this binary.** The whole list of CAs to trust
lives in `config.yaml`'s own `sshKnownHosts:` section, or
`$SLUISCTL_SSH_KNOWN_HOSTS` (the same YAML, as text) when the file
names none — the file's own list wins whenever it names anything at
all. Each entry pairs one or more `ssh_config`-style host patterns with
exactly one CA source:

```yaml
# config.yaml
issuer: https://access.example
clientId: sluisctl
sshKnownHosts:
  - patterns: ["*.devel.example"]
    openbao:
      namespace: env
      mount: ssh-host
  - patterns: ["ip-10-0-*.example.ts.net"]
    url: https://ca.example/ssh-host-ca.pub
```

`openbao: {namespace, mount}` reads
`<address>/v1/<mount>/public_key`, unauthenticated, with the namespace
sent as `X-Vault-Namespace` — the same call `bao read ssh/config/ca`
makes — joined with the address `sluisctl bao` already resolves:
`--address`, then `$BAO_ADDR`, then `$VAULT_ADDR` (`--ca-cert`, then
`$BAO_CACERT`/`$VAULT_CACERT`, the same way). `url:` is a full URL
answering with the CA's OpenSSH public key as its whole body, plain
text, for an installation that fronts its CA some other way.

**Only `ssh-ed25519` CA keys are ever written**, refused by name
otherwise — OpenBAO's SSH secrets engine defaults new CAs to it, and
every host certificate this design targets is signed by one. A pattern
with whitespace, a comma, or `#` is refused at load, naming the entry:
patterns are joined with `,` on the rendered line, and any of those
characters could inject a second field or a second line into
`known_hosts`.

**A fetch failure never drops trust silently, and never fails the whole
run for one environment's CA being briefly down.** The previous run's
line for that exact set of patterns is kept (with a warning) when there
is one; the entry is skipped (with a warning) when there is not. The
file is otherwise fully rewritten on every run — never appended to —
written atomically (a temporary file, then a rename), `0644` in a
`0700` directory.

**It never edits `~/.ssh/config`.** That file is the person's own, the
same reason `kubeconfig` writes through `kubectl config` rather than
rewriting a kubeconfig wholesale. It only checks whether a
`UserKnownHostsFile` line already names the managed file (a plain
substring check, the tilde form or the absolute path, whichever was
written) and, when none does, prints the one line to add:

```
UserKnownHostsFile ~/.ssh/known_hosts ~/.ssh/known_hosts.d/sluisctl
```

### Flags

<!-- generated: cli-sluisctl -->
| Flag | Default | |
|---|---|---|
| `--file` | `~/.ssh/known_hosts.d/sluisctl` | the managed file to write |
| `--address` | `$BAO_ADDR`, then `$VAULT_ADDR` | the OpenBAO API for `openbao:` entries |
| `--ca-cert` | `$BAO_CACERT`, then `$VAULT_CACERT` | a PEM bundle to trust, added to the system's roots |
<!-- /generated -->

### What each failure exits with

| Code | When |
|---|---|
| `2` | an entry names both or neither of `url`/`openbao`; an entry names no patterns, or a pattern with whitespace, a comma or `#`; `openbao.mount` missing; a CA bundle that cannot be read or holds no certificate |
| `0` | everything else, including a source that could not be fetched — that is a warning on stderr, not a failed run: see above |
