---
title: Configuration
description: "How sluis is configured: Pkl defines the shape at build time, generated JSON Schema validates at runtime, with layers, data origins and explain commands."
status: target
sidebar:
  order: 6
---

sluis is configured by files that an operator reads and edits. The design makes four promises about them: a file that is wrong is refused before anything starts; a field that does not exist is an error, not silently ignored; any value can be traced to the layer that set it; and an operational switch can never change who may do what.

## Pkl at build time, generated artifacts at runtime

The **shape** of the configuration is written in Pkl, and only at build time. Pkl generates:

- a **JSON Schema** (draft 2020-12), which is part of the public API;
- the **Go structs** the roles load into;
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

A secret is a reference such as `cred:sluis-int-github.app-key`, resolved at runtime from the credential store the shape provides. The value never appears in a file, in `explain` output, in logs or in a crash dump.

## Layers

Values come from layers. A later layer overrides an earlier one:

1. **defaults**, built into the schema;
2. **shape profile**, for example the single-node or Lambda profile;
3. **install**, the file the operator edits;
4. **environment**, for the few fields marked as tunable.

## Checking and explaining

```bash
sluis config check
sluis config explain
```

`sluis config check` validates the configuration against the schema and refuses a combination that cannot work, such as a Raft-based state store on Lambda or a YubiKey on Lambda. `sluis config explain` prints each field's effective value and the layer that set it.

## Switches and authority

**Operational switches stay separate from authority.** A switch changes how sluis runs: a log level, a timeout, the choice of a driver. Authority says who may do what: policy, grants, the clients that may ask for tokens. The two are different kinds of configuration with different owners and review, and an environment override can reach a switch but never authority. v1 mixed them in one policy file.

## Examples

The following are examples to show the shape. Field names are illustrative.

A fragment of a generated JSON Schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "unevaluatedProperties": false,
  "properties": {
    "shape": { "enum": ["local", "single-node", "cluster", "ec2", "lambda", "kubernetes"] },
    "public_url": { "type": "string", "format": "uri" },
    "drivers": {
      "type": "object",
      "unevaluatedProperties": false,
      "properties": {
        "yubikey": { "$ref": "#/$defs/driverShared" },
        "aws": { "$ref": "#/$defs/driverPerRole" }
      }
    }
  },
  "required": ["shape", "public_url"]
}
```

An install file on a single node, `/etc/sluis/install.yaml`:

```yaml
shape: single-node
public_url: https://sluis.example.com

drivers:
  yubikey: { instance: shared }

resources:
  keys.oidc-signing: { driver: yubikey, key: oidc }
  keys.audit-seal:   { driver: yubikey, key: seal }

integrations:
  github:
    role: facts
    app_key: cred:sluis-int-github.app-key   # a reference, never the key
```

And what `sluis config explain` prints for it:

```text
shape                         = single-node           install   /etc/sluis/install.yaml:1
public_url                    = https://sluis.example.com  install   /etc/sluis/install.yaml:2
drivers.yubikey.instance      = shared                install   /etc/sluis/install.yaml:5
log.level                     = debug                 environment   SLUIS_LOG_LEVEL (tunable)
integrations.github.app_key   = cred:sluis-int-github.app-key   install   (reference, not resolved)
```

For where each file lives on a node, see [Drivers](/concepts/drivers/).
