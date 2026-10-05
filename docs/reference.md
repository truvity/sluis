# Reference

Every value, flag, input and output: its default, its type, what it does
and when it is required. The test for whether something belongs here:
*what does this knob do.* This page is an index; the pages below hold the
tables. For a map of every page in the repository, not just these, see
[index.md](index.md).

- [reference/configuration.md](reference/configuration.md) — every value
  of the `sluis` chart, the overlay format, every endpoint the
  issuer serves, the objects the service writes, and each controller's
  environment
- [reference/policy.md](reference/policy.md) — the policy file: groups,
  matchers, clients, resources, client documents, lifetimes, GitHub
  bindings, `people` and Slack channels
- [reference/sluisctl.md](reference/sluisctl.md) — every command and
  flag of `sluisctl`, and its exit codes;
  [`bao`](reference/sluisctl.md#bao-authenticate-then-run-bao-unchanged),
  [`pg` / `psql`](reference/sluisctl.md#pg--psql-a-postgres-client-certificate-then-a-command)
  and [`ssh known-hosts`](reference/sluisctl.md#ssh-known-hosts-trust-configured-ssh-host-cas-before-the-first-connect)
  for what OpenBAO and the secret stores mint
- [connect/github-actions.md](how-to/connect/github-actions.md#workflow-side-the-action)
  — every input and output of the GitHub Action, and the
  [`token-source: access-roster`](how-to/connect/github-actions.md#in-a-reusable-workflow-token-source-access-roster)
  pattern for a reusable workflow
- [reference/adapters.md](reference/adapters.md) — every adapter per concern, what it
  needs, where it runs and whether it is built (generated from the registry)
- [reference/contracts.md](reference/contracts.md) — the ConnectRPC
  services (the Slack services included), installation tokens at `/token`, and
  the whoami endpoint
- [connect/slack-workspace.md](how-to/connect/slack-workspace.md) — the Slack
  controller: the policy's `slack` table in action, connecting a workspace,
  console channels, holds and the breaker;
  [Slack Connect channels](how-to/connect/slack-connect-channels.md);
  [the Slack Apps catalogue](how-to/connect/slack-apps-catalogue.md)
- [`internal/audit/catalogue/roster.yaml`](../internal/audit/catalogue/roster.yaml)
  — every audited action and what it carries
- [reference/go-module.md](reference/go-module.md) — the Go module
  `github.com/truvity/sluis`
- [reference/typescript.md](reference/typescript.md) — the TypeScript
  package, and installing it from GitHub Packages
- [conformance.md](explanation/conformance-findings.md) — the last OpenID conformance run,
  column by column
- `charts/*/values.schema.json` — the schema each chart's values are
  checked against; an unknown key fails the render
