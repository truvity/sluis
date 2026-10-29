---
title: Configuration
description: "How sluis is configured: an installation, Pkl-defined schema, JSON Schema validation at runtime, the layers, data origins and the explain command."
status: target
sidebar:
  order: 6
---

sluis is configured by files that an operator reads and edits. The design makes four promises about them: a file that is wrong is refused before anything starts; a field that does not exist is an error, not silently ignored; any value can be traced to the layer that set it; and an operational switch can never change who may do what.

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

**Strictness comes from the schema itself.** The schema sets `unevaluatedProperties: false`, so an unknown key is an error that names the field, and turns format assertions on, so a field declared as a URL or a duration is checked as one. A retired option that is still set is refused, not ignored.

## Data origins

Not all data in sluis's configuration comes from the same place, and the origin decides who may change it.

| Origin | What it is | Written by |
|---|---|---|
| Declared | Operator-written configuration | The operator, in files under version control |
| Learned | Facts sluis discovers and stores | sluis, as it runs |
| Operator secrets | Credentials | Referenced in configuration, **never inlined** |

A secret is a reference such as `cred:sluis-int-github.app-key`, resolved at runtime from the credential store the environment provides. The value never appears in a file, in `explain` output, in logs or in a crash dump.

## What is configured: an installation

The thing an operator configures is an **installation**: one concrete deployment, made of

- an **environment**, the runtime kind: bare-metal, serverless or Kubernetes (see [Environments](/environments/));
- the **nodes**: how many there are, which is 1, N, or elastic on serverless;
- the **dependencies** that are available: an AWS account, a Cloudflare account, a YubiKey, a NATS service;
- the **drivers**: one driver per port (see [Ports and drivers](/concepts/drivers/)).

An example is "bare-metal, 3 nodes, an AWS account". A named, tested installation is a [preset](/environments/presets/), used in the documentation, the tutorials and the test matrix. Nodes and dependencies are properties of the installation, not of the environment.

## Layers

Values come from layers. A later layer overrides an earlier one:

1. **defaults**, built into the schema;
2. **environment**, the defaults of the runtime kind (bare-metal, serverless or Kubernetes);
3. **preset**, for example T1 Single board or T5 AWS Lambda (on Kubernetes, T6, and for development a development preset, as proposed in [OD-25](/open-decisions/#od-25));
4. **install**, the file the operator edits;
5. **stage overrides**, for the values that differ between a development, a staging and a production stage, including the few fields marked as tunable that a process environment variable may set.

:::note[Proposed]
This order replaces the older "shape profile" layer with two layers, the environment and the preset. The layering, and the stage overrides as its last layer, are proposed.
:::

## Checking and explaining

```bash
sluis config check
sluis config explain
```

`sluis config check` validates the configuration against the schema, and then validates the installation against the requirements that its drivers declare, and refuses a combination that cannot work, such as a Raft-based state store on serverless or a YubiKey on serverless. How the second check works is on [Ports and drivers](/concepts/drivers/#how-sluis-config-check-validates-an-installation). `sluis config explain` prints each field's effective value and the layer that set it.

## Switches and authority

**Operational switches stay separate from authority.** A switch changes how sluis runs: a log level, a timeout, the choice of a driver. Authority says who may do what: policy, grants, the clients that may ask for tokens. The two are different kinds of configuration with different owners and review, and a stage override can reach a switch but never authority. v1 mixed them in one policy file.

## Examples

The following are examples to show the form. Field names are illustrative.

A fragment of a generated JSON Schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "unevaluatedProperties": false,
  "properties": {
    "environment": { "enum": ["bare-metal", "serverless", "kubernetes"] },
    "preset": { "enum": ["T0", "T1", "T2", "T3", "T4", "T5", "T6"] },
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
  "required": ["environment", "nodes", "public_url"]
}
```

An install file on a single board, `/etc/sluis/install.yaml`:

```yaml
environment: bare-metal
preset: T1
nodes: 1
public_url: https://sluis.example.com

dependencies:
  yubikey: { per_node: true }
  cloudflare: { account: example }

drivers:
  yubikey-piv: { instance: shared }

resources:
  keys.oidc-signing: { driver: yubikey-piv, key: oidc }
  keys.audit-seal:   { driver: yubikey-piv, key: seal }

integrations:
  github:
    actor: facts
    app_key: cred:sluis-int-github.app-key   # a reference, never the key
```

And what `sluis config explain` prints for it:

```text
environment                   = bare-metal            install   /etc/sluis/install.yaml:1
preset                        = T1                    install   /etc/sluis/install.yaml:2
public_url                    = https://sluis.example.com  install   /etc/sluis/install.yaml:4
drivers.yubikey-piv.instance  = shared                install   /etc/sluis/install.yaml:11
log.level                     = debug                 stage override   SLUIS_LOG_LEVEL (tunable)
integrations.github.app_key   = cred:sluis-int-github.app-key   install   (reference, not resolved)
```

For where each file lives on a node, see [Drivers](/concepts/drivers/).
