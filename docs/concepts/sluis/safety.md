# Where do I find what can break?

This page indexes every refusal, every default chosen because the other failed, and every trap met in use. For a map of
all pages, see the [index](README.md). Ask: what goes wrong if I do the obvious thing?

## Policy and clients

| Page | Covers |
|---|---|
| [policy validation](../../reference/sluis/policy-validation.md#refused-at-load) | what the loader refuses, so a typo fails a rollout and not a sign-in |
| [self-describing clients](../../reference/sluis/policy-clients.md#clients-that-describe-themselves) | what a client that serves a document may do, and why there is no stale fallback |
| [resources](../../reference/sluis/policy.md#resources--what-a-token-is-for) | what a client asking for a resource gets and what it is refused |
| [Slack channel bindings](../../reference/sluis/policy-bindings.md#slack-channels) | what the loader refuses about Slack channels |
| [GitHub bindings](github-controller.md#what-waits-for-a-person) | what the render refuses about GitHub bindings |
| [GitHub App tokens](../../guides/sluis/connect/github-app-tokens.md) | how a token request is refused and what the audit trail records |
| [Slack Apps](../../guides/sluis/connect/slack-apps-catalogue.md) | how a Slack App install is refused |

## Configuration and operation

| Page | Covers |
|---|---|
| [configuration](../../reference/sluis/configuration.md) | the strict values schema and the values that refuse to render unset |
| [failure semantics](failure-semantics.md) | what happens when the directory, the store or the issuer is down |
| [trust](trust.md#recovery-is-the-root-not-a-back-door) and [lost operator access](../../guides/sluis/operate/lost-operator-access.md) | the way back in when nobody can sign in |
| [check health](../../guides/sluis/operate/check-health.md) | what each unhealthy state means |
| [unreachable installation](../../guides/sluis/operate/read-the-audit-trail.md#3-when-the-installation-cannot-be-reached) | what to do when you cannot reach it |
| [console app traps](../../guides/sluis/connect/console-app.md) | putting a console behind the gateway |
| [sluisctl exit codes](../../reference/sluis/sluisctl.md#exit-codes) | every failure of `sluisctl bao`, `pg` and `psql`, and a key it never overwrites |

## What the Slack controller refuses

The [Slack pass](slack-pass.md#what-it-never-does) lists what the controller will not do. It creates no account and
touches no user group. It removes nobody from a public channel, converts no visibility, unarchives nothing and invites or
removes no guest. It removes nobody the directory has not vouched for.

| Page | Covers |
|---|---|
| [breakers](slack-pass.md#breakers) and [confirming one](../../reference/sluis/slack.md#confirming-a-breaker-from-the-console) | the two removal breakers, the fingerprint an operator confirms, its 24-hour lapse |
| [dry run](slack-pass.md#dry-run-until-enabled) | every workspace is a dry run until the chart lists it; removing it is the emergency stop |
| [modes](slack-pass.md#modes) | `extend` is the default; `strict` is for private channels and refused for a public one; Slack Connect is always `extend` |
| [console channels](../../guides/sluis/connect/slack-console-channels.md) | a channel defined in git and the console is held; archiving is off by default |
| [connect a workspace](../../guides/sluis/connect/slack-workspace.md) | a team that is not the recorded one is revoked; the configuration token is never stored or logged |

## Elsewhere

| Page | Covers |
|---|---|
| [conformance findings](conformance-findings.md) | what the OpenID conformance suite found, and what was fixed |
| [CONTRIBUTING](../../../CONTRIBUTING.md) | the fixes not to reach for |
| [SECURITY](../../../SECURITY.md) | reporting a vulnerability, and design notes to read first |
