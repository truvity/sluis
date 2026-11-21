# 0026 — A reconciler manages membership only, one per target under a lease

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #1012](https://github.com/truvity/sluis/issues/1012).

## Context

A `Provisioner` makes an external system equal to a desired state that is stored as data ([0025](/decisions/0025-integrations/#the-contracts)): teams, chat channels, wiki groups and on-call rotations. [0025](/decisions/0025-integrations/) fixes that the adapter receives a desired state by digest and reports an outcome. It does not fix the method, the safety rules or the tests. v1 spread them over four controllers, each with its own breakers and dry runs.

The forces:

- An external system is changed by a process that uses a vendor credential, so a mistake is felt by real people. A check that cannot run is not a pass.
- More than one process acting on a target would fight.
- An unreadable directory must never look like an empty one ([0025](/decisions/0025-integrations/#what-an-integration-may-return)).
- A kind that two places could write must have one owner ([0004](/decisions/0004-the-data-model/)).

The desired state needs three answers besides the rule: which actor computes and writes it, where it is stored when a container holds people of several companies, and what makes a stored part safe to act on. They are under [The desired state](/decisions/0026-reconcilers/#the-desired-state).

## Decision

### Scope

- **A reconciler manages membership only,** and membership means four things: who is in a team, a channel, a group or a rotation; the role a member holds in the container (member or administrator); what a group may do in a sub-container of the vendor, such as a collection of a wiki, which is a grant of access to the group and not the content; and whether a person's vendor account is active or suspended. It creates or adopts the container by name, invites, adds, promotes, demotes, removes and suspends people, and reactivates a suspended account whose person is vouched for again. It never deletes an account. It does not manage the container's content, its settings or its sharing outside the organisation, and it reports risky settings it sees without changing them.
- **A reconciler is a job of an adapter.** The adapter of the vendor (`adapter-github` for teams, `adapter-slack` for channels) runs it with the connection of the tenant whose container it manages ([0025](/decisions/0025-integrations/#adapters-and-connections)).
- **One reconciler per target.** A target is one container namespace of one connection, such as the teams of a GitHub organisation or the channels of a Slack workspace. It is declared in the durable kind `ReconcileTarget`, which also lists the directory connections whose people the target may hold (its sources) and, for a Slack Connect channel, a `shared` flag. Exactly one process acts on it, holding the lease of the target. A `Lease` is a temp kind whose id starts with the adapter's name and names the tenant and the target ([0005](/decisions/0005-the-store/)). The process passes the fencing token, the version of the lease, with every call that changes the vendor, and the adapter refuses a call with a lower token.
- **It acts on a tick or on a wake.** The scheduler runs a pass per target under the lease: an in-process ticker, a systemd timer or a cloud scheduler, by installation. A pushed notification wakes one pass ([0025](/decisions/0025-integrations/#push-then-pull)); a periodic full pass runs as well.

### The contract

- **`Reconcile` takes** the digest of the desired state, the mode (`dry_run` or `apply`), the fencing token and a budget of changes for one pass. The desired state is the kind `DesiredState`, a temp kind of the tenant that holds names and is therefore sealed. The provisioner reads it through its data port by its digest and never asks core a question.
- **`Reconcile` returns an outcome** that is one of `done`, `held`, `refused` or `failed`, with a reason from the closed list of [0025](/decisions/0025-integrations/#what-an-integration-may-return), a list of changes (operation, subject reference, container reference, result), a list of held changes with their reasons, a list of drift (what the vendor holds that the desired state does not name, and the reverse), counts, and the digest of what it observed. No vendor text crosses the contract. The messages are in the package `sluis.integration.v2alpha1` ([Contracts and errors](/reference/contracts/)).
- **A dry run changes nothing and returns the same lists** that an apply would act on. A target is in dry run until the operator declares `enabled: true` for it in its `ReconcileTarget`.
- **A pass is idempotent.** A second pass on an unchanged desired state and vendor makes no change.
- **An operator can pause a reconciler.** The pause is a `Switch`, a durable kind that only tightens; deleting it returns to what config says ([0027](/decisions/0027-the-console/#writes)).

### The desired state

- **The directory adapter writes `DesiredState`.** It writes it under the lease it already holds for refreshing the directory of one connection, straight after each refresh. `adapter-upstream-oidc`, for example, writes it once per connection, which is once per company's Google Workspace. The adapter makes one batch call to `policy`, which asks for the members of these targets given these facts ([Policy](/reference/policy/)), and writes the answer. Writing the answer is executing, not deciding ([0025](/decisions/0025-integrations/#the-rule)). No actor and no scheduler is added.
- **A part is the desired state of one target from one source.** `DesiredState` is stored per target and source directory connection, and addressed by digest. A target with several sources has several parts.
- **The provisioner of the target reconciles to the union of the parts.** `adapter-github`, for a team, reads every current part of its target through its data port, takes their union and reconciles to it. It reads nothing else and asks core nothing.
- **A missing or stale part is unreadable, never empty.** A part is missing when a source the target lists has none, and stale when it has passed the time to live of the kind. The pass holds that source's people and does not remove them, and it reports the part. The rest of the target is reconciled as usual.
- **The owner of the container holds the parts.** All parts of a target are stored in the tenant that owns the container and sealed under that tenant's `data` key ([0006](/decisions/0006-keys-and-sealing/)). A company's GitHub organisation is stored in that company's tenant, and a Slack Connect shared channel in the tenant that owns (hosts) the channel. The provisioner opens only its own tenant's credentials, as every adapter does.
- **A company's people may be written into another company's tenant.** When a part's source directory belongs to another company, the adapter of that directory writes the part into the owner's tenant. It learns where from `policy`'s answer alone, which names each target and its owner's tenant; it never reads another tenant's `ReconcileTarget`. Only the declared people cross, and only their vendor account ids, such as a GitHub user id, each with the source tenant's audit pseudonym of the person ([Audit in both tenants](/decisions/0026-reconcilers/#audit-in-both-tenants)). This is a narrow cross-tenant write: one kind, between declared pairs only. `policy` seals the grant for the owner's tenant only for a declared pair, and key@ checks the grant as it does for any seal ([0008](/decisions/0008-boundaries-and-tenants/)). Both tenants audit the write.
- **Both sides declare a share.** A part from another company is written only when both sides have declared it: the owner's `ReconcileTarget` lists the other company's directory connection as a source, and the other company's reviewed policy allows its group to be provisioned into that target. With one side declared, nothing is written and the pass reports the missing declaration, because a tenant gets nothing from another tenant that is not declared.
- **Slack Connect is a shared target.** People of several companies share one channel. The `adapter-slack` of the owner reconciles it, with `shared` set on the target. A member of another company is added by a Slack Connect invitation, and that company's own admin approval or connection policy still applies.

### Sharing across tenants

- **External containers follow the container-owner model.** A GitHub organisation or team, a Slack channel or a Slack Connect channel is shared across tenants only as [The desired state](/decisions/0026-reconcilers/#the-desired-state) says: the tenant that owns the container holds the parts under its own `data` key, and another tenant's people are added only when both sides declare the share.
- **This is a membership share, not data sharing.** What crosses is the vendor account id of each declared person, with the source's audit pseudonym of them, and nothing else (REQ-OPS-076). sluis has no general sharing of data between tenants. Sharing beyond memberships exists only for a specific integration that declares its own contract on both sides.
- **A person is linked across tenants through the source connection, never through a subject.** A person has a different `sub` in each tenant, and the owner's tenant cannot compute or compare the source tenant's subjects. The provisioner therefore knows a person of another tenant only as a vendor identifier named by the part of one source connection, and that connection's directory is the person's own directory for every rule here (REQ-OPS-091).
- **The owner keeps the minimal reference only.** For a person of another tenant, the owner's tenant holds the vendor's account id that its container needs (in the parts, in a `HeldChange` and in what its provisioner observes), and never the source's directory data beyond it: no name, no e-mail address, no group, no subject (REQ-OPS-085). The one other value a part carries is the source's audit pseudonym of each person, which only the source can make or reverse. A Slack Connect invitation that the vendor sends by e-mail uses the address only in the call that sends it, and no item of the owner's tenant stores it.

In the next three sections, tenant A owns the container and tenant B owns a source directory connection that A's `ReconcileTarget` lists.

### Propagation from a source to the owner

- **B's directory adapter learns of the change.** It reads B's directory on its interval, 15 minutes by default, and a push from the vendor starts a read sooner ([0019](/decisions/0019-person-and-upstream-link/)). Straight after the read it writes the parts of every target B's connection feeds, A's included, into the tenant of each owner. There is no other path: B never calls A's provisioner.
- **A's provisioner watches the parts.** It lists `DesiredState` in A's tenant, then watches it from the listing's resume point ([the store port](https://github.com/truvity/atoom/blob/master/store/docs/store-port.md)). The parts of B are in A's tenant, so the watch is A's own and crosses no tenant. A delivered change to a part of a target wakes one pass of that target under its lease. The provisioner is level-triggered: a delivery only says that the target may have changed, and the pass re-reads the current parts and reconciles to them idempotently (REQ-OPS-034). A repeated delivery costs a pass that changes nothing, so no dedupe key is needed (REQ-OPS-086).
- **The periodic full pass is the backstop.** Every target gets a full pass at least once per `full_pass_interval`, a tunable of its `ReconcileTarget` with a default of 15 minutes and a ceiling of one hour built into the release (REQ-OPS-087, REQ-OPS-088). It catches a change that a lost stream, an expired resume point or a restart missed, and it is the only path on Lambda, where nothing is pushed.
- **A removal reaches the container within one full-pass interval of the part that carries it.** The pass applies it or holds it under the breaker, and reports a held removal (REQ-OPS-089). A person suspended or deleted in B's directory is removed within B's read interval plus A's full-pass interval: 30 minutes with the defaults, and usually the read interval plus seconds while the watch is up. A person who is merely absent is `left` only after `N` successful reads (3 by default, [0019](/decisions/0019-person-and-upstream-link/)), so that removal takes `N` read intervals more. These are design figures from the defaults, not measurements; the integration tests measure them.

### When a source tenant exits

B's exit must destroy B's personal data ([0008](/decisions/0008-boundaries-and-tenants/)), and a restore must not bring it back ([0010](/decisions/0010-backup-and-recovery/)). The parts B wrote into A are sealed under A's key, so B's crypto-shred does not reach them, and B's people would stay in A's container. The exit therefore ends every share B is the source of, before the shred.

- **B's exit withdraws every part it wrote into another tenant.** When B is `erasing`, B's directory adapter replaces each such part with a **withdrawal**: a part whose members are marked withdrawn. It writes it as it writes every part, from `policy`'s batch answer, which lists the members of each target as usual, marks every one withdrawn for a connection whose tenant is erasing, and gives the grant for the owner's tenant (REQ-OPS-081). The adapter never reads the part it replaces. The withdrawal is written with the maximum lifetime of `DesiredState`, so that it outlives the wait below. Deleting the parts would not do: a missing part is unreadable and holds B's people in place.
- **A withdrawal is B's directory ceasing to vouch.** A's provisioner removes every person that a withdrawal names and no other current part of the target names, in `strict` and in `extend` mode alike (REQ-OPS-082). `extend` keeps the members that sluis does not manage; a withdrawal names only people that B's share put in the target. A person that another source still names stays, a container owner is reported and not removed, and the breaker holds a pass that would remove too many ([Safety](/decisions/0026-reconcilers/#safety)).
- **The exit waits for the owners, within a bound.** Before the shred, B's exit waits until B's own audit log holds, for every withdrawal, the outcome of the owner's pass that applied it, or until one hour (the removal ceiling) plus a margin of 15 minutes for the delivery of audit records has passed since the last withdrawal was written (REQ-OPS-083, REQ-OPS-089). B never reads A's configuration, so the bound is the ceiling, not A's own interval. B's log already holds the write of each withdrawal (REQ-OPS-077), so the exit knows how many outcomes to wait for.
- **What was not removed is an accepted residual.** A removal that the breaker held, that the vendor refused, or that did not happen within the bound, and a person the external system keeps (a container owner, or a Slack Connect member whom the other workspace keeps as its own), is recorded in B's exit record as a residual: the target and the count, never the people (REQ-OPS-084). It is data outside sluis, an accepted risk of [0008](/decisions/0008-boundaries-and-tenants/), and A's operators see the same held change on their console.
- **What A keeps afterwards.** The withdrawal parts expire with the kind's maximum lifetime, and a `HeldChange` lasts until A's operator confirms or drops it; both hold vendor account ids only. A's audit log keeps its records, which name B's people by A's own pseudonyms. A's `ReconcileTarget` still lists B's connection, so every pass reports a missing part for it, which holds no one, until A's operator removes the source. A change applied after B's shred is audited in A's log only, because B's pseudonym key is gone.

### Audit in both tenants

sluis emits these records through audit's emitter ([0002](https://github.com/truvity/audit/blob/master/docs/decisions/0002-pieces-and-holders.md)); the sealing and the pseudonyms are audit's ([0011](https://github.com/truvity/audit/blob/master/docs/decisions/0011-evidence-encryption-and-pseudonyms.md)).

- **A change in A's container that B's part caused is audited in both logs.** A's provisioner, which made the change, emits two records: one for A and one for B. audit seals each under the `evidence` key of the tenant it names, so each tenant reads its own record and neither reads the other's (REQ-OPS-090). A held change and a withdrawal's outcome are changes for this rule.
- **Each record names the person as its own tenant knows them.** A record names a person by a pseudonym under the `pseudonym` key of the tenant whose log receives it, so each tenant's reversal role works on its own log and on no other.
  - **B's record names the person by B's own subject.** A's provisioner cannot know it, so B's directory adapter, which knows the person from its connection, makes the pseudonym under B's key when it writes the part and puts it in the part beside the vendor account id. The provisioner copies it into B's record unchanged. A cannot reverse it and learns nothing from it that the account id does not already say.
  - **A's record names the person by A's pseudonym of the vendor account,** the external account identity of audit's pseudonym rule, which the provisioner asks key@ for under A's key. A's reversal role answers for it given the account id.
  - **Neither record carries the other tenant's data beyond the minimal reference** (REQ-AUDIT-165). A's record names B's source connection by its id; B's record names A's target by its id. B's record of a held change covers B's people only, and never says how large A's container is or what state A's breaker is in.
- **B's record is what B can show.** It is how B proves to its own people and auditors what happened to them in a container B does not own, and it is what B's exit waits for.

### Safety

- **A removal needs a vouching directory.** A person is removed, demoted or suspended only when their own directory, the one that sluis reads for them, has stopped vouching for them and their account is tied to a person ([0019](/decisions/0019-person-and-upstream-link/)). A person who is absent from another source's part is not removed for that. `UNAVAILABLE`, a stale fact and a missing or stale part never remove, demote or suspend anyone: the pass holds those changes for that source's people, and the rest of the target is reconciled.
- **An answer that is stale or not vouched for may add a person to a managed group, and never promotes** (REQ-OPS-092). It does not add anyone to a group that confers an administrator role, and it never removes, demotes or suspends. A fact that is unavailable adds no one.
- **The breaker holds a pass** that would remove more than half of a container's members, remove, demote or suspend the last active administrator (REQ-OPS-093), demote more than one administrator (REQ-OPS-094), or suspend more than 3 accounts or more than the limit the target declares (REQ-OPS-096). A target that declares no limit is guarded by the default of 3, or exceeds the vendor's per-pass limit. The thresholds are declared tunables of the `ReconcileTarget`, with these defaults. A held change is a `HeldChange`, a durable kind of the adapter, and it waits for an operator's confirmation; the confirmation is a write that the console routes to the adapter that owns the target ([0027](/decisions/0027-the-console/#writes)).
- **The account that holds the credential stays an administrator.** A reconciler never demotes or suspends the vendor account whose credential it uses, whatever the desired state says; it reports the change as held (REQ-OPS-095). Some vendors have no service account, so an API key acts with the role of the person who made it, and that person can be demoted or suspended from outside.
- **The credential is checked before the pass.** At the start of every pass the provisioner confirms that the connection's credential still holds the vendor role the pass needs, and holds the whole target when it does not (REQ-OPS-097). This applies to the permissions of a GitHub App and the scopes of a Slack token as it does to a wiki key.
- **A confirmation names its change set and lapses.** It confirms the held changes it lists and no others, and it lapses within 24 hours (REQ-OPS-098), so a confirmation cannot approve a different change later.
- **A container owner is reported, never removed.** A leaver still present is reported.
- **Two modes for a container:** `extend` only adds; `strict` also removes whom the desired state does not name.
- **Adoption is explicit and holds on conflict.** A container found by name is adopted only when the `ReconcileTarget` declares it; a container that is both bound in config and made on the console is held, and nothing changes on it.
- **Channels bound in reviewed policy and channels made on the console are two kinds with two owners** and never mix. The first are `ReconcileTarget` (durable), the second `ConsoleChannel` (durable, written by the chat adapter).
- **A pass is audited:** its outcome, its digest and counts, and every change, including each held one ([0025](/decisions/0025-integrations/#splitting-by-credential)).

### Tests

- **Every provisioner passes one conformance suite against a fake vendor server:** idempotence, dry run writes nothing, a lower fencing token is refused, `UNAVAILABLE` removes no one, the breaker thresholds (the last administrator, more than one demotion, the credential's own account, the suspension limit), no promotion on an unvouched answer, a degraded credential holds the target, a confirmation that names another change set releases nothing, `extend` and `strict`, adoption and conflict, and no vendor text in an outcome. A provisioner that fails any of them is not released.
- **The fake names the vendor release and the date whose behaviour it models** (REQ-TEST-012), because a fake that is not tied to a release keeps passing after the vendor changes.

### Built and planned

Planned: all of it. The kinds `ReconcileTarget`, `DesiredState`, `HeldChange`, `ConsoleChannel` and `Lease` are declared in the kind registry. The `Provisioner` contract, the batch membership method of `policy`, the provisioners and the conformance suite are not built. See [the roadmap](/roadmap/).

## Consequences

- A new vendor is a new provisioner plus its fake server and the suite; the rules above are not written again per vendor. Per-vendor behaviour stays on the integration pages.
- Two processes never act on one target; a failover waits for the lease to expire and a stale holder cannot write after it.
- A mass removal needs a person. The price is a held pass when a directory has a real large change, such as a reorganisation; the operator confirms it once.
- A reconciler runs for a tenant with the credential of that tenant's connection, opened at key@ for each call, so a pass over many tenants costs many opens.
- A directory refresh ends with one batch call to `policy` and one sealed write per target and source, so the cost of the desired state follows the directory's refresh, not the passes of the provisioners.
- A target in a shared container needs two reviews: the owner's `ReconcileTarget` and the other company's policy. A share that one side has not declared is reported on every pass and changes nothing.
- A change in one company's directory reaches another company's container in about the directory's read interval while the watch is up, and within that interval plus one full pass in every case, Lambda included. Each target costs a full pass at least once an hour, whatever the watch delivers.
- A company's exit reaches every container it shared people into, before its keys are shredded, and waits at most an hour for the owners. What an owner's breaker or vendor keeps is a residual that the exit names by count. An `extend` container loses the people that an exited company's share put there.
- A change across tenants writes two audit records, one per tenant: the source's names the person by its own subject, the owner's by the vendor account, under two keys, so the two cannot be linked without both tenants' reversal roles. A part carries one pseudonym per person besides the account id, made at the source's key@ on each write.
- A wiki controller needs the vendor's roles, group grants on collections and account suspension, so a reconciler's reach is wider than a team or a channel. A pass can cut a person off from every container of a vendor, and the breakers, the vouching rule and the credential check exist for that.
- The requirements are REQ-OPS-031 to REQ-OPS-037 (the reconciler), REQ-OPS-068 to REQ-OPS-077 (the desired state), REQ-OPS-081 to REQ-OPS-098 (sharing, exit and safety), REQ-AUDIT-165, REQ-ISO-117 and REQ-TEST-012.

## Roads not taken

- **Reconcile settings and content as well as membership.** It widens what a vendor credential must be allowed to do and what a bug can break; membership is where the directory's authority is.
- **Leave roles, collection grants and suspension to the vendor's own console.** A person who leaves would keep their access until someone remembered to remove it, and sluis ends only its own sessions, so a wiki account would stay valid until the wiki is told.
- **Let the wiki's own session lifetime bound a leaver's access instead of suspending the account.** The account and any API key of the person stay valid, and the bound would be a vendor setting that sluis does not control.
- **Treat a stale answer like a vouched one for promotions.** A wrong answer would make an administrator; it may add a member and never promote.
- **A confirmation that stays valid until it is used.** It could approve a different change made days later.
- **A reconciler per vendor with its own lease rule.** Each would invent its own safety rules, as v1's four controllers did.
- **No breaker: trust the desired state.** A directory error or a policy mistake would remove a whole team before anyone saw it.
- **Remove whoever the desired state does not name, in every mode.** Channels and teams that people use for other purposes would lose members silently; `extend` exists for them.
- **A database advisory lock as the only guard.** It gives no fencing token, so a paused holder could write over its successor.
- **The console writes the held-change confirmation itself.** It would write a kind that the adapter owns and validates; the console writes only `Switch` and `LogLevelOverride`.
- **`exchange` writes the desired state.** It puts a batch load on the sign-in path and gives `exchange` a second job.
- **A new work actor for it.** An extra actor and scheduler, for what the lease of the directory refresh already gives.
- **The target's adapter computes its own list.** A vendor would read another vendor's directory, repeated per vendor, which is v1's four-controller shape.
- **Store a part in the tenant of its source directory, and let the provisioner open the other tenant's key.** It would widen every provisioner and break the rule that an adapter opens only its own tenant's credentials.
- **Leave the writer open.** The reconcilers would have nothing to reconcile to.
- **General sharing of data between tenants, with a cross-tenant grant per kind.** Every kind would become a possible path between companies, and each share needs a review on both sides that only an integration's own contract gives.
- **Link a person across tenants by subject.** A person's item and its `sub` belong to one tenant, and a link across tenants would need a record that no tenant owns.
- **Let the source's shred alone end a share.** The parts are sealed under the owner's key, so the people would stay in the owner's tenant and container.
- **Delete the source's parts at its exit.** A missing part is unreadable and holds its people, so no one would be removed.
- **Remove a withdrawn person in `strict` mode only.** Every `extend` container would keep an exited company's people, against the exit's promise; `extend` exists for members that sluis does not manage.
- **Let the exit wait for the owners without a bound.** One owner's breaker or outage would hold another company's erasure forever.
- **Read the owner's interval to bound the wait.** The source would read another tenant's `ReconcileTarget`; the ceiling bounds every owner.
- **Watch only, with no periodic full pass.** A lost stream, an expired resume point or Lambda, where nothing is pushed, would leave a removal with no bound.
- **Full passes only, with no watch.** Every change would wait for the next pass, and a short interval to hide that would spend vendor calls on unchanged targets.
- **The source's directory adapter calls the owner's provisioner.** A call between adapters of two tenants, and a second channel beside the store that would need its own fencing and audit.
- **Name the person in the source's log by the vendor account.** The source's reversal role works by subject, so the source's people could not find their own records by who they are.
- **Dedupe watch deliveries by a key.** The provisioner re-reads the parts and reconciles idempotently, so a repeated delivery is harmless and a key would be one more thing to get wrong.
- **Audit a change across tenants in the owner's log only.** The source could not show its own people or auditors what happened to them, and its exit would have nothing to wait for.

## Follow-ups

- The batch membership method, `DesiredState` and the targets: [#1220](https://github.com/truvity/sluis/issues/1220). The watch of `DesiredState` (its `watched_by` entry in the kind registry), `full_pass_interval`, the withdrawal and the two audit records arrive with it, with their first consumer.
- The first provisioners: GitHub teams [#808](https://github.com/truvity/sluis/issues/808), Slack channels [#812](https://github.com/truvity/sluis/issues/812) and [#813](https://github.com/truvity/sluis/issues/813), and the wiki controller [#815](https://github.com/truvity/sluis/issues/815), which also settles the shape of a `ReconcileTarget` for the grants on a sub-container and for the roles of a connection with several namespaces.
- The scheduler and lease: [#801](https://github.com/truvity/sluis/issues/801).
- Open: how the Slack Connect provisioner obtains the e-mail address for an invitation without storing it ([#813](https://github.com/truvity/sluis/issues/813)).
