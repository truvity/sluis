---
title: Configuration
description: "How sluis is configured: an installation, a Pkl-defined schema, JSON Schema validation, layers, operator-set records, live reload and explain."
status: target
sidebar:
  order: 7
---

sluis is configured by files that an operator reads and edits, and by the records an operator sets on the console. The design makes four promises about them: a file that is wrong is refused before anything takes effect; a field that does not exist is an error, not silently ignored; any value can be traced to the origin and the layer that set it; and an operational switch can never change who may do what. The rules are [ADR 0015](/decisions/0015-configuration-is-defined-once-in-pkl-and-shipped-as-json-schema/) and [ADR 0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/).

## Pkl at build time, generated artifacts at runtime

The **schema** of the configuration is written in Pkl, and only at build time. Pkl generates:

- a **JSON Schema** (draft 2020-12), which is part of the public API;
- the **Go structs** the actors load into;
- the **Helm values schemas**;
- the **reference documentation**.

The runtime uses only these generated artifacts. **There is no Pkl at runtime.** The Pkl command-line tool is about 95 MiB and pkl-go spawns it as a child process, which is too heavy for a small single-board computer or a Lambda function. Where Pkl lacks a generator, for example Pkl to JSON Schema, a separate open-source tool fills the gap.

## Validation

The same schema is checked by a validator in each language sluis is written in:

| Language | Validator |
|---|---|
| Go | `santhosh-tekuri/jsonschema/v6` |
| Rust | the `jsonschema` crate |
| TypeScript | `ajv` |

One shared set of **conformance vectors** runs against all three, so the validators agree on what is valid.

**Strictness comes from the schema itself.** The schema sets `unevaluatedProperties: false`, so an unknown key is an error that names the field, and turns format assertions on, so a field declared as a URL or a duration is checked as one. A retired option that is still set in a declared file is refused, not ignored. A record that sluis wrote itself, such as an operator-set record, is read with a retired field ignored, and the field is never written again.

The schema also carries, for every record kind, its **origin** and, for every field, its **sensitivity** (see [Data](/concepts/data/#origins-and-sensitivity)). CI checks the annotations, and the backup list, the telemetry allowlist and the audit redaction are generated from them.

## Declared and operator-set

Every record kind in sluis has one origin ([Data](/concepts/data/#origins-and-sensitivity)). Two of the five are configuration, because they are an operator's intent:

| Origin | What it is | Written by |
|---|---|---|
| **declared** | Reviewed files or infrastructure as code, delivered read-only | Nobody at run time; the operator, through review and deployment |
| **operator-set** | A lasting choice a person makes on the console, the CLI or the API, such as the chat channels an integration manages or a runtime switch. An overlay on declared, allowed only for the record kinds that accept it | The actor that owns the record kind, which validates the write; every change is audited under the person's name |

The **effective configuration** is declared overlaid with operator-set. Learned facts and issued records are data that the configuration refers to, not configuration.

- **Declared locks.** A field that declared sets cannot be changed on the console; the console shows it as locked.
- **Operator-set fills what declared leaves open.** The order is declared, then operator-set, then the schema default.
- **Tighten-only switches** (recovery off, a reconciler paused, a lower quota limit) may be moved by an operator in their safe direction even when declared sets them. Moving one back is a declared change that names a new generation.
- **An object defined in both places is held**: nothing changes on it until one side is removed, or a declared fragment adopts the operator-set record with `adopts: <kind>/<id>@<revision>`.
- **Authority is declared only.** The one exception is Cedar policies linked from declared templates within a declared delegation policy, which the console may create and remove ([ADR 0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/)).

A secret in configuration is a reference such as `cred:sluis-int-github.app-key`, resolved at run time through the `secrets` port (see [Secrets and export](/concepts/secrets/)). The value never appears in a file, in `explain` output, in logs or in a crash dump. A secret put at run time with `sluis secrets put` or on the console is an operator-set secret in sluis's own store.

## What is configured: an installation

The thing an operator configures is an **installation**: one concrete deployment, made of

- a **runtime**, the runtime kind: bare-metal, serverless or Kubernetes (see [Runtimes](/runtimes/));
- the **nodes**: how many there are, which is 1, N, or elastic on serverless;
- the **dependencies** that are available: an AWS account, a Cloudflare account, a YubiKey, a NATS service;
- the **drivers**: one driver per port (see [Ports and drivers](/concepts/drivers/)).

An example is "bare-metal, 3 nodes, an AWS account". Four installations are tested and named: [local, boards, lambda and kubernetes](/runtimes/installations/). They ship as **example configuration files** used by the documentation, the tutorials and the test matrix; an operator copies one as a starting point. "Installation" is not a configuration key: the install file *is* the installation. Any other combination that the rules allow is a possible installation, and it is not tested. Nodes and dependencies are properties of the installation, not of the runtime.

The word **environment** is kept for what differs between development, staging and production.

**One install file per installation, also in a federation.** A sluis that spans several [boundaries](/runtimes/boundaries/) is a front installation plus one boundary installation for each L4 boundary, each with its own install file, joined by declared trust. A boundary below L4 (a company, or an environment at L2 or L3) is declared in the install file of the installation that holds it, and every actor instance it gets is named `<actor>@<boundary>` ([Naming](/concepts/actors/#naming)). On bare-metal a systemd generator reads `/etc/sluis/install.yaml` and creates those instances from the template units; on Kubernetes the chart's presets do the same. Nothing generates packages per installation.

## Layers

Declared values come from layers. A later layer overrides an earlier one. Layers apply inside declared configuration only; operator-set is an origin, not a layer.

1. **defaults**, built into the schema;
2. **runtime**, the defaults of the runtime kind (bare-metal, serverless or Kubernetes);
3. **install**, the file the operator edits (often started from one of the example files);
4. **environment overrides**, for the values that differ between development, staging and production, including the few fields marked as tunable that a process environment variable may set.

This order replaces the older "shape profile" layer with the runtime layer; the tested installations are example files, not a layer.

## Live reload

Declared configuration may **live-reload as whole versioned snapshots**, through any channel that delivers it: a file, a ConfigMap, a Lambda layer or AWS AppConfig. A new snapshot is validated as `sluis config check` validates it, swapped atomically and audited with its digest. A snapshot that fails validation is refused, and the version in force stays. No part of a version ever changes: an instance moves from one whole version to the next.

Operator-set values are read live through the `state` port, with a short cache whose lifetime is part of the field's schema.

## Checking and explaining

```bash
sluis config check
sluis config explain
```

`sluis config check` validates the configuration against the schema, and then validates the installation against the requirements that its drivers declare, and refuses a combination that cannot work, such as a Raft-based state store on serverless or a YubiKey on serverless. How the second check works is on [Ports and drivers](/concepts/drivers/#how-sluis-config-check-validates-an-installation). `sluis config explain` prints each field's effective value, its origin and the layer that set it.

Both are **offline by default**, so CI runs them with no call to sluis. With `--live` they also read the operator-set records through the console API, with an operator's credentials: `check --live` lists the objects that are held and the records a new declared fragment would lock or hold, and `explain --live` shows, for an operator-set value, the record id, its revision, who set it and when.

## Switches and authority

**Operational switches stay separate from authority.** Authority says who may do what: policy, grants, the clients that may ask for tokens. A switch changes how sluis runs, and there are two classes of them:

- **Tunables** (a log level, a timeout, the choice of a driver) are declared, and an environment override may reach them.
- **Runtime switches** (recovery off, a paused reconciler, an acknowledged breaker) are operator-set, read live, and tighten-only where they touch safety.

Neither reaches authority. v1 mixed them in one policy file.

## Examples

The following are examples to show the form. Field names are illustrative.

A fragment of a generated JSON Schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "unevaluatedProperties": false,
  "properties": {
    "runtime": { "enum": ["bare-metal", "serverless", "kubernetes"] },
    "nodes": { "type": "integer", "minimum": 1 },
    "public_url": { "type": "string", "format": "uri" },
    "drivers": {
      "type": "object",
      "unevaluatedProperties": false,
      "properties": {
        "yubikey-piv": { "$ref": "#/$defs/driverShared" },
        "aws": { "$ref": "#/$defs/driverPerRole" }
      }
    }
  },
  "required": ["runtime", "nodes", "public_url"]
}
```

An install file for the boards installation, `/etc/sluis/install.yaml`:

```yaml
runtime: bare-metal
nodes: 3
public_url: https://sluis.example.com

dependencies:
  yubikey: { per_node: true }
  cloudflare: { account: example }

drivers:
  yubikey-piv: { instance: shared }

resources:
  keys.oidc-signing: { driver: yubikey-piv, key: oidc }
  keys.audit-log:    { driver: yubikey-piv, key: log }

integrations:
  github:
    actor: facts
    app_key: cred:sluis-int-github.app-key   # a reference, never the key
```

And what `sluis config explain` prints for it:

```text
runtime                       = bare-metal            install   /etc/sluis/install.yaml:1
public_url                    = https://sluis.example.com  install   /etc/sluis/install.yaml:3
drivers.yubikey-piv.instance  = shared                install   /etc/sluis/install.yaml:10
log.level                     = debug                 environment override   SLUIS_LOG_LEVEL (tunable)
integrations.github.app_key   = cred:sluis-int-github.app-key   install   (reference, not resolved)
```

With `--live`, an operator-set value shows where it came from (illustrative output; the record ids are examples):

```text
slack.acme.channels.ops       = #ops                  operator-set   record slack-channel/ops rev 4, set by an operator on 2026-10-30
recovery.disabled_generations = [3]                   operator-set   tighten-only, record recovery-switch rev 2
```

For where each file lives on a node, see [Drivers](/concepts/drivers/).
