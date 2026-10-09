# Connect PostgreSQL with short-lived client certificates

Reach PostgreSQL, CloudNativePG included, with a client certificate from OpenBAO's PKI engine instead of a password. See [OpenBAO](openbao.md) and the [wrapper reference](../../../reference/sluis/sluisctl-wrappers.md#pg--psql-a-postgres-client-certificate-then-a-command).

## Before you start

- The PKI's CA can differ from the CA that signed the server's certificate. Name the server's root in `sslrootcert` or `PGSSLROOTCERT`.
- PostgreSQL ignores revocation unless `ssl_crl_file` or `ssl_crl_dir` is set. A certificate works until it expires, so keep `max_ttl` short.
- PostgreSQL 18 native OAuth does not work: the issuer serves no device authorization grant.

## 1. Run a command with a certificate

```sh
sluisctl psql --address https://openbao.example:8200 -ns staging -- -h db.example -d orders
```

`sluisctl` exchanges your sign-in for the `openbao` audience (`--audience`) and logs in to the `jwt-roster` mount in `-ns`. To log in at a parent namespace, use `--login-ns` ([logins at a parent namespace](openbao.md#logins-at-a-parent-namespace)). It then generates an ECDSA P-384 key locally and calls `pki/sign/<role>` (`-role`, default `db-client`). The key never leaves your machine.

The common name is your identity unless `--common-name` overrides it. A cached certificate is reused while it has five minutes of life left. The files `client.crt`, `client.key` and `client-ca.crt` are `0600` under `<config>/credentials/<address-hash>/<ns>/<role>/`. The command then runs with libpq variables set:

| Variable | Value |
|---|---|
| `PGSSLCERT`, `PGSSLKEY` | the certificate and key, always |
| `PGUSER` | the certificate's common name |
| `PGSSLMODE` | `verify-full` |
| `PGSSLROOTCERT` | `client-ca.crt`; skipped when the role returned no CA |

`sluisctl` sets the last three only when you have not: `-U`, `user=` or a service file wins. Other arguments pass through.

## 2. Commit a service file

Commit a secret-free `pg_service.conf` and point `PGSERVICEFILE` at it from `devbox.json` or `.envrc`.

```ini
# pg_service.conf, committed
[orders]
host=db.example
port=5432
dbname=orders
sslmode=verify-full
# sslrootcert=/path/to/server-ca.crt   # only if the server's CA differs from the PKI's
```

```sh
export PGSERVICEFILE=$PWD/pg_service.conf
sluisctl psql --address https://openbao.example:8200 -ns staging -- service=orders
```

`sluisctl` never writes this file; it adds the certificate through the environment.

## 3. Grant access

OpenBAO policy gates access on the `groups` claim of the exchanged token. One group names one thing that may be minted ([OpenBAO policy](openbao.md#1-declare-the-policy)).

```yaml
groups:
  staging:db:orders:client: { members: [team-eng@example.com] }
clients:
  openbao:
    kind: static
    requires: [staging:db:orders:client]
```

The PKI role's `ttl` and `max_ttl` set the lifetime; no flag does. See [OpenBAO manager side](openbao.md#2-configure-the-manager).

## 4. Configure the server

Trust the role's CA and map the common name to a database role: [Configure PostgreSQL](postgresql-server.md).

## Verify

```sh
sluisctl psql --address https://openbao.example:8200 -ns staging -- -h db.example -d orders -c 'select current_user'
```

The result is the database role you connected as: the common name, or the `-U` role.

Decided in: [ADR 0013](../../../decisions/0013-openbao-access-through-the-bao-cli.md).
