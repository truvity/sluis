# 0038 — Estates render their documents through sluis

**Status:** Accepted; refines [0036](0036-configuration-is-immutable-per-instance.md)
(who writes the documents an instance is started with) and
[0037](0037-one-process-everywhere.md) (one service document, one policy document)
**Date:** 2026-10-05

## Context

An instance is started with two documents: the service document (`sluis.yaml`,
`sluis/v3`) and the policy document (`policy.yaml`, `policy/v2`). 0036 made them
immutable per instance and 0037 made them one pair. Neither said who writes them,
and in practice both estates write them by hand, each in its own way:

- **One estate** (on AWS Lambda) builds `map[string]any` documents in Go, and keeps the
  names the library owns (`secrets`, the state secret, the trigger's function) out
  of them by convention. The Pulumi library renders the policy from layers and
  refuses a document that disagrees with what it writes.
- **The other estate** (on Kubernetes) hand-renders the policy pieces in Go and Helm helpers:
  the roster values, the exchange clusters, the catalogue, the exports. The chart
  copies `config` into a ConfigMap and re-checks it against the values around it.

The two disagree about the same facts (the preset, the adapter settings, the
paths a shape fixes), and every change to a document's shape is made three times:
in sluis, in the estate's Go, and in the chart. A shape that no longer matches is
found by the service refusing to start, after the rollout. Beside that, the
Pulumi library imports `internal/config`, so it builds against the root module at
the exact release it is tagged with, and a library tagged with an older `require`
compiled for nobody.

## Decision

**An estate states what it knows once, as an installation, and sluis renders both
documents from it.**

1. **The installation** (`apiVersion: sluis.truvity.github.io/installation/v1`,
   `schemas/config/installation.schema.json`) is the estate's side of the line: the
   shape it runs in (`lambda`, `kubernetes`, `server`), the AWS resources and the
   OpenBao it is built on, whom it trusts, its clients and its exports. It is a
   superset of the two documents: a section is under the key the document gives it
   and holds that document's own type, so what is learned reading one is true of
   the other. It holds no secret.
2. **One renderer.** `config.Render` (the public package
   `github.com/truvity/sluis/config`) turns an installation into the two documents.
   It is deterministic, derives what the shape fixes (the preset, `policy.file`, the
   public URLs, the adapters the resources stand for, the names layout v3 gives the
   secrets sluis writes) and refuses a value that disagrees with it, and holds both
   outputs to the loader the service runs at start. `sluisctl render` is the same
   function for an estate that renders in CI, and `--check` holds committed
   documents to their source. The Pulumi library calls it for
   `LambdaArgs.Installation`, so Lambda and Kubernetes estates render the same way.
3. **The library imports the public package only.** What the library needs from the
   root module is a stable, documented surface (`Load`, `Validate`, `Render`, the
   document types), not whatever `internal/config` looks like at that commit. A test
   holds `deploy/pulumi` to no `internal/` import.
4. **The `require` is pinned by the release, not by a person.** The library still
   requires the root module (the public package lives in it). The release workflow
   tags `deploy/pulumi/vX.Y.Z` at a child of the release commit whose `go.mod`
   requires `vX.Y.Z` and nothing else differs (`hack/modules.py`, tested
   in `just release-chain`). The require on master is never bumped before a tag.
5. **The old way keeps working for one minor**, marked deprecated: `LambdaArgs.Config`,
   `Policy` and `PolicyPath` log a warning when used, and `sluisctl policy render`
   stays (a layer of policy files is still a way to write a policy, and is what an
   installation's tables replace for estates that choose to).

Breaking changes remain allowed in minors until the configuration is declared stable
([0007](0007-breaking-changes-inside-1x.md)); this decision starts the stabilization
by giving the estates a public, versioned surface (`installation/v1`, the `config`
package) and a deprecation period for what it replaces.

## Consequences

- An estate deletes its hand-rendering: the Lambda estate its `map[string]any` documents and the
  policy layers' Go, the Kubernetes estate its generated policy pieces and the
  Helm helpers that rebuild the policy from values. What remains in an estate is the
  facts, as data.
- A change to the documents' shape is made once, in sluis, and reaches both estates
  with the release, with `sluisctl render --check` showing the difference before a
  rollout.
- The installation is a third schema to keep. Its sections reuse the service and
  policy schemas' own definitions, so a key added to one reaches it without a second
  edit, and the types of the documents are the installation's.
- The renderer cannot check what only a running process knows: that an adapter's
  platform answer holds, that an OpenBao is reachable. The service still checks that
  at start.
- **The honest boundary.** An estate that needs a document the installation cannot
  say (a key a section does not hold) still writes it, and is refused by the schema,
  naming the key; the installation grows by a release, not by an escape hatch. A
  service document written by hand stays valid: the installation is a way to write
  them, not the only one.

## Alternatives considered

**Chart-only rendering.** Let the Helm chart be the renderer, as it is for
Kubernetes today, and have Lambda estates read the chart's output. Rejected: a Lambda
estate has no Helm and would shell out to it from Pulumi, the checks live in Go and
templates cannot share them, and the chart's own values are the third copy of the
documents' shape. The chart takes the rendered documents instead (a follow-up, in
the same series), and keeps only deployment-level values.

**The status quo.** Each estate renders its own. Rejected for the reason in the
context: three copies of one shape, found by a refused start. It also keeps the
library on `internal/`.

**Move the library into the root module.** It would remove the require, and with it
the pin. Rejected: Pulumi and the AWS provider SDK would join the root module's
dependency graph, for every consumer of the policy and identity packages.

**Bump the require by hand before every tag, with a check.** A CI check that
fails when `deploy/pulumi/go.mod` differs from the version being tagged cannot run
before the tag exists, so it would fail after the release commit is merged and the
version is burned. Pinning in the release workflow removes the step instead of
guarding it.

**Make `internal/config` public.** Rejected: it is the binary's own loader and
carries what the binary needs, which is more than an estate should depend on. The
public package re-exports the document types and adds the installation, and what it
exports is a promise.
