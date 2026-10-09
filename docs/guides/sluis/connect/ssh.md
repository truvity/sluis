# Connect SSH for people and machines

Sign people in to `sshd` with opkssh and machines with OpenBAO-signed certificates. sluis never signs an SSH key.

## Before you start

- opkssh refuses ES384, the issuer default. Pin `signing_alg` on the opkssh client row.
- opkssh compares only the last `:` segment of a group name. Pin `groups_delimiter: "."` and write `auth_id` lines with dots.
- Each server must reach the issuer's discovery document and JWKS on every verification.

These steps use opkssh v0.16.0.

## People: opkssh

```yaml
clients:
  opkssh:
    kind: public
    redirects:
      - http://localhost:3000/login-callback
      - http://localhost:10001/login-callback
      - http://localhost:11110/login-callback
    requires:         [devel:build-worker:operator, devel:router:operator]
    signing_alg:      ES256       # or RS256
    groups_delimiter: "."
    display_name:     opkssh
```

Declare the public client row. See [signing algorithm per audience](../../../reference/sluis/policy.md#signing-algorithm-per-audience) and [groups delimiter](../../../reference/sluis/policy.md#groups-delimiter-per-audience-opkssh-interop). Remove the delimiter once opkssh stops splitting on every `:`.

On each client machine, add the provider to `~/.opk/config.yml`. `opkssh login --create-config` writes a skeleton.

```yaml
default_provider: sluis

providers:
  - alias: sluis
    issuer: https://access.example
    client_id: opkssh
    scopes: openid email profile groups
    redirect_uris:
      - http://localhost:3000/login-callback
      - http://localhost:10001/login-callback
      - http://localhost:11110/login-callback
```

Without `groups` in `scopes`, the token has no `groups` claim and every `oidc:groups:` rule fails.

On each server, install opkssh, which creates the low-privilege `opksshuser`, then wire it into `sshd`.

```
# /etc/ssh/sshd_config
AuthorizedKeysCommand /usr/local/bin/opkssh verify %u %k %t
AuthorizedKeysCommandUser opksshuser
```

```
# /etc/opk/providers — issuer, client_id, expiration policy
https://access.example opkssh 24h
```

```
# /etc/opk/auth_id — principal   identity                                        issuer
ops  oidc:groups:devel.build-worker.operator  https://access.example
```

Reload `sshd`. `24h` matches the issuer's session limit. Add `auth_id` lines by hand or with `sudo opkssh add ops oidc:groups:devel.build-worker.operator sluis`.

### Host groups

Name groups `<scope>:<thing>:<role>` ([naming](../../../reference/sluis/policy.md#naming)); a host kind is the `thing`.

```yaml
groups:
  devel:build-worker:user:     { members: [team-eng@example.com] }
  devel:build-worker:operator: { members: [role-sre@example.com] }
  devel:build-worker:admin:    { members: [role-sre-lead@example.com] }
```

Without a vocabulary, groups are flat: repeat one on each `auth_id` line it should reach.

```
ops   oidc:groups:devel.build-worker.user      https://access.example
ops   oidc:groups:devel.build-worker.operator  https://access.example
ops   oidc:groups:devel.build-worker.admin     https://access.example
root  oidc:groups:devel.build-worker.admin     https://access.example
```

With a [declared vocabulary](../../../reference/sluis/policy.md#vocabulary) such as `roles: { user: [], operator: [user], admin: [operator] }`, an admin also holds the lower roles. Two lines suffice.

```
ops   oidc:groups:devel.build-worker.user   https://access.example
root  oidc:groups:devel.build-worker.admin  https://access.example
```

## Machines: OpenBAO-signed certificates

A job has no browser. `sluisctl bao` exchanges the job's token and logs in to OpenBAO ([OpenBAO](openbao.md), [GitHub Actions](github-actions.md), [Kubernetes cluster](kubernetes-cluster.md)). For an interactive session:

```sh
sluisctl bao --address https://openbao.example:8200 ssh -mode=ca -ns=staging -role=user deploy@build-worker.example
```

For scp, git, CI and Ansible, sign an existing public key:

```sh
ssh-keygen -t ed25519 -f id_ci -N ''
sluisctl bao --address https://openbao.example:8200 write -ns=staging -field=signed_key \
    ssh/sign/user valid_principals=ci public_key=@id_ci.pub > id_ci-cert.pub
scp -i id_ci ci@build-worker.example:backup.tar.gz .    # ssh reads id_ci-cert.pub beside id_ci
```

The role's `ttl`, `max_ttl` and `allowed_users` set lifetime and OS accounts, not a flag ([manager side](openbao.md#2-configure-the-manager)).

A job that runs only on GitHub Actions can use `opkssh login github`, with `https://token.actions.githubusercontent.com github oidc` in `/etc/opk/providers` and `auth_id` lines keyed on `repo:<owner>/<repo>:ref:<ref>`. Use OpenBAO when in-cluster runners need the same access.

## Hosts: host certificates from OpenBAO's SSH CA

See [SSH hosts](ssh-hosts.md).

## Verify

```sh
opkssh login
ssh ops@build-worker.example
```

Read a machine certificate with `ssh-keygen -L -f id_ci-cert.pub`.

## Who owns what

| Piece | Owner |
|---|---|
| the issuer, the opkssh client row, every internal group | sluis |
| the SSH CA, its roles, host auth methods, the host renewal agent | the secret store's owners |
| opkssh and `sshd` wiring, `/etc/opk/*`, `HostCertificate`, `known_hosts` | each host's owner |

Decided in: [ADR 0011](../../../decisions/0011-ssh-people-opkssh-machines-and-hosts-openbao.md), [ADR 0015](../../../decisions/0015-a-per-audience-groups-delimiter-for-opkssh.md).
