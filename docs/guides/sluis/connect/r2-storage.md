# Connect an R2 credential broker

A separate R2 credential broker trusts the issuer on an OIDC audience. It mints short-lived, prefix-scoped object-storage credentials. `sluisctl r2 [flags] [-- <args…>]` authenticates and runs the real `r2broker` binary ([reference](../../../reference/sluis/sluisctl-wrappers.md#r2-authenticate-then-run-the-real-r2broker-cli-unchanged)).

sluis decides no bucket, prefix or permission. The broker maps a token's group to a grant. Use this page only for a store with no OIDC federation of its own. For S3, use [`sluisctl aws`](aws-account.md).

## Before you start

- `r2broker` is on `PATH`.

- The broker's own config maps your groups to a bucket, prefixes and a permission.

- `sluisctl r2` hands the bearer token to `r2broker` as `R2BROKER_TOKEN`, never on the command line.

## Steps

### 1. Declare the client

An `exchange` client for the broker audience, gated by the groups the broker maps to a grant:

```yaml
groups:
  ci-cache-reader: { matchers: [{ github: { repository: example/app, event_name: pull_request } }] }
  ci-cache-writer: { matchers: [{ github: { repository: example/app, ref: refs/heads/main } }] }
clients:
  r2-broker: { kind: exchange, requires: [ci-cache-reader, ci-cache-writer] }
```

Render the policy and read the client. Roll back by removing the row.

### 2. Add a profile

`r2broker` speaks the AWS `credential_process` protocol version 1, so any S3-compatible tool that reads an AWS profile works:

```ini
[profile ci-cache]
credential_process = sluisctl r2 --service-url https://r2-broker.example.com -- \
    credentials --bucket example-bucket --prefix nix/
```

`--service-url` or `$SLUISCTL_R2_SERVICE_URL` names the broker. `--audience` defaults to `r2-broker`; `$SLUISCTL_R2_AUDIENCE` overrides it. A standalone broker runs `r2broker credentials --config broker.yaml` directly, with no `sluisctl r2`.

Run any S3 command with `--profile ci-cache` and check that it gets credentials. Roll back by deleting the profile.

### 3. Use it in a job

The same line works in a GitHub Actions job with `id-token: write`. `sluisctl r2` exchanges the job's identity token ([in a job](../../../reference/sluis/sluisctl.md#in-a-job)).

## Decided in

- [ADR 0014](../../../decisions/0014-minting-third-party-credentials-only-where-membership-is-governed.md)
- [ADR 0013](../../../decisions/0013-openbao-access-through-the-bao-cli.md)
- [ADR 0008](../../../decisions/0008-credentials-only-where-we-govern-membership.md)
