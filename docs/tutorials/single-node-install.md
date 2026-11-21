---
title: "Tutorial: a single-node local installation"
description: "Run sluis on one workstation with `just local-up`: Docker Compose, one rqlite node and software keys; sign in, verify a token and find the audit record."
status: partial
sidebar:
  order: 0
---

:::caution
This tutorial follows the recipes of a checkout of `truvity/sluis`, as of 2026-11-18, after `v2.0.0-alpha.1`. A step that names a recipe runs today. A step that says "planned" does not exist yet, and links its issue. Outputs are examples ([examples are illustrative](/integrations/#examples-are-illustrative)), not captured logs.
:::

You run the [local installation](/runtimes/#local) on one workstation: Docker Compose, one container per actor and per driver, an rqlite engine of one node, and software keys. It is the installation for development, continuous integration and the integration tests ([0011](/decisions/0011-runtimes-and-installations/#the-local-installation)). The entry point is one recipe, `just local-up`, run in the project's devbox. You finish with a running installation, a sign-in through a real identity provider (Keycloak, run beside it), a token you have verified yourself, and the audit records of your sign-in.

It is the core installation: the key set carries no `x5c` chain, because the core leaves the token certificate off ([ADR 0006](/decisions/0006-keys-and-sealing/#the-token-certificate)). [Step 7](#7-the-certificate-preset-is-planned) says what that means for `local-up` today.

The same path is proven in CI on every pull request: `local-smoke` and `local-person-smoke` run the sign-in, the token and the audit checks of steps 6, 8 and 9 against a fresh installation, so a step that stops working fails the build ([The local installation](/runtimes/local/#recipes)).

## 1. Prerequisites

- A Linux workstation with Docker, devbox and `git`. Every command below runs as `devbox run -- just <recipe>`, from the root of the checkout. The first run installs the toolchain and builds every executable and image, which takes a while.
- A checkout of `truvity/sluis` on `main` (the v2 line; a fresh clone starts on `master`, which is v1) at the release tag you want. The local installation's files are under `tests/installations/local/` (the bootstrap file, the config documents, the policy and the identity provider's realm) and `deploy/local/` (the Compose project, generated from the registries, and the scripts) ([The local installation](/runtimes/local/#topology)).
- A free `/24` subnet for the project's network, and the address of the identity provider that this subnet reaches. The steps below use one example subnet; use another if it clashes with a route of yours ([The local installation](/runtimes/local/#recipes)).
- No hosts file, no browser and no certificate import are needed for the recipes below. They are needed only to sign in with your own browser and CLI ([step 5](#5-trust-the-harness-ca)).

## 2. Read the bootstrap file

store@ reads one local file at start, the [bootstrap file](/reference/bootstrap-file/): the boundary, a driver socket for each of its four ports, and the public keys of the publishers that sign config. Everything else is a signed config document. The local one, abridged:

```yaml
# Abridged from tests/installations/local/bootstrap.yaml
boundary: dev
start_timeout: 30s

config_source: { driver: file, socket: /run/sluis/store/dev/config-source.sock }
secret_source: { driver: credential, socket: /run/sluis/store/dev/secret-source.sock }
durable: { driver: rqlite, socket: /run/sluis/store/dev/durable.sock }
temp: { driver: rqlite, socket: /run/sluis/store/dev/temp.sock }

publishers:
  - kid: publisher-1
    public_key: |
      -----BEGIN PUBLIC KEY-----
      ...
    spki_sha384: "d4c36e17…afcbb4"
```

- **One rqlite node serves both engine ports,** the engine of the boards with a cluster of one ([0005](/decisions/0005-the-store/#engines)).
- **The publisher pin is the trust anchor of config.** store@ verifies every config document against a key in this list before it parses it, and refuses any other ([Verifying a document](/reference/signed-config/#verifying-a-document)). The committed file carries a test key that guards nothing. `local-up` makes a publisher key for the run, writes its pin into the copy of this file that the run uses, and signs the config documents with it ([The first start](/runtimes/local/#the-first-start)).
- **A duration is written with a unit,** here and in every config document: one whole number and one of `s`, `m`, `h` or `d`, such as `30s` or `15m` for fifteen minutes, never an ISO 8601 string such as `PT15M` ([0004](/decisions/0004-the-data-model/#schemas-are-protobuf), [Durations](/reference/bootstrap-file/#durations)).

## 3. Start the identity provider and the project

Sign-in goes through an upstream identity provider, never through accounts in sluis. `just local-idp` starts a Keycloak container with the reviewed realm of the tests (the user `alice`, with a password and a one-time password) and writes a file of random secrets for the run. Then `just local-up` builds the executables and the images, makes the state of the run below `.cache/local`, and starts the project. It returns when every service that has a health check is healthy.

```sh
# Example: the subnet and its first address are yours to choose
export SLUIS_LOCAL_SUBNET=10.99.8.0/24
export SLUIS_UPSTREAM_TARGET=10.99.8.1:8180
export SLUIS_KEYCLOAK_SECRETS="$PWD/.cache/idp.env"

devbox run -- just local-idp .cache/idp.env
devbox run -- just local-up
devbox run -- just local-status
```

- **`SLUIS_UPSTREAM_TARGET`** is the `host:port` of the identity provider as the project's network reaches it: the first address of the subnet (the network's gateway) and port 8180, where `local-idp` publishes Keycloak. The repository names no real value.
- **`SLUIS_KEYCLOAK_SECRETS`** names the file that `local-idp` writes. The values in it are random and are never printed.
- **The first start has two phases.** key@ makes its software wrapping key, `local-up` reads the key's thumbprint, signs it into the `KeyPlan` as the only recipient, and then starts every other service ([The first start](/runtimes/local/#the-first-start)). There is no one-shot `setup` service and no Compose file in `tests/installations/local/`: the Compose project is generated into `deploy/local/compose.yaml` by `just compose-gen`, and a test fails while the committed file differs from what the registries make.
- **`local-status`** lists each service with its user id, its state and its health.

The keys are software keys. key@'s `software` driver refuses to start in a production boundary and warns in a staging one, so these keys guard development data only ([0006](/decisions/0006-keys-and-sealing/#floors-in-code)).

To run the images of a release instead of building them, set `SLUIS_LOCAL_IMAGES=release` and the release's version and catalogue ([Images](/runtimes/local/#images)).

## 4. Check the configuration

`sluis config check` validates the bootstrap file and the config documents the way store@ does at start: the schema and protovalidate, every signature against the pinned publisher keys, the sequence, and the whole set against the registries ([`sluis config check`](/reference/cli/#sluis-config-check-and-explain)). The smoke of the command-line tool runs it, with `doctor`, `key status` and the other commands of the first-version table, against the running installation:

```sh
devbox run -- just local-cli-smoke
```

Its `config check` step prints lines like these:

```text
# Example output
ok    bootstrap file: schema v1, publisher-1 matches its pin
ok    config documents: every signature verifies against a pinned publisher key
ok    snapshot: the set validates against the kind registry
ok    drivers fit the runtime (bare-metal, 1 node)
warn  audit: no witness; with no witness a compromised writer can get a rewritten checkpoint signed
```

The warning is expected: the local installation has no witness ([0011](/decisions/0011-runtimes-and-installations/#the-local-installation)). A `fail` line exits 3.

## 5. Trust the harness CA

`local-up` made a certificate authority for the run, the harness CA, and a leaf for the forwarder that ends TLS. The CA's certificate is `.cache/local/ca/ca.pem`. The recipes trust it through `SSL_CERT_FILE`, so they need nothing from you. Never import it into the system trust store. `just local-down` deletes it.

To reach the installation from the workstation by hand, give `curl` the CA and the name mapping, with the project on its default port 8443:

```sh
# Example
curl --cacert .cache/local/ca/ca.pem --resolve sluis.dev.local.test:8443:127.0.0.1 \
  https://sluis.dev.local.test:8443/.well-known/openid-configuration
```

To sign in with your own browser and your own CLI, set `SLUIS_LOCAL_PORT=443` before `local-up`, add four lines to the hosts file, and import `ca.pem` into a browser profile made for this project ([Host names](/runtimes/local/#host-names), [TLS](/runtimes/local/#tls)):

```text
127.0.0.1 sluis.local.test
127.0.0.1 sluis.dev.local.test
127.0.0.1 idp.local.test
127.0.0.1 app.local.test
```

## 6. Sign in

1. The realm sends `amr` with `mfa` after the one-time password, so the local policy's multi-factor rule passes ([Sign-in](/runtimes/local/#sign-in)). The local policy binds `alice` to the grant name `dev:example-app:user` ([A worked example](/reference/policy/#a-worked-example)).
2. Sign in as `alice`:

   ```sh
   devbox run -- just local-login-headless
   ```

   The recipe runs the real `sluis login --issuer https://sluis.dev.local.test`. A harness fills in Keycloak's form and computes the one-time code from the user's secret, where a browser and an authenticator would. The CLI reaches the forwarder through a small proxy of the test process, so no hosts file is needed.

```text
# Example output
signed in to https://sluis.dev.local.test
subject  pm4x7q2wz5c3nbr6dy7vjk5tha
```

The subject is an opaque random id of 26 base32 characters that sluis assigned at the first sign-in, the same for every client and never your e-mail address ([0017](/decisions/0017-identity-and-sign-in/#subjects)). `sluis login` then exchanges your token silently at the boundary's own token endpoint ([`sluis login`](/reference/cli/#sluis-login-and-sluis-token)). With your own browser and CLI ([step 5](#5-trust-the-harness-ca)) you run `SSL_CERT_FILE=.cache/local/ca/ca.pem sluis login --issuer https://sluis.dev.local.test` yourself; `sluis` is the binary that `local-up` built at `dist/local/bin/cli/sluis`.

## 7. The certificate preset is planned

The core installation needs no intermediate and no test certifier, and its key set carries no `x5c`. A local installation that turns the token certificate on is an opt-in preset named `certificate`: it adds the intermediate to the `KeyPlan`, signs `token_certificate: true`, and ends with the hand-off of the intermediate (`sluis key request`, a throwaway root of the run certifies, `sluis key install`). Each key of the key set then carries an `x5c` chain to that root ([The local installation](/runtimes/local/#recipes)).

:::note[Proposed]
The preset is not built. Until it is ([#1895](https://github.com/truvity/sluis/issues/1895)), `local-up` always runs the hand-off and turns the certificate on, so a run today shows the `x509-intermediate` row in `sluis key status` and an `x5c` chain in the key set. Treat both as the preset's output, and not as something the core needs.
:::

## 8. Get a token and verify it

```sh
devbox run -- just local-person-smoke
```

The recipe is the proof of the person: it signs `alice` in, runs `sluis token --audience example-app`, which has `exchange` do the exchange of RFC 8693, prints the claims of the ID token and of the exchanged token, and checks both against `/jwks`. `example-app` asks for multi-factor authentication no older than 900 seconds, so a later run asks for a new sign-in ([A worked example](/reference/policy/#a-worked-example)).

A sluis token is a standard JWS. Any JOSE library verifies it against the key set of the boundary's issuer, which exporter@ publishes as a static document; sluis needs no client library. Save the key set that the discovery document points to, then verify a token you made with `sluis token` ([step 6](#6-sign-in)):

```python
# Example
import jwt  # pip install "pyjwt[crypto]"

issuer = "https://sluis.dev.local.test"
keys = jwt.PyJWKSet.from_json(open("jwks.json").read())  # saved from the jwks_uri of the discovery document

token = open("token.jwt").read().strip()
kid = jwt.get_unverified_header(token)["kid"]
key = next(k for k in keys.keys if k.key_id == kid)
claims = jwt.decode(token, key.key, algorithms=["ES384"], issuer=issuer, audience="example-app")
print(claims["sub"], claims["groups"])
```

ES384 is sluis's default; an audience whose client names `signing_alg: RS256` gets RS256 ID tokens, from a separate key ([0006](/decisions/0006-keys-and-sealing/#named-exceptions)). The issuer is the `dev` boundary's own, with its own keys: a token of `dev` is refused by a relying party that pins another boundary's issuer ([0017](/decisions/0017-identity-and-sign-in/#issuers)).

## 9. Find the audit record

Your sign-in and the token are audit records of the `dev` boundary's audit, written by the writer to its `fs` blob and indexed for search ([0011](/decisions/0011-runtimes-and-installations/#the-local-installation), [Audit](/concepts/audit/)). Each names you by a keyed pseudonym, not by subject or e-mail. The records appear a few seconds after the event: the writer seals every 5 seconds and the indexer follows.

```sh
devbox run -- just local-audit-search 'action:signer.sign since:2m'
```

A word like `key:value` is a filter (`action`, `status`, `actor`, `trace`, `record`, `project`, `tenant`, `since`), and any other word is free text. `sluis audit verify` checks the log itself, offline, from its files alone, and `local-smoke` and `local-person-smoke` run it ([`sluis audit verify`](/reference/cli/#sluis-audit-verify)). A `sluis audit` command that searches is planned ([Later commands](/reference/cli/#later-commands)); until it exists, search is the harness recipe above.

## 10. Stop, or start again

```sh
devbox run -- just local-down      # removes the containers, the volumes and the state of the run
docker rm -f sluis-idp             # removes the identity provider that local-idp started
```

`local-down` deletes the harness CA, the publisher key, the credentials, the signed config and the data. The next `local-up` is a first start with a new CA, and a browser profile that trusted the old one trusts nothing that exists any more.

## Next steps

- What you ran, in detail: [The local installation](/runtimes/local/), the [bootstrap file](/reference/bootstrap-file/) and [signed config](/reference/signed-config/).
- What runs inside: the [families](/concepts/families/), the [store](/concepts/data/#the-store) and [keys](/concepts/keys/).
- The other installations, which are part of the core and are being built: [boards](/runtimes/#boards), with hardware keys, on the [bare-metal](/runtimes/bare-metal/) runtime; [lambda](/runtimes/#lambda), on the [serverless](/runtimes/serverless/) runtime; and [kubernetes](/runtimes/#kubernetes), on the [kubernetes](/runtimes/kubernetes/) runtime, from one Helm chart.
- The way back in when sign-in fails: [Break-glass recovery](/security/break-glass/).
