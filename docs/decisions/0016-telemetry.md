# 0016 — Telemetry follows the group's OpenTelemetry conventions; sluis fixes its own names, and a log level is raised at run time only with an expiry

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #1003](https://github.com/truvity/sluis/issues/1003).

## Context

Thirty executables in two languages drift apart quickly unless the names of their telemetry are fixed once. The generic conventions are the group's telemetry rules, kept in the group's architecture, which every product of the group follows:
- the OpenTelemetry SDK of each language, used directly, with exporters set only by the standard `OTEL_*` variables;
- export that is best effort and never blocks a request;
- JSON log lines on stderr in the OpenTelemetry log model, with a fixed body;
- W3C Trace Context as the only propagation format, with parent-based sampling;
- the standard resource attributes;
- an allowlist of the attributes that may leave a process;
- the practice of gauges, counters and alert rules.

This record holds what is sluis's own: the names, the kinds that set log levels, where a trace starts, the correlation id, and the join between an audit record and a trace. The `REQ-OBS` rows state the rules that sluis tests.

## Decision

What is built: the kinds `LogSettings` and `LogLevelOverride` are in the kind registry, and every request carries a correlation id. The kit's instrumentation, the allowlist and the alert rules are planned (the `REQ-OBS` rows).

### Log levels

- **The log level and the trace sampling of an actor are the layered kind `LogSettings`.** It is a config kind of the boundary, with an id of the form `<actor>[/<module>]`, so a level can be set for one actor and for one module of it. Built-in defaults sit below it. It is never per tenant.
- **A level raised at run time is `LogLevelOverride`,** a temp kind in the operator layer. The console writes it itself, because no actor has domain logic for it, so the store checks its schema and bounds ([0027](/decisions/0027-the-console/)). Raising a level makes the log more permissive than config grants, so the override carries an expiry: 1 hour by default and at most 24 hours ([principle 12](/principles/)).
- **The unexpired override wins; when it expires the level returns to config,** with no write. Making a level permanent is a change of config, reviewed and signed ([0005](/decisions/0005-the-store/)).
- **An actor reads and watches its own ids only.** On Lambda nothing is pushed, so a function re-reads with an ETag.
- **Every raise is authorised for a named person and audited.**

### Where a trace starts, and span names

- **Only internal actors trust a remote parent.** `entry` starts a new trace for each request, a vendor's webhook to an adapter included, and keeps a caller's `traceparent`, if any, as a span link. At `entry`, in both languages, the server span has no parent, a new trace id and exactly one link to the caller's client span (measured on 2026-11-02).
- **Spans have fixed names:** one server span per RPC, named by the OpenTelemetry RPC conventions (`<package>.<Service>/<Method>`); one client span per call to another actor; one span per call through a port, named `<port>.<operation>` (for example `data.write`, `keys.seal`), with `sluis.driver` naming the driver when the call goes to one. A key operation's span records the purpose and the outcome, never the input.
- **The sampling ratio for an actor or a module comes from `LogSettings`.**

### The correlation id

- **`entry` gives every request a correlation id** (a random UUID version 4), returns it to the caller in a response header, and passes it in the metadata of every internal call the request causes. A client cannot choose it. A support question with the id finds the records of the request.
- **The header is `Atoom-Correlation-Id`.** The old name `Sluis-Correlation-Id` is accepted for one release. The wire change is planned in [atoom](https://github.com/truvity/atoom/blob/master/docs/call-context.md); until it lands the header keeps its old name.

### Metric names

- **Metric names are `sluis.<area>.<thing>`,** and the RPC and HTTP metrics take the names of the OpenTelemetry semantic conventions of the pinned SDK version wherever one exists. Every RPC and every call through a port has a duration histogram and an outcome count; the outcome of a port call is one of `ok`, `conflict`, `not_found` or `error`.
- **`service.instance.id` is `<actor>@<boundary>`.**
- **Named metrics that other records rely on, and how their series combine:**
  - `sluis.key.expiry`: seconds until a key's certificate or binding expires, attributes `purpose` and `boundary`, read as the minimum over instances. It drives the expiry warnings of [0024](/decisions/0024-root-custody-and-the-signing-api/).
  - `sluis.directory.staleness`: seconds since the last good read of each upstream directory ([0017](/decisions/0017-identity-and-sign-in/)), shown as the maximum and driving no alert.
  - `sluis.job.ticks`: the counter that every periodic job (a reconciler target, a directory refresh, a sweep) adds to on each pass, with the attributes `job` (bounded) and `outcome` (`ok` or `failed`) ([REQ-OBS-036](/requirements/obs/#REQ-OBS-036)). The age of a job, and its alerts, come from this counter.

### Pointing from audit to a trace

[Audit](https://github.com/truvity/audit) is a product of its own; its records are described in [its record format](https://github.com/truvity/audit/blob/master/docs/decisions/0004-the-record-format.md). A trace carries the audit record's `metadata.uid` as `sluis.audit.uid`, and the audit record carries the trace id, so the two can be joined by someone who may read both.

## Consequences

- Fixed names let a dashboard, an alert rule and a support question work on every executable in both languages. The Rust kit carries the conventions in sluis's own instrumentation crate, which must keep pace with the Go SDK; the conformance suite checks both against the same expected spans and metrics.
- **The Rust kit costs more than first estimated.** Measured on 2026-11-02 on an x86_64 workstation: one hop pinned to 2 CPUs, 10,000 calls per repetition, the median of 5 repetitions, server CPU summed over all threads including the exporters. The server and client spans with the RPC histograms add 15.0 µs of CPU per call to a Rust leaf, against 5.1 µs estimated before; the full set of conventions adds 21.8 µs (10.9 µs without telemetry, 32.7 µs with it). The Go kit adds 10.1 and 16.8 µs on a base of 35.7 µs. Rust with everything still uses less CPU at a leaf than Go with nothing.
- Every data access crosses store@ and every seal or open crosses key@, so a request has a span for each hop. The two holders are the first place to look when a request is slow, and their histograms are the input to the capacity measurements ([0037](/decisions/0037-capacity-is-measured-before-it-is-promised/)).
- Starting a new trace at `entry` means an outside caller's trace stops at sluis's edge and continues as a link. A caller cannot plant trace ids or flood the sampler.
- A raised log level costs log volume for at most a day and then ends by itself.
- The `REQ-OBS` rows state these rules; the alert rules and the dashboard of v1 are rebuilt on these names in parity ([#836](https://github.com/truvity/sluis/issues/836)).

## Roads not taken

- **Trusting a remote parent at `entry`.** An outside caller could choose trace ids and force sampling on every request it sends.
- **The audit record id as the trace id, or the trace inside the audit record.** Either makes one store depend on the other's retention.
- **A log level raised with no expiry.** A forgotten debug level would stay on, and nothing would tell an operator that the log is more detailed than config says.
- **A log level per tenant.** The layers of the logging group are built-in, config and the operator's override, set per boundary, actor and module; none carries a tenant.
- **Keeping the generic conventions in this record.** They bind every product of the group, so they are the group's rules; this record would repeat them and drift.

## Follow-ups

- Telemetry, health probes, the alert rules and the dashboard for parity: [#836](https://github.com/truvity/sluis/issues/836).
- The expiry warning that uses `sluis.key.expiry`: [#1068](https://github.com/truvity/sluis/issues/1068).
- The Rust kit: where the 15 µs go, a client wrapper or transport that always injects `traceparent` (connect-rust has no client interceptor), OTLP over TLS, and the value of `server.address`: [#861](https://github.com/truvity/sluis/issues/861).
