---
title: Install file
description: "The install-file schema v1 of the first working version: where the file lives, every key it has, the checks on it, and the example file for local."
status: target
sidebar:
  order: 3
---

The install file is the installation: one file says which runtime, boundaries, drivers, upstreams, clients and policy one deployment has ([ADR 0018](/decisions/0018-runtimes-installations-and-delivery-order/#runtime-kinds)). This page fixes its shape for the first working version, schema v1. How configuration works in general (layers, origins, live reload, `explain`) is on [Configuration](/concepts/configuration/); the rules are [ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/).

Schema v1 has only the keys that code of the first working version reads ([principle 8](/principles/#8-nothing-without-a-consumer)). Keys for the boards, Lambda and Kubernetes installations, integrations, quota and the rest are added at the parity milestone, each by the pull request that reads it. Adding a key or a value is compatible; removing or changing one is a new schema version beside v1 ([ADR 0019](/decisions/0019-release-train-packaging-and-distribution/#compatibility-between-releases)).

## Where it lives

- **On bare-metal** it is `/etc/sluis/install.yaml`, owned by root and readable by the sluis users. The systemd generator reads it and creates the actor instances from the template units.
- **In the local installation** the same file is mounted read-only at the same path into every container of the Compose project.
- **`--config FILE`** points any `sluis` command at another file ([Commands](/reference/cli/#global-flags)).

A path inside the file (a policy file, the root list) is relative to the directory of the install file.

## Format

- **YAML 1.2 that is plain JSON data:** mappings, sequences, strings, numbers, booleans. Tags, anchors, aliases and merge keys are refused, and so is a duplicate key, so every YAML reader sees the same document.
- **The schema is JSON Schema draft 2020-12** with the `$id` `https://sluis.id/schema/install/v1/install.schema.json`, generated from the Pkl sources in `schema/` and embedded in every binary that reads the file ([ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#pkl-and-json-schema)).
- **Strict:** `unevaluatedProperties: false` on every object, so an unknown key is an error that names its path; format assertions are on. An error never shows the value.
- **`$schema`** may name the `$id` above, for editors. A file that names another `$id` is refused.
- **Names** of boundaries, upstreams and clients are lower-case kebab-case, 1 to 32 characters, starting with a letter: they appear in unit names, sockets and record keys (`<actor>@<boundary>`).
- **Durations** are strings such as `90s`, `15m`, `12h` or `30d`.
- **A secret is a reference, never a value:** `{ credential: NAME }` names a read-only source. On bare-metal it is the systemd credential `NAME` of the unit; in Compose it is the file `/run/secrets/NAME`. The writable secret stores and the other sources come with the `secrets` port ([#1014](https://github.com/truvity/sluis/issues/1014)).

## Top-level keys

| Key | Required | Read by | What it says |
|---|---|---|---|
| `$schema` | no | every binary | The schema `$id`, for editors |
| `runtime` | yes | every binary, `sluis config check` | `bare-metal`, `serverless` or `kubernetes`. The first working version accepts `bare-metal` |
| `environment` | yes | every binary; the `software` keys driver | `development`, `staging` or `production` ([floors in code](/decisions/0012-cryptography-and-authority-keys/#floors-in-code)) |
| `nodes` | yes | `sluis config check` | The number of nodes; the first working version accepts `1` |
| `front` | yes | `entry`, `session`, `console`, `signer` | The front installation's group issuer and the passkey relying party ([below](#front)) |
| `boundaries` | yes | every actor; the generators | The boundaries this file holds ([below](#boundaries)) |
| `drivers` | yes | every actor's port library; `sluis config check` | One driver per port ([below](#drivers)) |
| `actors` | no | each actor | Tunables per actor ([below](#actors)) |
| `upstreams` | yes | `session`, the upstream integration | The upstream identity providers people sign in through ([below](#upstreams)) |
| `clients` | yes | `session`, `exchange`, `signer` | The clients of the group issuer ([below](#clients)) |
| `policy` | yes | `policy` | The Cedar policy files ([below](#policy)) |
| `trust` | yes | `sluis key install`, `sluis key request`, the actors that check a chain | The root list ([below](#trust)) |

### `front`

| Field | Required | Meaning |
|---|---|---|
| `host` | yes | The host of the group issuer. The issuer URL is `https://<host>`, with no path ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#issuers)) |
| `rp_id` | yes | The passkey relying-party ID: the registrable parent domain of the front host and every boundary host ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#passkeys)) |
| `token_ttl` | no | The default lifetime of the access and ID tokens of the group issuer. Default and maximum `1h`; it can only be shortened ([ADR 0028](/decisions/0028-grants-come-from-a-declared-vocabulary-and-a-token-carries-fixed-claims-for-one-audience/#lifetimes)) |

### `boundaries`

A mapping from the boundary's name to its declaration. Every boundary below L4 is declared in the install file of the installation that holds it; an L4 boundary has an install file of its own ([ADR 0005](/decisions/0005-boundaries-and-the-isolation-ladder/#federation-between-boundaries)).

| Field | Required | Meaning |
|---|---|---|
| `kind` | yes | `environment` or `company` ([ADR 0005](/decisions/0005-boundaries-and-the-isolation-ladder/#boundary-kinds)). The product kind comes with the CIAM upstream milestone |
| `level` | yes | `L0` to `L3`; L4 is a separate install file ([The isolation ladder](/decisions/0005-boundaries-and-the-isolation-ladder/#the-isolation-ladder)) |
| `host` | environment boundaries only | The host of the boundary's issuer, a sibling of the front host under `rp_id`. A company boundary has no issuer |
| `uid_block` | yes | An integer from 0 to 54, unique in the file. In containers the uid of `<actor>@<boundary>` is the actor's pinned base uid plus 1000 times this number ([The uid table](/architecture/manifest/#the-uid-table)). Never reused after a boundary is removed |
| `token_ttl` | no | The default lifetime of the access and ID tokens of this boundary's issuer, as for [`front`](#front) |
| `clients` | no | The clients of this boundary's issuer, in the shape of the top-level [`clients`](#clients) |

### `drivers`

One driver per port. The values the first working version accepts are the local installation's ([ADR 0018](/decisions/0018-runtimes-installations-and-delivery-order/#installations)); the drivers of the other tested installations are added with them.

| Field | Values in v1 |
|---|---|
| `keys` | `software` |
| `state` | `embedded` |
| `lease` | `local-lock` |
| `blob` | `fs` |
| `transport` | `spool`, `direct` |
| `index` | `sqlite` |

`sluis config check` refuses a driver that does not fit the runtime or the node count, and the `software` keys driver refuses to start when `environment` is `production` ([Ports and drivers](/concepts/drivers/#how-sluis-config-check-validates-an-installation)). The on-disk layout of these drivers is on [Storage](/reference/storage/#the-local-drivers-on-disk). The audit log is in the first working version ([ADR 0018](/decisions/0018-runtimes-installations-and-delivery-order/#the-local-installation)), so the `transport`, `blob` and `index` drivers serve it; the keys for its retention and its witness join schema v1 with the pull request that reads them.

### `actors`

A mapping from an actor's name, as in the [actor registry](/reference/actors/), to its tunables. A tunable may also come from an environment override ([Layers](/concepts/configuration/#layers)).

| Field | Default | Meaning |
|---|---|---|
| `log_level` | `info` | `debug`, `info`, `warn` or `error`, for the actor's log on standard error |

Telemetry is not configured here: it is the standard `OTEL_*` environment variables only ([ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#pkl-and-json-schema)).

### `upstreams`

A mapping from an upstream's name to its declaration. Each upstream is one integration process, `<vendor>[-<capability>]@<tenant>` ([ADR 0003](/decisions/0003-core-actors-with-one-responsibility-each/#names)).

| Field | Required | Meaning |
|---|---|---|
| `kind` | yes | The integration that serves it. The first working version has `oidc`, the generic OpenID Connect upstream, sign-in only, with subjects assigned at the first sign-in ([#1106](https://github.com/truvity/sluis/issues/1106), [ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#subjects)) |
| `issuer` | yes | The upstream's issuer URL |
| `subject_claim` | no | The ID-token claim whose value, with the issuer, identifies the person: default `sub`. Microsoft Entra needs `oid`, because its `sub` differs per application. A mutable claim (`email`, `preferred_username`, `upn`, `name`) is refused, and so is a change while the upstream has links ([ADR 0027](/decisions/0027-a-person-is-tied-to-an-upstream-account-by-issuer-and-subject-claim/#linking-an-upstream-account)) |
| `client_id` | yes | sluis's client ID at the upstream |
| `client_secret` | yes | A reference: `{ credential: NAME }` |

### `clients`

A mapping from a client ID to its declaration, for the group issuer; `boundaries.<name>.clients` has the same shape for a boundary's issuer. Client IDs are readable, unique per issuer and the same in every environment ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#audiences)). Clients are declared only: authority is never operator-set ([ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#origins)). The fields are v1's client model, cut to what the first working version reads; the rest comes at parity ([#789](https://github.com/truvity/sluis/issues/789)).

| Field | Required | Meaning |
|---|---|---|
| `kind` | yes | `public` (PKCE, no secret), `confidential` (a secret) or `exchange` (only an audience of token exchange, with no redirect, such as a boundary's client ID at the group issuer) |
| `redirects` | `public` and `confidential` | Exact redirect URIs. `https` only, except `http://127.0.0.1` and `http://[::1]` with any port for a public native client (RFC 8252) |
| `secret` | `confidential` only | A reference: `{ credential: NAME }`. Generated client secrets come with [#790](https://github.com/truvity/sluis/issues/790) |
| `requires` | no | The grants of which a subject needs at least one to get a token for this client, as in v1 |
| `ttl_cap` | no | The longest lifetime of a token for this client |

### `policy`

| Field | Required | Meaning |
|---|---|---|
| `files` | yes | The Cedar policy files, validated against the Cedar schema at load ([ADR 0009](/decisions/0009-policy-is-cedar-in-a-rust-actor/)) |
| `vocabulary` | yes | The grant vocabulary: the scopes, the things with their roles and what each role implies. Every grant name in a policy file, a client's `requires` and a binding is checked against it ([ADR 0028](/decisions/0028-grants-come-from-a-declared-vocabulary-and-a-token-carries-fixed-claims-for-one-audience/#grant-names-and-the-vocabulary)) |

### `trust`

| Field | Required | Meaning |
|---|---|---|
| `root_list` | yes | The signed root list that ritueel produced, `root-list.json` with its `.jws` beside it. sluis takes its anchors from it, and `sluis key request` reads the active primaries from it ([ADR 0013](/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api/#root-pins-and-the-root-list)). At parity `sluis trust sync` keeps it current |

## Which keys a file enables

A key purpose that an installation does not enable gets no key ([ADR 0012](/decisions/0012-cryptography-and-authority-keys/#the-card-slot-map)). Schema v1 enables, per environment boundary, the X.509 intermediate, the token-signing key and the sealing key. The other purposes are enabled by the keys that use them, which come later: the SSH CA keys with `ssh-ca`, the release-signing subkey with the signing API ([#890](https://github.com/truvity/sluis/issues/890)), the enrolment key with host enrolment. The card slot map override is read by the `yubikey-piv` driver and arrives with it.

## What `sluis config check` checks

Beyond the schema:

- every boundary host and the front host are siblings under `rp_id`, each host is one origin, and no two issuers share a host;
- a boundary of kind `company` has no `host`, and every `environment` boundary has one;
- every `credential` reference names a source that exists (offline: the file is present; the value is never read by `check`);
- every client's `requires` names grants that the vocabulary declares, no grant name outside the vocabulary appears in a policy file, and every policy file parses and validates against the Cedar schema;
- every `uid_block` is unique, and every `subject_claim` is a claim that does not change for a person;
- the drivers fit the runtime and the node count, every enabled key purpose has a key, and the root list verifies and is not older than the one sluis holds.

## The example file for local

The local installation ships as an example install file, [`tests/installations/local/install.yaml`](https://github.com/truvity/sluis/blob/main/tests/installations/local/install.yaml): the integration tests run it, and the first working version is done when it starts ([Roadmap](/roadmap/)). This copy is checked against that file (`just check-examples`).

<!-- example: tests/installations/local/install.yaml -->
```yaml
# The local installation: Docker Compose on one workstation, software keys, one node
# (ADR 0018). Install-file schema v1: https://sluis.id/reference/install-file/
$schema: https://sluis.id/schema/install/v1/install.schema.json
runtime: bare-metal
environment: development
nodes: 1

front:
  host: sluis.local.test
  rp_id: local.test

boundaries:
  dev:
    kind: environment
    level: L2
    host: sluis.dev.local.test
    uid_block: 0
    clients:
      example-app:
        kind: confidential
        redirects: [https://app.local.test/callback]
        secret: { credential: client-example-app }
        requires: [dev:example-app:user]

drivers:
  keys: software
  state: embedded
  lease: local-lock
  blob: fs
  transport: spool
  index: sqlite

upstreams:
  workforce:
    kind: oidc
    issuer: https://idp.local.test
    client_id: sluis-local
    subject_claim: sub              # the default; Microsoft Entra needs oid
    client_secret: { credential: upstream-workforce }

clients:
  sluis-cli:
    kind: public
    redirects: [http://127.0.0.1/callback]
  dev:
    kind: exchange

policy:
  files: [policy/local.cedar]   # written with the Cedar schema (#1008)
  vocabulary: policy/vocabulary.yaml

trust:
  root_list: trust/root-list.json   # from ritueel's software root, at first start
```

## Open items

:::note[Proposed]
**The hosts and the TLS of the local installation.** The example uses `local.test` names, outside the sluis host names, as a test installation must be ([ADR 0005](/decisions/0005-boundaries-and-the-isolation-ladder/#shared-parts-and-accepted-risks)). The forwarder that publishes `entry`'s socket, the certificate it serves, and whether a browser's virtual authenticator accepts `local.test` as the relying-party ID are measured with the local topology before these names are final. Tracked in [#877](https://github.com/truvity/sluis/issues/877).
:::

:::note[Proposed]
**The Cedar files' shape.** `policy.files` names Cedar files; the Cedar schema, the entity model and how a client and a grant are written in Cedar are the planned record of [#1008](https://github.com/truvity/sluis/issues/1008).
:::
