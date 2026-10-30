# 0015 — Configuration is defined once in Pkl and shipped as JSON Schema

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (realignment pass).

## Context

The shape of sluis's configuration is read by many consumers, in many languages. The Go actors load it. The Rust actors and drivers load it. The console, written in TypeScript, shows it and uses the same shapes for the operator-set records it sends; the actor that owns each record kind validates those records ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). Helm checks the values of the chart. The reference documentation lists every field. If each of them has its own description of the shape, they drift apart, and the first sign is a deployment that starts with a field one of them ignored.

v1 settled the first half of this in two steps. v1 ADR 0063 (on `master`) made the configuration one file validated against a JSON Schema that ships with the release, so that an unknown key, a missing key or a wrong type is a start-up error naming the path. It listed a configuration language that generates the file (Pkl, CUE) as a good later producer of that schema. v1 ADRs 0057 and 0069 (on `master`) gave every schema a stable identifier and then moved the identifiers twice, from an unserved name to one site and then to another, each time with an alias, because archived documents carried the old names.

v2 takes the later producer now. A process that runs for a short time on a small board or in a serverless function cannot afford a heavy tool at start, so the question is also what exists at run time and what exists only at build time.

Two limits matter. The Pkl command-line tool is about 95 MiB, and the Go binding starts it as a child process. That is too heavy for a single-board computer or a serverless function, and an actor that has no right to start a process ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)) cannot use it. And JSON Schema, the format v1 chose, has no single validator that serves Go, Rust and TypeScript alike, so each language needs its own and they must agree.

## Decision

- **The shape of the configuration is defined once, in Pkl, and Pkl runs at build time only.** The Pkl sources live in the repository. There is no Pkl at run time.
- **The build generates these artifacts from the Pkl sources:**
  - a **JSON Schema in draft 2020-12**, which is part of the public API;
  - the **Go structs** that the actors load into;
  - the **Helm values schemas**;
  - the **reference documentation**.

  Where Pkl has no generator, for example from Pkl to JSON Schema, a separate open-source tool fills the gap. The tool is a build detail and not part of the public API.
- **The runtime uses only the generated artifacts.** The schema is embedded in each binary that loads configuration.
- **The JSON Schema has a stable identifier.** Each schema's `$id` is a URL on the sluis documentation site, with its version in the path, so that a validator outside Go can fetch it. A published identifier is not changed. A schema that changes incompatibly is a new version under a new path, beside the old one.
- **Each language has one validator, and they share conformance vectors.**

  | Language | Validator |
  |---|---|
  | Go | `santhosh-tekuri/jsonschema/v6` |
  | Rust | the `jsonschema` crate |
  | TypeScript | `ajv`, in its draft 2020-12 build |

  A shared set of vectors, each a document with the expected verdict and the expected error path, runs against all three. A validator that disagrees with the others fails the build.
- **Strictness comes from the schema.** The schema sets `unevaluatedProperties: false`, so an unknown key is an error that names the field. Format assertions are on, so a field declared as a URL or a duration is checked as one. A retired option that is still set in a declared document is refused and not ignored. A record that sluis wrote itself is read under the rule in [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/).
- **The schema carries the origin and sensitivity of the data it describes.** Each record kind's origin and each field's sensitivity, authority mark and the other annotations of [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/) are written in the Pkl source and generated into the JSON Schema. CI checks them, and the generators that build the backup list, the telemetry allowlist and the audit redaction read them.
- **Telemetry is not configured in the file.** It is configured by the standard `OTEL_*` environment variables only (observability: ADR 0036). The configuration schema has no telemetry keys, so the install file does not change when signals go somewhere else, and the platform decides where they go.
- **The install file is the document the schema describes.** "Installation" is not a key of it ([0009](/decisions/0009-one-installation-per-boundary/)).
- **Decided in [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/), not here:** the origins of data (declared, operator-set, learned, derived, issued) and their precedence, how a secret is referenced, the order of the configuration layers, and live reload of declared configuration as whole versioned snapshots.

## Consequences

- A field is added in one place. The Go struct, the Helm schema, the console's validation and the reference page change with it, and the shared vectors catch a validator that reads it differently.
- The schema is a public contract. Its identifiers and its versions are an interface that people outside the repository can depend on, so a change to either is a change to the public surface. What may change in a release is ADR 0053.
- The build gains a step, and with it a toolchain: Pkl, a Pkl-to-JSON-Schema generator and the code generators for Go and Helm. The generator for JSON Schema is the part most likely to need patching, because Pkl has none of its own. (inferred) In v1 the generated schemas were committed and checked in CI, and v2 keeps that discipline so that a stale generated artifact fails the build.
- Three validators implement one standard. They agree on the vectors, and they may still differ on an edge that no vector covers. Each real disagreement adds a vector.
- Helm checks values with a schema in a dialect of its own checker. The Helm schema is generated to the dialect that the checker accepts, and the vectors cover the fields that Helm shares with the install file. (inferred)
- Operators get early, uniform errors. A misspelt field, a wrong type and a retired option all fail at `sluis config check` and at start, and name the path.
- A secret never appears in the schema as a value: a field that holds a secret is declared as a reference. How references work is in [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/).
- v2 publishes its own identifiers on the sluis site from the start, so it does not repeat v1's moves. The identifiers of v1 keep working for v1, and how a v1 file moves to v2 is part of ADR 0044.

## Roads not taken

### Pkl at run time

- Pros: one definition, and no generated copy to check in.
- Cons: the command-line tool is about 95 MiB, and the Go binding starts it as a child process. That does not fit a single-board computer or a serverless function, and an actor has no right to start a process.
- Why not: Pkl is used at build time only, and the runtime uses generated artifacts.

### Hand-written JSON Schema as the source

- Pros: no extra language and no generator. JSON Schema is the public contract already.
- Cons: Go structs, Helm schemas and documentation would have to be written again from it, by hand or with other tools, and each copy can drift. JSON Schema has no way to share a definition across files that is as plain as a typed language. (inferred)
- Why not: the source should be one typed definition that the schema is generated from.

### Protobuf messages as the source of the schema

- Pros: contracts between components are already Protobuf ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)). One toolchain.
- Cons: a message describes a wire format. It has no good way to say "this field is a duration", "this key is retired" or "no other keys". (inferred)
- Why not: this was the earlier plan and Pkl replaced it. A configuration shape is not a wire contract.

### CUE as the source

- Pros: designed for configuration and for validating data. It can export JSON Schema (v1 ADR 0063 named it beside Pkl).
- Cons: the maintainer chose Pkl. CUE's model of unification is unfamiliar to many operators, and its generators for the code we need would still be separate tools. (inferred)
- Why not: Pkl was chosen. The decision does not depend on the language: the schema is what the consumers see, so changing the source language later would not change the public API.

### The Go structs as the source, with a schema generated from them by reflection

- Pros: it is what v1 does today. No new language.
- Cons: Rust and TypeScript consumers get their shape from a Go type. Documentation and Helm still need separate generation. A Go-only source cannot express the public API as a language-neutral contract. (inferred)
- Why not: the schema is the contract, and a Go type should not be the source of it.

### One validator, called from the other languages

- Pros: one implementation and one set of bugs. No vectors needed to align three.
- Cons: the other languages would call into it through a process or a foreign interface. That fails the restriction on starting processes, and it adds a runtime dependency to a function that has to start fast. (inferred)
- Why not: a validator per language, with shared vectors, keeps each actor self-contained.

### Telemetry settings in the configuration file

- Pros: one place for all settings.
- Cons: the file would be tied to one environment, and it would duplicate a standard that every OpenTelemetry SDK already reads from the environment (v1 ADR 0063, on `master`).
- Why not: telemetry is configured by `OTEL_*` variables only.

### Many sources of configuration: flags and environment variables beside a file

- Pros: familiar. Easy for a single value.
- Cons: the sources overlap and none of them is checked as a whole. A flag misspelt in a chart is found when a container starts (v1 ADR 0063, on `master`).
- Why not: the file is the configuration. What else may come from the environment is decided with the layers ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)).
