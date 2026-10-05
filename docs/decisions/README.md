# Architecture decisions

One record per decision that shapes this repository from the outside —
what a relying party must do, what an installation must accept, what a
release removes. The pages under [`../explanation/`](../explanation/) say
how the shipped thing works; a record here says why it is shaped that
way, what was weighed against it, and what follows from choosing it. When
the two disagree, the explanation pages describe what actually shipped and a
record here is read as the reasoning that got there.

A record is never edited to reverse a decision. A changed mind gets a new
record that supersedes the old one, so the index below stays a true
timeline and nothing is silently rewritten under an old date. The old record's
**Status** line (and the Status column below) is changed to say what overtook
it, so a reader of one record does not need to know another exists; its body
is not edited.

## Index

<!-- generated: adr-index -->
| ADR | Decision | Status |
|---|---|---|
| [0001](0001-sessions-and-an-absolute-limit.md) | Sessions and an absolute limit | Accepted; amended by [0033](0033-a-longer-absolute-limit-for-read-only-resources.md) |
| [0002](0002-mission-boundary-tokens-and-memberships.md) | Mission boundary: tokens and memberships | Partly superseded by [0008](0008-credentials-only-where-we-govern-membership.md), and for `sluisctl credential db`/`client` by [0013](0013-openbao-access-through-the-bao-cli.md) |
| [0003](0003-deprecate-access-proxy.md) | Deprecate and remove the access-proxy chart | Accepted; carried out in v1.32.0 |
| [0004](0004-ssh-opkssh-and-the-secret-stores-ca.md) | SSH: opkssh for people, the secret store's SSH CA for hosts | Partly superseded by [0011](0011-ssh-people-opkssh-machines-and-hosts-openbao.md); refined by [0015](0015-a-per-audience-groups-delimiter-for-opkssh.md) |
| [0005](0005-es384-signing-algorithm.md) | ES384 is the signing algorithm | Partly superseded by [0009](0009-a-default-signing-algorithm-and-per-audience-exceptions.md) |
| [0006](0006-groups-claim-scoped-per-audience.md) | The groups claim is scoped per audience, by default | Accepted; refined by [0010](0010-a-declared-vocabulary.md) |
| [0007](0007-breaking-changes-inside-1x.md) | Breaking changes inside 1.x | Accepted; extended by [0036](0036-configuration-is-immutable-per-instance.md) |
| [0008](0008-credentials-only-where-we-govern-membership.md) | Credentials only where we govern membership | Accepted; supersedes [0002](0002-mission-boundary-tokens-and-memberships.md) in part; refined by [0014](0014-minting-third-party-credentials-only-where-membership-is-governed.md) |
| [0009](0009-a-default-signing-algorithm-and-per-audience-exceptions.md) | A default signing algorithm, and per-audience exceptions | Accepted; partly supersedes [0005](0005-es384-signing-algorithm.md) |
| [0010](0010-a-declared-vocabulary.md) | A declared vocabulary: things, scopes, roles, inheritance and mapping wildcards | Accepted; extended by [0012](0012-per-role-scopes-in-the-vocabulary.md) |
| [0011](0011-ssh-people-opkssh-machines-and-hosts-openbao.md) | SSH: people on opkssh, machines and hosts on the secret store's OpenBAO | Accepted; amended by [0013](0013-openbao-access-through-the-bao-cli.md); refined by [0015](0015-a-per-audience-groups-delimiter-for-opkssh.md), [0016](0016-a-managed-known-hosts-file-for-ssh-host-cas.md) |
| [0012](0012-per-role-scopes-in-the-vocabulary.md) | Per-role scopes in the vocabulary | Accepted; extends [0010](0010-a-declared-vocabulary.md) |
| [0013](0013-openbao-access-through-the-bao-cli.md) | OpenBAO access through the `bao` CLI | Accepted; refines [0002](0002-mission-boundary-tokens-and-memberships.md), amends [0011](0011-ssh-people-opkssh-machines-and-hosts-openbao.md) |
| [0014](0014-minting-third-party-credentials-only-where-membership-is-governed.md) | Minting third-party credentials: only where membership is governed, brokers elsewhere | Accepted; refines [0008](0008-credentials-only-where-we-govern-membership.md) |
| [0015](0015-a-per-audience-groups-delimiter-for-opkssh.md) | A per-audience groups delimiter, for opkssh's colon-splitting bug | Accepted; refines [0004](0004-ssh-opkssh-and-the-secret-stores-ca.md), [0011](0011-ssh-people-opkssh-machines-and-hosts-openbao.md) |
| [0016](0016-a-managed-known-hosts-file-for-ssh-host-cas.md) | A managed known_hosts file for SSH host CAs, distinct from `sluisctl bao` | Accepted; refines [0011](0011-ssh-people-opkssh-machines-and-hosts-openbao.md) |
| [0017](0017-the-slack-reconciler-membership-only.md) | The Slack reconciler keeps channel membership and nothing else | Accepted; applies [0002](0002-mission-boundary-tokens-and-memberships.md) |
| [0018](0018-do-not-configure-what-the-product-knows.md) | Do not configure what the product already knows | Accepted; applies [0007](0007-breaking-changes-inside-1x.md) |
| [0019](0019-two-kinds-of-slack-channel-never-mixed.md) | Two kinds of Slack channel, never mixed | Accepted; refines [0017](0017-the-slack-reconciler-membership-only.md) |
| [0020](0020-hold-on-double-definition-instead-of-taking-over.md) | A channel defined twice is held, not taken over | Accepted; refines [0019](0019-two-kinds-of-slack-channel-never-mixed.md) |
| [0021](0021-slack-connect-channels-are-console-records.md) | Slack Connect channels are console-managed, audited records | Accepted; refines [0019](0019-two-kinds-of-slack-channel-never-mixed.md) |
| [0022](0022-the-console-archives-only-ordinary-channels-only-when-asked.md) | The console archives only ordinary channels, only when asked | Accepted; refines [0017](0017-the-slack-reconciler-membership-only.md) |
| [0023](0023-guest-side-probe-only-for-managed-slack-connect-channels.md) | The guest-side probe asks only about managed Slack Connect channels | Accepted; refines [0021](0021-slack-connect-channels-are-console-records.md) |
| [0024](0024-reconciler-rails-are-shared-pieces-not-a-framework.md) | Reconciler rails are shared pieces, not a framework | Accepted |
| [0025](0025-slack-apps-catalogue-keeps-credentials-mints-none.md) | The Slack Apps catalogue keeps credentials but mints none | Accepted; applies [0008](0008-credentials-only-where-we-govern-membership.md), [0014](0014-minting-third-party-credentials-only-where-membership-is-governed.md) |
| [0026](0026-two-platforms-permanently-kubernetes-and-aws-lambda.md) | Two platforms, permanently: Kubernetes and AWS Lambda | Accepted |
| [0027](0027-the-state-port-nats-jetstream-and-dynamodb.md) | The State port: DynamoDB on AWS (the NATS JetStream half was removed, 2026-10-04) | Partly superseded: NATS removed 2026-10-04, the DynamoDB half stands |
| [0028](0028-nothing-writes-configmaps-or-secrets.md) | Nothing writes ConfigMaps or Secrets (sealing was retired, 2026-10-04) | Partly superseded: sealing retired 2026-10-04, the rule stands; extended by [0034](0034-exports-go-to-openbao-directly.md); amended by [0036](0036-configuration-is-immutable-per-instance.md) |
| [0029](0029-ticks-per-target-under-a-lease.md) | Ticks per target, under a lease | Accepted; applies [0024](0024-reconciler-rails-are-shared-pieces-not-a-framework.md) |
| [0030](0030-workload-identity-on-both-platforms.md) | Workload identity: both mechanisms on both platforms | Accepted; applies [0002](0002-mission-boundary-tokens-and-memberships.md) |
| [0031](0031-a-generic-migration-tool.md) | A generic migration tool, and the order of the move | Accepted; amended by [0036](0036-configuration-is-immutable-per-instance.md) |
| [0032](0032-one-configuration-file-one-binary-one-chart.md) | One configuration file, one binary, one chart | Accepted; refined by [0036](0036-configuration-is-immutable-per-instance.md) |
| [0033](0033-a-longer-absolute-limit-for-read-only-resources.md) | A longer absolute limit for read-only resources, up to seven days | Accepted; amends [0001](0001-sessions-and-an-absolute-limit.md) |
| [0034](0034-exports-go-to-openbao-directly.md) | Exports: the service copies its secrets into OpenBao itself | Accepted; extends [0028](0028-nothing-writes-configmaps-or-secrets.md) |
| [0035](0035-renamed-to-sluis.md) | Renamed to sluis: what changed and what deliberately did not | Accepted; its NATS and sealing rows are moot ([0027](0027-the-state-port-nats-jetstream-and-dynamodb.md), [0028](0028-nothing-writes-configmaps-or-secrets.md)) |
| [0036](0036-configuration-is-immutable-per-instance.md) | Configuration and policy are immutable per instance; credentials and State are read live | Accepted; amended by [0037](0037-one-process-everywhere.md); refined by [0038](0038-estates-render-through-sluis.md) |
| [0037](0037-one-process-everywhere.md) | One process everywhere: one Lambda function, one Deployment, one service document | Accepted; refined by [0038](0038-estates-render-through-sluis.md); amends [0036](0036-configuration-is-immutable-per-instance.md) |
| [0038](0038-estates-render-through-sluis.md) | Estates render their documents through sluis | Accepted; refines [0036](0036-configuration-is-immutable-per-instance.md), [0037](0037-one-process-everywhere.md) |
<!-- /generated -->

## Template

Start a new record from this shape. Keep it tight — long enough to make
the reasoning checkable, short enough that the next reader finishes it.

```markdown
# NNNN — <a decision, stated as a decision>

**Status:** Proposed | Accepted | Accepted; amended by [NNNN](NNNN-slug.md) | Accepted; partly superseded by [NNNN](NNNN-slug.md) | Superseded by [NNNN](NNNN-slug.md)
**Date:** YYYY-MM-DD

## Context

The situation that made a decision necessary, and the constraint that
ruled some answers out before the rest were compared.

## Decision

What was decided, stated so a reader could act on it without reading
anything else. Include the shape of the mechanism, not just its name.

## Consequences

What this costs, what it forecloses, and what a relying party or an
operator must now do differently. Say the honest boundary out loud —
the case this decision does not cover — rather than leaving it to be
discovered.

## Alternatives considered

Each one named, with the specific reason it was not chosen. "We didn't
think of it" is a fine thing to be able to write here later; do not
retrofit reasons no one had at the time.
```

Use `refines`, `extends` or `amends` on the newer record, and add the matching
`amended by`, `refined by` or `extended by` to the older one's Status line only.
The older record's text is never edited.
