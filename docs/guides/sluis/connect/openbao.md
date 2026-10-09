# Connect OpenBao

OpenBao trusts the issuer on a JWT auth mount and mints short-lived certificates for SSH, databases and mutual TLS. `sluisctl bao <args…>` authenticates and runs `bao`.

See the [contract](https://github.com/truvity/openbao/blob/master/docs/integrations/sluis.md), the [issuer side](openbao-issuer-side.md) and the [wrapper reference](../../../reference/sluis/sluisctl-wrappers.md#bao-authenticate-then-run-bao-unchanged).

## Before you start

- The policy decides who may ask. The manager's roles decide what they get, lifetime included.

- Every kind signs a key made on the caller's machine.

## Steps

### 1. Declare the policy

Declare one exchange client and one group per thing that may be minted:

```yaml
groups:
  staging:ssh:user:         { members: [team-eng@example.com] }
  staging:ssh:admin:        { members: [role-sre@example.com] }
  staging:db:orders:client: { members: [team-eng@example.com] }
  staging:machine:gateway:  { members: [role-sre@example.com] }
clients:
  openbao:
    kind: static
    requires: [staging:ssh:user, staging:ssh:admin, staging:db:orders:client, staging:machine:gateway]
```

Group names follow the [naming rule](../../../concepts/sluis/trust.md#naming).

### 2. Configure the manager

- A JWT auth mount per namespace with `bound_audiences: [openbao]`. `sluisctl` logs in on mount `jwt-roster` as role `roster` (legacy identifiers, renamed in v1.75–v1.76); `--mount` and `--login-role` override them.

- An SSH CA per environment and a role per group: `allow_user_certificates: true`, `allow_host_certificates: false`, `allowed_users`, `allow_user_key_ids: false`, short `ttl` and `max_ttl`. Set `key_id_format` to the token's display name.

- PKI roles `db-client` and `client` at `pki/sign/<role>`: `key_type: ec`, `key_bits: 384`, client-auth key usage only, a short `max_ttl`. The database role takes the subject as common name. The machine role sets the SAN the consumer matches.

- Policies that grant the sign path only, with no `read` or `list` on role paths, and an audit device.

### 3. Use it

```sh
sluisctl bao --address https://openbao.example:8200 ssh -mode=ca -role=user -ns=staging deploy@host.example

sluisctl psql --address https://openbao.example:8200 -ns staging -- -h db.example -d orders

sluisctl bao --address https://openbao.example:8200 write -ns=staging -field=signed_key \
    ssh/sign/user public_key=@key.pub > key-cert.pub
```

`--address`, `BAO_ADDR` or `VAULT_ADDR` must name the manager. `bao`'s own `-role` is `user` by default, `admin` for the administering account.

For a private root, pass `--ca-cert <file>` or set `BAO_CACERT`. Put sluisctl's flags before the `bao` subcommand, `bao`'s after. `sluisctl bao --forget` revokes and clears the cached token.

Check the subject with `ssh-keygen -L -f key-cert.pub`.

### Logins at a parent namespace

When logins live in an environment namespace and data in per-project children, set the login namespace. The target must be that namespace or a path-segment descendant: `dev` does not cover `devel`. Otherwise the command exits `2`.

```sh
SLUISCTL_BAO_LOGIN_NAMESPACE=<env> sluisctl bao kv get -ns=<env>/<project> -mount=kv -format=env <path>
```

`--login-ns` does the same. Projects under one `<env>` share one cached login.

## In a job

The same commands run in a GitHub Actions job with `id-token: write`; the `ci` rules decide the groups ([GitHub Actions](github-actions.md)). A job has no ssh-agent: use `write ... > key-cert.pub`.

## Decided in

- [ADR 0013](../../../decisions/0013-openbao-access-through-the-bao-cli.md)

- [ADR 0002](../../../decisions/0002-mission-boundary-tokens-and-memberships.md)
