# Adoption

What a platform needs to take sluis into use: the prerequisites,
the order to install in, the first sign-in, connecting a directory and
then each cluster, account and console, adopting objects or an identity
provider that already exist, and moving between releases. The test for
whether something belongs here: *I am about to change what runs.* This
page is an index; the pages below hold the substance. For a map of every
page in the repository, not just these, see [index.md](index.md).

## The zero-diff gate

**A consumer adopts a release only when the render it produces is
byte-identical to what runs, or differs exactly by the change the release
announces.** Render the chart with your values at the pinned version
and at the new one, and compare; a difference you cannot explain from
[CHANGELOG.md](../CHANGELOG.md) is a reason to stop. Moving from
hand-written objects, or from another installation method, to these
charts is one change whose render diff is empty; tightening a default is
a separate release, adopted separately. The policy is part of the
values, so a policy change is a render diff too, and is reviewed as one.
[operations/adoption-plain-helm.md](how-to/install-with-helm.md#without-argocd-or-kargo)
shows the comparison with nothing but `helm template` and `diff`. Upgrade order
for 1.42 and later: roll the console before the Slack controller; the controller
fails every pass against a console older than the `ListServedDomains` RPC.

## Where it is written down

- [operations/adoption-plain-helm.md](how-to/install-with-helm.md) —
  prerequisites, install order with neutral values, the
  first sign-in, and taking releases without a GitOps controller
- [reference/configuration.md](reference/configuration.md) — what the
  issuer's chart renders and expects, and the objects the service writes
- [operations/runbook.md](how-to/day-two.md#day-one) — day one: the
  recovery sign-in and the console's Overview
- [operations/connect-runbook.md](how-to/connect/google-workspace.md) — the
  one-time OAuth client and connecting each Google Workspace
- [connect/](connect/) — one guide per thing that trusts the issuer: a
  [Kubernetes cluster](how-to/connect/kubernetes-cluster.md), an
  [AWS account](how-to/connect/aws-account.md), [ArgoCD](how-to/connect/argocd.md),
  [Kargo](how-to/connect/kargo.md), [a console](how-to/connect/console-app.md),
  [GitHub Actions](how-to/connect/github-actions.md),
  [a GitHub organisation](how-to/connect/github-organisation.md),
  [catalogue GitHub Apps](how-to/connect/github-apps-catalogue.md),
  [a Slack workspace](how-to/connect/slack-workspace.md),
  [catalogue Slack Apps](how-to/connect/slack-apps-catalogue.md),
  [Slack Connect channels](how-to/connect/slack-connect-channels.md),
  [an infrastructure-as-code program](how-to/connect/infrastructure-as-code.md),
  [registries and artifacts](how-to/connect/registries-and-artifacts.md),
  [a service called by workloads](how-to/connect/service-to-service.md),
  [a secret manager that mints certificates](how-to/connect/openbao.md),
  [the corporate directory](how-to/connect/corporate-directory.md)
- [operations/runbook.md](how-to/day-two.md#enabling-a-slack-workspace) —
  enabling a Slack workspace: connect it, read its dry run, then list it in
  `policy.controllers.slack.enabledWorkspaces`; and
  [operations/runbook.md](how-to/day-two.md#slack-state) — backing up and
  restoring Slack state
- [connect/slack-workspace.md](how-to/connect/slack-workspace.md#moving-a-policy-channel-to-the-console)
  — moving a channel from git to the console: remove it from the policy, then
  Manage it from Discovered (a channel defined in both places is held, and there
  is no take-over)
- [CHANGELOG.md](../CHANGELOG.md) v1.42.0 — a policy that still carries
  `slack.workspaces.<key>.team_id`, `domains` or `owner`, or `github.<org>.owner`,
  is refused at load: delete the keys and connect (or reconnect) from the console
- [operations/migration-from-an-idp.md](how-to/migrate-from-an-idp.md)
  — retiring an identity provider run for infrastructure without a day of
  broken logins
- [operations/migration-from-google-group-sync.md](how-to/migrate-from-google-group-sync.md)
  — moving from a per-workspace directory reader
- [reference/sluisctl.md](reference/sluisctl.md#installing-it) — putting
  `sluisctl` on laptops and into CI toolchains
- [CHANGELOG.md](../CHANGELOG.md) — every release, and for a breaking
  one what to do first
