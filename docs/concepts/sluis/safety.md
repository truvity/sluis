# Safety

What can break and how sluis prevents it: every refusal at
render or at load, every default chosen because the other one failed,
and the traps that were met in use, each with the failure that earned
it. The test for whether something belongs here: *what goes wrong if I
do the obvious thing.* This page is an index; the pages below hold the
substance. For a map of every page in the repository, not just these, see
[index.md](README.md).

- [reference/policy.md](../../reference/sluis/policy-validation.md#refused-at-load) — what the
  policy loader refuses, so that a typo fails a rollout rather than a
  sign-in
- [reference/policy.md](../../reference/sluis/policy-clients.md#clients-that-describe-themselves)
  — what a client that registers itself by serving a document may and may
  not do, why an allow-list of origins is the guard rather than a refusal
  of unknown clients, and why there is no stale fallback
- [reference/policy.md](../../reference/sluis/policy.md#resources--what-a-token-is-for)
  — what a client asking for a resource gets and what it is refused, why
  the parameter is refused rather than ignored, and why the session has to
  remember which resource it was opened for
- [reference/configuration.md](../../reference/sluis/configuration.md) — the strict
  values schema, the values that refuse to render unset, and the things
  the chart will not do for you (mint its signing key, run a gateway)
- [connect/github-organisation.md](github-pass.md#what-the-render-refuses-and-why-each-is-silent-otherwise)
  — what the render refuses about GitHub bindings, and why each would
  otherwise be silent
- [connect/github-apps-catalogue.md](../../guides/sluis/connect/github-app-tokens.md#errors)
  — how a token request is refused, and what the audit trail records
- [architecture.md](architecture.md#failure-semantics) and
  [the design](design.md#failure-semantics) —
  what happens when the directory, the store or the issuer is down
- [trust.md](trust.md#recovery-is-the-root-not-a-back-door)
  and [day two](../../guides/sluis/operate/lost-operator-access.md)
  — the way back in when nobody can sign in
- [day two](../../guides/sluis/operate/check-health.md)
  — what each unhealthy state means and what to do, and
  [when the installation cannot be reached](../../guides/sluis/operate/read-the-audit-trail.md#3-when-the-installation-cannot-be-reached)
- [connect/console-app.md](../../guides/sluis/connect/console-app.md#traps-that-were-real) —
  the traps of putting a console behind the gateway
- [reference/sluisctl.md](../../reference/sluis/sluisctl.md#exit-codes)
  — every failure of `sluisctl bao`/`pg`/`psql` and its exit code, and a
  key it will never overwrite
- [reference/policy.md](../../reference/sluis/policy-bindings.md#slack-channels) — what the loader
  refuses about Slack channels (a strict public channel, an empty `from`, the
  removed keys `team_id`, `domains` and `owner`, which now name where the value
  comes from)

## The reconcilers: what they refuse to do

- [connect/slack-workspace.md](slack-pass.md#what-it-never-does)
  and [the design](design.md#the-slack-reconciler)
  — what the Slack controller will not do: create an account, touch a user
  group, remove anybody from a public channel, convert a channel's visibility,
  unarchive a channel, create a second channel under another name, invite or
  remove a guest, or remove anyone the directory has not vouched for
- [connect/slack-workspace.md](slack-pass.md#breakers) and
  [connect/slack-workspace.md](../../reference/sluis/slack.md#confirming-a-breaker-from-the-console)
  — the two removal breakers (a channel, the workspace), the fingerprint an
  operator confirms, and its 24-hour lapse
- [connect/slack-workspace.md](slack-pass.md#dry-run-until-enabledworkspaces)
  — every workspace is a dry run until the chart lists it; removing it from the
  list is the emergency stop
- [connect/slack-workspace.md](slack-pass.md#modes) — `extend` adds
  only and is the default; `strict` is for private channels only and refused at
  load for a public one; Slack Connect channels are always `extend`
- [connect/slack-workspace.md](../../guides/sluis/connect/slack-console-channels.md)
  — a channel defined in git and in the console is held, not merged; the
  console refuses to manage a channel git defines; archiving from the console is
  off by default and refused for Slack Connect channels
- [connect/slack-workspace.md](../../guides/sluis/connect/slack-workspace.md#connect-a-workspace-from-the-console)
  — a team that is not the recorded one is revoked and refused; the
  configuration token is never stored or logged
- [connect/slack-apps-catalogue.md](../../guides/sluis/connect/slack-apps-catalogue.md#errors) —
  how a Slack App install is refused

## And the rest

- [conformance-findings.md](conformance-findings.md) — what the OpenID conformance suite
  found, and what was fixed
- [CONTRIBUTING.md](../../../CONTRIBUTING.md) — the fixes that must not be
  reached for, each because it was the first idea and the wrong one
- [SECURITY.md](../../../SECURITY.md) — reporting a vulnerability, and the
  design notes a reviewer should read first
