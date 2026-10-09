# sluisctl: the wrappers (`bao`, `r2`, `pg` / `psql`, `ssh known-hosts`)

Commands that authenticate, then run another program or write a file you own. Other commands and exit codes: [sluisctl](sluisctl.md). Concepts: [couriers](../../concepts/sluis/sluisctl.md#bao-psql--pg-r2-couriers-for-what-a-store-mints). Decided in [ADR 0013](../../decisions/0013-openbao-access-through-the-bao-cli.md) and [ADR 0016](../../decisions/0016-a-managed-known-hosts-file-for-ssh-host-cas.md).

## `bao`: authenticate, then run `bao` unchanged

`sluisctl bao [flags] <bao args>` puts a valid OpenBAO token in front of `bao`. sluisctl's flags go before the subcommand and bao's own (`-namespace`, `-ns`) after it.

| Flag | Default |
|---|---|
| `--address` | `$BAO_ADDR`, then `$VAULT_ADDR` |
| `--ca-cert` | `$BAO_CACERT`, then `$VAULT_CACERT` |
| `--mount` | `jwt-roster` |
| `--login-role` | `roster` |
| `--login-ns` | The target namespace, then `$SLUISCTL_BAO_LOGIN_NAMESPACE` |
| `--audience` | `openbao` |
| `--issuer`, `--client` | What `login` wrote |
| `--forget` | Revokes and removes the cached token; needs no bao command. With `--login-ns` it clears that entry |

| Behavior | Rule |
|---|---|
| Login namespace | The target namespace, from bao's `-namespace`/`--namespace`/`-ns`/`--ns` (last wins), then `BAO_NAMESPACE`, `VAULT_NAMESPACE`, then root |
| `--login-ns` | Logs in at a parent; the target must be it or a descendant by path segment (`dev` is not a parent of `devel`), else exit 2. `BAO_NAMESPACE` is never rewritten. See [logins at a parent namespace](../../guides/sluis/connect/openbao.md#logins-at-a-parent-namespace) |
| Token cache | `<config>/bao/<hash>.json`, `0600`, per address, login namespace and subject; never `~/.vault-token`. Offered until a margin before expiry (`sessionTokenMargin`). A login without a lease is used once and not cached |
| Child | Runs with `BAO_ADDR`, `BAO_TOKEN` and, when named, `BAO_CACERT` set; everything else flows through. `syscall.Exec` replaces the process except on Windows |

### `-format=env` on `kv get`

`bao kv get ... -format=env` (also `--format=env`, `-format env`, `BAO_FORMAT=env`) runs `bao` once as JSON and renders dotenv. Detection stops once `bao` lists `env` itself.

| Value | Output |
|---|---|
| String without `'`, CR or LF | `KEY='value'` |
| Other string | `KEY="value"`, escaping backslash, `"`, `$`, CR, LF |
| Number, boolean | Its JSON text |
| `null` | `KEY=''` |
| Object or array | Refused, naming the field |
| Key outside `[A-Za-z_][A-Za-z0-9_]*` | Refused, naming the key |
| `-field` with `-format=env` | Usage error |

### What each failure exits with

| Code | When |
|---|---|
| `2` | Unknown flag before the subcommand; no address; unreadable or empty CA bundle; empty `--audience`; no bao command and no `--forget`; `-field` with `-format=env`; `--login-ns` not covering the target |
| `3` | Not signed in: run `sluisctl login` |
| `4` | The issuer refused the exchange for `openbao`, or OpenBAO refused the login |
| `5` | No `bao` on `PATH`; issuer or OpenBAO unreachable |
| bao's own | Whatever `bao` exits with |

## `r2`: authenticate, then run the real `r2broker` CLI unchanged

Deprecated: use `sluisctl cloudflare r2 <preset>` ([how-to](../../guides/sluis/cloudflare-tokens.md)). See [ADR 0014](../../decisions/0014-minting-third-party-credentials-only-where-membership-is-governed.md) and [R2 storage](../../guides/sluis/connect/r2-storage.md).

```sh
sluisctl r2 --service-url https://r2-broker.example.com -- credentials \
    --bucket example-bucket --prefix nix/ --permission object-read-only
```

| Flag | Default |
|---|---|
| `--audience` | `$SLUISCTL_R2_AUDIENCE`, then `r2-broker` |
| `--service-url` | `$SLUISCTL_R2_SERVICE_URL`; injected as `r2broker`'s `--service-url` after the subcommand unless the command names one |
| `--issuer`, `--client` | What `login` wrote |

| Behavior | Rule |
|---|---|
| Subcommand | Defaults to `credentials`. Omitting it needs `--` first |
| Token cache | `<config>/r2/<hash>.json`, `0600`, per issuer, client and audience; same margin as `kube-token`. No `--forget` |
| Child | Runs with `R2BROKER_TOKEN` in its environment only, never argv or a file. The process is replaced as for `bao` |

| Code | When |
|---|---|
| `2` | Unknown flag before the subcommand (often a missing `--`); empty `--audience` |
| `3` | Not signed in |
| `4` | The issuer refused the exchange |
| `5` | No `r2broker` on `PATH`; issuer unreachable |
| `r2broker`'s own | Its exit: `0` ok, `2` usage, `3` refused, `4` upstream |

## `pg` / `psql`: a Postgres client certificate, then a command

`sluisctl pg [flags] -- <command> [args]` signs or reuses a Postgres client certificate and runs the command with libpq variables set. `psql [flags] [args]` is `pg -- psql`; put `--` before arguments that start with a flag. Guide: [PostgreSQL](../../guides/sluis/connect/postgresql.md).

```sh
sluisctl psql --address https://openbao.example:8200 -ns staging -- -h db.example -d orders
sluisctl pg   --address https://openbao.example:8200 -ns staging -- pg_dump orders > orders.sql
```

| Flag | Default | Meaning |
|---|---|---|
| `--address` | `$BAO_ADDR`, then `$VAULT_ADDR` | OpenBAO API |
| `--ca-cert` | `$BAO_CACERT`, then `$VAULT_CACERT` | PEM bundle added to system roots |
| `-ns` | `$BAO_NAMESPACE`, then `$VAULT_NAMESPACE` | Namespace of the PKI mount, where the certificate is signed |
| `--login-ns` | `-ns`, then `$SLUISCTL_BAO_LOGIN_NAMESPACE` | Login namespace; `-ns` or a parent |
| `-role` | `db-client` | PKI role |
| `-mount` | `pki` | PKI mount |
| `--common-name` | The signed-in identity | Requested common name |
| `--audience` | `openbao` | Exchange client OpenBAO accepts |
| `--issuer`, `--client` | What `login` wrote | |

The login is `bao`'s and shares its cache, keyed by address, login namespace, mount and role.

### The certificate

| Aspect | Rule |
|---|---|
| Cache | `<config>/credentials/<address-hash>/<ns>/<role>/client.{crt,key}`, plus `client-ca.crt` when the role returns a chain; `0600` in `0700`. No TTL is sent; the role's `max_ttl` decides |
| Reuse | While more than five minutes remain (`awsCacheMargin`) and the cached common name matches. Otherwise minted under a lock |
| Check | The certificate is checked against the key before it is written. A role that will not sign the name refuses |

### The environment

| Variable | Set |
|---|---|
| `PGSSLCERT`, `PGSSLKEY` | Always |
| `PGSSLROOTCERT`, `PGSSLMODE=verify-full`, `PGUSER` (the certificate's common name) | Only when absent, so `-U`, `user=` and a libpq service file still win. `PGSSLROOTCERT` is skipped when no `client-ca.crt` was written; the PKI CA may differ from the server's CA |

| Code | When |
|---|---|
| `2` | Unknown flag before `--`; no address; bad CA bundle; empty `--audience`; `pg` without a command; `--login-ns` not covering `-ns` |
| `3` | Not signed in |
| `4` | The issuer refused the exchange, or OpenBAO refused the login or the sign |
| `5` | No command (or `psql`) on `PATH`; issuer or OpenBAO unreachable |
| The command's own | Its exit |

## `ssh known-hosts`: trust configured SSH host CAs before the first connect

`sluisctl ssh known-hosts` writes the file it owns so a laptop trusts SSH host CAs before the first connection. `login` runs it only when something is configured. Guide: [SSH host certificates](../../guides/sluis/connect/ssh.md#hosts-host-certificates-from-openbaos-ssh-ca).

The list lives in `config.yaml` `sshKnownHosts:`, or in `$SLUISCTL_SSH_KNOWN_HOSTS` (the same YAML) when the file names none.

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

| Entry | Rule |
|---|---|
| `patterns` | `ssh_config` host patterns; whitespace, a comma or `#` is refused at load |
| `openbao: {namespace, mount}` | Reads `<address>/v1/<mount>/public_key` unauthenticated, namespace in `X-Vault-Namespace`. Address and CA as for `bao` |
| `url:` | A URL whose body is the CA's OpenSSH public key |
| Source | An entry names one of `url` or `openbao` |
| Key type | Only `ssh-ed25519` is written; others are refused by name |
| Fetch failure | Keeps the previous run's line for that pattern set with a warning, else skips it. The run does not fail |
| File | Rewritten atomically each run, `0644` in a `0700` directory. `~/.ssh/config` is never edited; the command prints the `UserKnownHostsFile` line to add when none names the managed file |

| Flag | Default | Meaning |
|---|---|---|
| `--file` | `~/.ssh/known_hosts.d/accessctl` | Managed file (a legacy identifier, renamed in v1.75–v1.76) |
| `--address` | `$BAO_ADDR`, then `$VAULT_ADDR` | OpenBAO API for `openbao:` entries |
| `--ca-cert` | `$BAO_CACERT`, then `$VAULT_CACERT` | PEM bundle added to system roots |

| Code | When |
|---|---|
| `2` | Entry with both or neither of `url`/`openbao`; no or bad patterns; `openbao.mount` missing; bad CA bundle |
| `0` | Everything else, an unfetched source included (a warning on stderr) |
