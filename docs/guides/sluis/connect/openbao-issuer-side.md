# OpenBao: what the issuer provides

OpenBao trusts the issuer on two auth mounts per namespace, maps the `groups` claim onto its identity groups by name and signs the certificates `sluisctl bao` and `sluisctl pg`/`psql` request. The contract lives in [truvity/openbao docs/integrations/sluis.md](https://github.com/truvity/openbao/blob/master/docs/integrations/sluis.md). The installation steps are in [connect OpenBao](openbao.md).

A change to any item below breaks OpenBao sign-in.

## Before you start

- OpenBao can reach `/.well-known/openid-configuration` and the key set, both when configured and when keys rotate.

- A mount that pins `jwt_supported_algs` names the algorithm the signing key uses. ES384 is the default, RS256 applies to an RSA key or a pinned `signing_alg`.

- `iss` equals the issuer URL, so a trailing slash on one side refuses every login.

- Every token carries `sub` and `groups`, ID tokens included. No `groups` scope is needed.

## Steps

### 1. Declare two clients

```yaml
clients:
  openbao:
    kind: exchange
    ttl_cap: 15m
    requires: [all:openbao:operator, staging:ssh:user, staging:ssh:admin, staging:db:client, ci:release]
  openbao-ui:
    kind: confidential
    secret: openbao-ui-client
    redirects: [https://openbao.example/ui/vault/auth/oidc/oidc/callback]
    signed_out: [https://openbao.example/ui/]
    ttl_cap: 5m
    requires: [all:openbao:operator, staging:ssh:user, staging:ssh:admin, staging:db:client]
```

- `openbao` is the audience at the `jwt-roster` login: `sluisctl bao`, `sluisctl pg`/`psql`, `sluisctl token --audience openbao` and CI jobs. Keep its cap short.

- `openbao-ui` is the web UI sign-in. OpenBao redeems the code, so the secret must reach whatever applies OpenBao's configuration. One redirect serves every namespace.

- Keep the two `requires` lists equal, except for groups only jobs hold. Enforce it with a test in the repository that owns the policy.

- Every group OpenBao holds a policy for belongs in `requires`, per the [naming rule](../../../concepts/sluis/trust.md#naming). A missing group is refused at the exchange and `sluisctl` exits `4`.

### 2. Check sluisctl

- `bao` logs in on `jwt-roster` as role `roster` with a token for `openbao`. `--mount`, `--login-role` and `--audience` override them.

- `pg`/`psql` share that login and make one `pki/sign/<role>` call over a CSR for an ECDSA P-384 key. No TTL is sent.

- Exit codes: `4` refused, `5` unreachable, `2` usage ([reference](../../../reference/sluis/sluisctl.md#exit-codes)).

### 3. Check CI

A job with `id-token: write` exchanges its GitHub token for `openbao`, and the `ci` rules decide its groups ([GitHub Actions](github-actions.md)). The GitHub Action writes kubeconfigs and AWS profiles only. For OpenBao a job runs `sluisctl token --audience openbao`, or `sluisctl bao`/`pg`/`psql`.

The installation applies policy changes ([install](../operate/install-with-helm.md)).

## Decided in

- [ADR 0013](../../../decisions/0013-openbao-access-through-the-bao-cli.md)
