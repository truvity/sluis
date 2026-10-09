# What do the terms mean?

Each term has one meaning and one owner page. The owner page holds the detail.

## Directory

A workspace is a directory tenant. A Slack workspace is a Slack team, always written in full and named by a key in the policy's `slack.workspaces` table.

| Term | Means | Owner |
|---|---|---|
| workspace | One directory tenant the service holds a credential for, identified by the backend's tenant id and never by a domain. | [Directory model](directory-model.md) |
| domain | Discovered from the workspace on every probe and never typed. Addresses route to workspaces by domain. | [Directory model](directory-model.md) |
| served | The discovered domains this service answers for. An unserved domain routes nothing and its accounts are never read. | [Directory model](directory-model.md) |
| synced | The groups the service keeps. All of them unless narrowed. | [Directory model](directory-model.md) |
| snapshot | The service's copy of one workspace, with the `snapshot_at` time on every answer. | [Freshness](freshness.md) |
| authoritative | A domain whose last probe succeeded, whose snapshot is inside the freshness window, and which no other workspace serves. | [Directory model](directory-model.md) |
| provisional | A served domain whose answers may add but never remove: first snapshot pending, snapshot stale or probe failed. | [Failure semantics](failure-semantics.md) |
| contested | A domain two workspaces serve. Authoritative for neither. | [Directory model](directory-model.md) |
| `max_age` | A caller's freshness demand. Omitted serves the snapshot, a value refreshes first, zero fetches now. | [Freshness](freshness.md) |

## Trust

| Term | Means | Owner |
|---|---|---|
| anchor | A root of trust a service verifies a caller against: the cluster or the issuer, never a third. | [Trust](trust.md) |
| proof | Something a service verifies without authenticating anyone: a sign-in's ID token, a CI token or a ServiceAccount token. | [Trust](trust.md) |
| the waist | The internal group name, the one currency of authorization, never re-mapped. | [Trust](trust.md) |
| federated issuer | An OpenID issuer whose tokens are accepted for exchange, trusted by its public key set alone. | [Verify and issue](verify-and-issue.md) |
| recovery | The way in when no directory can vouch: a ServiceAccount token checked by the API server. | [Recovery](recovery.md) |

## Policy

| Term | Means | Owner |
|---|---|---|
| internal group | The vocabulary of access. A caller holds one by directory membership or by a matcher. | [Policy](policy.md) |
| membership | One directory group inside an internal group. | [Policy](policy.md) |
| matcher | A condition on a verified proof: a CI repository and ref, a ServiceAccount, an address or its domain. | [Policy](policy.md) |
| claim fragment | What an internal group adds to a token. Fragments deep-merge, and a scalar conflict is refused at load. | [Policy](policy.md) |
| lifetime | The shortest across the caller's groups, then the client's cap. | [Policy](policy.md) |
| client | A relying party that asks for a token, declared by the deployment. Its `requires` says who may be issued one. | [Policy clients](../../reference/sluis/policy-clients.md) |
| resource | What a token is for when that is not the client, named with `resource` (RFC 8707). It has its own `requires`. | [Policy](../../reference/sluis/policy.md#resources--what-a-token-is-for) |
| scope | A role held over one workspace, the first segment of `<scope>:<thing>:<role>`. | [Trust](trust.md#naming) |

## Console and sessions

| Term | Means | Owner |
|---|---|---|
| exposure | A console behind a gateway: a hostname, a backend and, for proxied shapes, a posture. | [Native or gateway OIDC](../../guides/sluis/connect/choosing-native-or-gateway-oidc.md) |
| posture | What an exposure enforces: `authenticated` leaves the gate at the issuer, `groups` adds a gateway check. | [Native or gateway OIDC](../../guides/sluis/connect/choosing-native-or-gateway-oidc.md) |
| session | What the issuer holds for one identity and one client: a refresh token and how it was obtained. | [Sessions](sessions.md) |
| secret store | An OpenBao that trusts the issuer as a relying party and mints credentials. | [OpenBao](../../guides/sluis/connect/openbao.md) |

## Controllers

| Term | Means | Owner |
|---|---|---|
| binding | A row of the policy's `github` table. Every entry is an internal group. | [GitHub controller](github-controller.md) |
| link | The tie between a GitHub account and a person: `linked`, `lost` or `unverifiable`. | [GitHub controller](github-controller.md) |
| dry run | The state of every organisation or Slack workspace until the policy lists it as enabled. | [GitHub controller](github-controller.md) |
| held | A change the controller declines to make because a person is needed. | [GitHub controller](github-controller.md) |
| waiting | Slack only: nobody here needs to act. | [Slack reconciler](slack-reconciler.md) |
| reported | A fact the controller notes and never acts on. | [GitHub controller](github-controller.md) |
| retrying | A change that failed for a transient reason and is tried next pass. | [GitHub controller](github-controller.md) |
| breaker | A pass whose removals concern more than half of a unit removes nobody until an operator confirms that set. | [Slack reconciler](slack-reconciler.md) |
| runner App | The GitHub App a self-hosted runner scale set registers with, one per organisation per tier. | [GitHub controller](github-controller.md) |
| catalogue App | An App the deployment declares as data. Its grants name the groups that may ask for installation tokens. | [Integrations](integrations.md) |

## Slack

| Term | Means | Owner |
|---|---|---|
| policy channel | A channel bound in git and fed by internal groups, in `extend` or `strict` mode. | [Slack reconciler](slack-reconciler.md) |
| console channel | A channel managed on the console and fed by directory groups and addresses. | [Slack reconciler](slack-reconciler.md) |
| Slack Connect channel | A channel shared between your own Slack workspaces, kept as a console record. | [Slack reconciler](slack-reconciler.md) |
| no mixing | A channel is one kind. Defined as both, it is held on both sides. | [Slack reconciler](slack-reconciler.md) |
| owning directory | The connected directory that owns a Slack workspace or GitHub organisation, recorded at connection. | [Slack reconciler](slack-reconciler.md) |

## Audit

| Term | Means | Owner |
|---|---|---|
| audit installation | The [audit](../audit/README.md) installation that keeps the trail, in this application's namespace. | [Audit](audit.md) |
| action | One declared thing that can happen, such as `roster.person.signed_in`. | [Audit](audit.md) |
| record | One action that happened or was refused. Each is also a log line sharing its id. | [Audit](audit.md) |
| block | The one delivery that waits: a recovery sign-in is refused if its record cannot be kept. | [Audit](audit.md) |
| async | Every other delivery, queued in the process and retried with backoff. | [Audit](audit.md) |

## Decided in

- [ADR 0010: a declared vocabulary](../../decisions/0010-a-declared-vocabulary.md)
- [ADR 0019: two kinds of Slack channel, never mixed](../../decisions/0019-two-kinds-of-slack-channel-never-mixed.md)
