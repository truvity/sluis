# 0016 — Data has an origin and a sensitivity

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (alignment with 0007, 0058).

**In short:**
- Every record kind has one origin (declared, operator-set, learned, derived, issued) and every field one sensitivity (public, internal, personal, secret, plus free text), written in the schema.
- The origin decides who writes a record, where it lives, whether it is backed up, what a restore does with it and whether it may leave sluis. The sensitivity decides what audit, logs and telemetry may say.
- Declared configuration locks its fields; operator-set records fill the rest and are written by the actor that owns the kind; an object defined in both is held.
- Authority is declared only, except Cedar policies linked from declared templates within a declared delegation policy.
- Declared configuration may live-reload as whole versioned snapshots; operator-set and durable issued records are backed up and rolled forward, with secrets archived sealed.

**Words used here:**
- *Generation*: a number that a tighten-only switch records; undoing the tightening needs a declared change that names a newer generation.
- *Pin*: an issued record that fixes an identity on first contact, such as the workspace an App was first installed into; a later contradiction is refused, not learned.
- *Trust registry* and *trust log*: the published list of trusted roots, certificates and attestations, and the append-only log that records changes to it.
- *Deny matrix*: the generated tests that prove each actor is refused every record, secret, key and socket its manifest does not grant ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)).
- *Identity treatment*: the handling of personal data in audit records, such as pseudonymisation (ADR 0040).

## Context

sluis keeps many kinds of data: the install file and the policy, records that an operator sets on the console, facts it reads from directories and vendors, an index it builds from its own log, and things it issues itself, from client secrets to sessions. Each of them raises the same questions. Who may write it? Where does it live? Is it in a backup, and what does a restore do with it? May it leave sluis? What may an audit record, a log line or a span say about it?

v1 answered these one kind at a time. v1 ADR 0018 (on `master`) said that sluis does not ask for what it already knows. v1 ADRs 0019 to 0022 found, one Slack case after another, that some configuration is made on the console and not in git, that such records are audited and backed up, and that an object defined in both places is held. v1 ADR 0036 made configuration immutable per instance and kept credentials and state live. v1 ADR 0075 added a recovery switch that the console can only turn off. Each answer was right for its case, and none said what the general rule was.

The early v2 design had three "data origins" for configuration: declared, learned and operator secrets. That set has three faults:

- It has no place for configuration that an operator makes on the console, such as choosing the chat channels an integration manages. The maintainer wants that pattern: a view in the console where a click configures a record.
- It calls things "learned" that sluis cannot rebuild: a GitHub App's private key from the manifest flow, a chat bot token, the workspace pinned at the first install. A restore cannot learn them again, and some are security pins.
- It covers configuration only. Backup, export, audit and telemetry kept their own lists of what is in and what is out, and the lists disagreed.

The configuration schema is defined once in Pkl and shipped as JSON Schema ([0015](/decisions/0015-configuration-is-defined-once-in-pkl-and-shipped-as-json-schema/)). Every record kind has one writer, enforced by the state backend's own check of each caller, with no mediator ([0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/), [0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)). The permission manifest generates the access lists ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)). This record gives all of them one classification to read.

## Decision

### Every record kind has one origin and every field a sensitivity

- **A record kind declares one origin, in the schema.** A record kind is the unit that has one writer. Origin is a property of the kind, never of one field of a stored record. A domain object that has parts of several origins is split into several kinds, each with its own writer: an OIDC client's definition is declared, and its generated secret versions are issued.
- **Every field declares a sensitivity, in the schema.** The annotations are part of the generated JSON Schema and are checked in CI. The backup list, the deny matrix, the telemetry allowlist and the audit redaction are generated from them, not written by hand.
- **The audit log and non-extractable keys are outside the model.** The log is the record itself, a blob with a lock mode ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). A key in the `keys` port never leaves its store and is not data that sluis keeps ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).

### The origins

Ask in order. The first "yes" is the origin.

1. **declared**: written by an operator in a reviewed file (git or infrastructure as code) and delivered read-only. Examples: the install file, policy, clients and resources, exchange trust, PKI roles, internal-group bindings, integration declarations, quota limits and enforcement rules, backup and export recipients, the card slot map override, tunables.
2. **operator-set**: chosen by a person at run time, through the console, the CLI or the API, as a lasting intent. It is an overlay on declared, allowed only for the record kinds that accept it. Examples: chat channels managed from the console, the owner of a connected workspace, runtime switches (turn recovery off, pause a reconciler, acknowledge a breaker), a directory tenant connected from the console, a quota limit lowered below the declared one, a secret put at run time.
3. **learned**: observed in an external system and rebuildable from it without a person. It reaches sluis by **pull** or by **push**. Examples: directory users and groups, group memberships, the members of a workspace, discovered channels, the installed-Apps list, the client metadata document cache, the membership that a customer identity product pushes.
4. **derived**: rebuilt from sluis's own records or log. Verification never trusts it. Examples: the search index and its cursor, quota meters and hourly aggregates, desired-state projections.
5. **issued**: sluis created it, or a vendor or a person handed it over once. It is **durable** or **ephemeral**.
   - Durable: generated client secret versions and their rotation metadata, revocations, grants, the certificate issuance ledger and revocation state, ceremony outputs in the trust registry, vendor-issued secrets (an App private key, a bot token), pins (the workspace at first install, the first subject, an App id and installation id), account links, approvals and consents, quota balances and top-ups.
   - Ephemeral: sessions, refresh tokens and their spent markers, authorization and device codes, pending sign-in state, short-lived minted credentials, leases, dedupe records, the maintenance flag.

An act is not configuration. An approval, the confirmation of a held removal, a consent or an account link is an issued record of that act. A lasting choice is operator-set.

A secret that an operator supplies is not an origin of its own. A secret **referenced** from a declared field lives in the operator's own store and sluis never archives it. A secret **put** at run time (`sluis secrets put`, a paste on the console) is an operator-set secret in sluis's own store; it is the only copy, so it is backed up.

### The sensitivities

| Level | Meaning | In audit records | In telemetry | In logs (stderr) | In `config explain` |
|---|---|---|---|---|---|
| **public** | Meant to be published: key sets, CA certificates, discovery, trust bundles | yes | only if allowlisted | yes | yes |
| **internal** | Not published, not personal, not secret: client ids, channel names, App ids, digests, revisions | yes | only allowlisted identifiers of bounded cardinality | yes | yes |
| **personal** | About a person who can be identified: an email, a subject, a group membership, a display name | yes, with identity treatment (pseudonymisation is ADR 0040) | never | redacted by default | only to roles that may see it |
| **secret** | Credential material that is read | never; a fingerprint and a revision only | never | never | the reference only |

- **A field may also carry the free-text flag**, for text a person typed: a reason, a description, a display name. Free text never enters telemetry, and its length in an audit record is bounded.
- **A secret lives behind the `secrets` port**, never as a plain record in `state`. A sealed-state driver keeps ciphertext in `state` and is a `secrets` driver.
- **Personal data in an archive is reachable by erasure.** Erasure by destroying a key (crypto-shredding) has to reach backups as well as live records.

### Where each origin lives and who writes it

| Origin | Where it lives | Its writer |
|---|---|---|
| declared | A configuration source: files, a ConfigMap, a Lambda layer, AWS AppConfig; signed fragments merged by the scope each publisher owns; digest-addressed blobs for desired state | Nobody at run time. The operator, through review and deployment |
| operator-set | `state`, in a namespace of its own per kind, revisioned, written with compare-and-set; secret parts behind `secrets` | The **owning actor** of the kind, on a request through the console API |
| learned | `state`, in the namespace of the integration that observed it, with the time it was observed and a maximum age; or memory | The integration actor that observed it |
| derived | `index`; `state` for meters and projections; caches | The actor that derives it (the indexer; `quota` for meters) |
| issued, durable | `state` records; `secrets` for values; the trust registry and trust log for ceremony outputs; `blob` for published revocation lists | The issuing actor, one per kind |
| issued, ephemeral | `state` with a short lifetime; memory | The owner of the protocol (`session` for sessions and codes, `exchange` for enrolments, the lease holder) |

- **Operator-set writes are done by the owning actor**, which validates them with its own logic. The console API is the `console` actor, behind `entry` ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). It checks the operator's delegation and routes each write to the actor that owns the kind. The permission manifest declares which kinds accept operator-set writes, and the state backend's own per-caller check refuses any other writer.
- **Namespaces are per origin and trust class, with one writer per kind.** When pieces are combined in one installation they share driver instances ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). Operator-set records, learned facts, issued records and sessions then sit in separate namespaces, and the deny matrix generated from the origin annotation keeps each writer to its own kind.
- **Every operator change is authorised for a concrete person.** It is permission-checked and audited under that person's name. There is no extra signature on an operator write in 2.0.
- **Every operator-set kind has one owning actor, and it is that kind's only writer.** The owner may be a core actor (`session` for the recovery switch) or an integration actor that owns an operator-set kind of its own (the chat integration for its console-managed channels).
- **No actor writes learned and operator-set data of the same object.** An integration writes the facts it observed as a learned kind, and an operator's choices about the same object form a separate operator-set kind with its own owner; when one object seems to need both, it is split into two kinds.

### Learned by pull or by push

- **Pull** is a scheduled or on-demand read of the external system by the integration actor.
- **Push is notify, then pull.** A small notification reaches `entry`, which routes it to the integration actor. The integration reads the full state from the product's own API and writes what it read. A periodic full re-sync runs as well, so a lost notification costs freshness and not correctness. The notification carries no data that sluis stores.
- **The pushing product authenticates with its workload identity** and may touch only its own organisations. A customer identity product that pushes membership is the first case.
- **A SCIM 2.0 server is a later option.** It is not part of this record.

### Precedence

- **Declared locks.** When a declared value sets a field that also accepts operator-set, the declared value is effective, the console shows the field as locked, and an operator write to it is refused.
- **Operator-set fills what declared leaves open.** The order is: declared value, then operator-set value, then the schema default.
- **Tighten-only switches are the one exception.** A field marked tighten-only may be moved by an operator in its safe direction even when declared sets it: recovery off, a reconciler paused, a lower quota limit. Moving it back is a declared change that names a new **generation**, one that was never tightened. A stale document cannot undo a tightening; a deliberate one can.
- **An object defined twice is held.** If the same identity (a channel, a tenant) exists as declared and as operator-set, both definitions are held: nothing changes on that object, and the console and `sluis config check --live` report it. The console refuses to create a record for an identity that declared already defines. Last writer wins and take-over are not used.
- **Adoption is an explicit step.** A declared fragment may carry `adopts: <kind>/<id>@<revision>`. On load, if the operator-set record is at that revision, sluis retires it in one audited step and the declared definition takes over. If the revision differs, both stay held.
- **Two declared fragments that define the same object** are resolved by scope ownership: only the fragment that writes outside its publisher's scope is refused.
- **Learned never configures.** A declared key for a value the product learns is refused at load, and the error names where the value comes from. A learned fact never overrides declared or operator-set. A mismatch between a declared expectation (an App's permissions, a bot's scopes) and what the vendor granted is **drift**: it is reported, and a pass that would act on it is held.
- **Pins refuse.** An issued pin that a later observation contradicts makes sluis refuse and revoke, not learn again.
- **Declared refuses retired fields; operator-set tolerates them.** A retired option in a reviewed file is refused ([0015](/decisions/0015-configuration-is-defined-once-in-pkl-and-shipped-as-json-schema/)). A retired field in a record that sluis wrote itself is read, ignored and never written again, because refusing it would make an installed record unreadable.
- **A console action that cannot be undone is an explicit opt-in.** It is off by default and checked against the vendor first.
- **The effective configuration is declared overlaid with operator-set.** Learned facts and issued records are data that configuration refers to, not configuration.

### Authority is declared

- **Authority is never operator-set.** A field marked as authority accepts only declared, and the schema generator and `sluis config check` refuse anything else. Authority is every input of `policy`, every claim of a token or credential, internal-group bindings, infrastructure grants, exchange trust, PKI roles, OIDC clients and console roles.
- **The one exception is template-linked Cedar policies.** Policy logic stays declared: Cedar templates and a declared delegation policy that says who may link which template to what. Within those bounds the console creates and removes template-linked policies ([0014](/decisions/0014-policy-is-written-in-cedar-and-evaluated-by-a-rust-actor/)). The details, including how the links reach `policy`, are in ADR 0056.
- **Tighten-only actions are immediate.** Suspending a subject or revoking a grant takes effect at once and needs no review.
- **The console can render a change for review.** "Promote to declared" renders operator-set records, or an authority change, as a declared fragment. Nothing applies until the fragment is reviewed and deployed.

### Declared configuration and live reload

- **Layers apply inside declared only.** The order is defaults, runtime, install file, environment overrides. A later layer overrides an earlier one. Operator-set is an origin, not a layer.
- **Declared configuration may live-reload as whole versioned snapshots.** A snapshot may arrive through any channel: a file, a ConfigMap, a Lambda layer or AWS AppConfig. A new snapshot is validated as `sluis config check` validates it, swapped atomically and audited with its digest. A snapshot that fails validation is refused, and the version in force stays. The rule is "immutable per version", not "immutable per instance": no part of a version changes, and an instance moves from one whole version to the next.
- **Operator-set values are read live.** They are data read through `state` with a short cache whose lifetime is part of the field's schema, so a change on the console takes effect within that bound.
- **Telemetry is configured by the standard `OTEL_*` variables only** ([0015](/decisions/0015-configuration-is-defined-once-in-pkl-and-shipped-as-json-schema/)). It has no origin in this model, because it is not in the configuration.
- **Two classes of switch are kept apart.** Tunables (a log level, a timeout, the choice of a driver) are declared and may be set by an environment override. Runtime switches (recovery off, a paused reconciler) are operator-set, and tighten-only where they touch safety. Neither reaches authority.

### Checking and explaining

- **`sluis config check` and `sluis config explain` are offline by default.** CI runs them with no call to sluis.
- **With `--live` they read operator-set records through the console API.** `check --live` lists the objects that are held and the operator-set records that a new declared fragment would lock or hold. `explain` shows, for each field, its effective value, its origin and layer, and for an operator-set value the record id, the revision, who set it and when.

### Audit and telemetry by origin

| Origin | What the audit log records | Telemetry |
|---|---|---|
| declared | At each start and each reload: the digests of the fragments and of the policy, not their content | Allowlisted identifiers and digests |
| operator-set | Every create, update, delete and refused attempt: the person, the actor, the kind, the id, the previous and new revision, the full new value apart from secret parts, and the reason. A secret part appears as a fingerprint and a revision | Counts only |
| learned | Each pass and its outcome, with a digest and counts. A decision that used a fact records the fact, with personal data under identity treatment | Counts only |
| derived | Not per item; the indexer's cursor | Allowlisted aggregates |
| issued, durable | Every issuance, rotation and revocation, with enough data apart from secrets to replay it | Counts only |
| issued, ephemeral | Issuance and revocation | Counts and allowlisted identifiers |

Audit records never carry a secret. Telemetry never carries a personal, secret or free-text value, whatever its origin.

### Backup, restore and export by origin

| Origin | In the backup | After a restore | Export |
|---|---|---|---|
| declared | No: it lives in version control. The archive keeps the resolved document and its digest as evidence for the restore check | Redeployed from version control, then checked with `sluis config check` against the archived document | Rendered, not exported: rendered grants and vendor objects for the operator to apply |
| operator-set | Yes. Secret parts are archived sealed | Restored from the archive, then rolled forward from the audit log, merged per kind: replaced, or united for tighten-only switches | "Promote to declared"; through the `export` port only if the kind is marked external |
| learned | No | Learned again: a fresh pull before maintenance lifts, or a live read on the first miss | An authenticated read with a grant |
| derived | No. A snapshot may shorten a restore and is never evidence | Rebuilt after the roll-forward | The query API |
| issued, durable | Yes. Vendor-issued secrets are archived sealed | Restored, then rolled forward | Per kind, through the `export` port where the kind is marked external; public material by static publication |
| issued, ephemeral | No | Invalidated: everyone signs in again | No |

- **Vendor-issued and operator-set secrets are archived sealed, with an escrow ceremony for the sealing key.** A restore then needs no vendor ceremony, and the backup side never reads a secret. The escrow never copies a key that cannot leave its device ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)); the mechanism and the archive format are in ADR 0035.
- **Roll-forward covers every kind marked for it.** The audit record of each write to such a kind carries the full value apart from secrets. Operator-set records, tighten-only switches, pins, account links, approvals, quota balances, revocations, the issued parts of clients, grants, the certificate issuance ledger and revocation state roll forward.
- **A restore never loosens a tighten-only switch.** Its restore merge is a union, so a restore can only add a tightening, such as a disabled recovery generation.
- **A secret written after the last archive cannot be rolled forward,** because audit never carries secrets. The restore preview lists every secret written since the archive and raises an alarm, and the operator rotates or reconnects it.
- **Restore order:** check the configuration against the archived document, write the archive, roll forward, rebuild derived data, learn again, lift maintenance.
- **Whether a value may leave sluis is a property of its kind, whatever its origin.** A kind marked external is published through the `export` port. A secret referenced from declared configuration is exported by granting the consumer the operator's own path, not by copying it.

### Quota by origin

- Limits, enforcement rules and what happens on exhaustion or unavailability are declared. An operator may lower a limit on the console, tighten-only.
- Balances and top-ups are issued, durable, backed up and rolled forward from the admit, emit and top-up records.
- Meters and hourly aggregates are derived from the log and rebuilt after a restore.

### The schema annotations

Per record kind and per field, in the Pkl source and the generated JSON Schema. The names are illustrative; the generator fixes the spelling.

| Annotation | Values | Drives |
|---|---|---|
| `x-sluis-origin` (kind) | declared, operator-set, learned, derived, issued | writer rule, precedence, backup, restore order |
| `x-sluis-durability` (issued kinds) | durable, ephemeral | backup, invalidation on restore |
| `x-sluis-sensitivity` (field) | public, internal, personal, secret | port, audit redaction, telemetry allowlist, log redaction, `explain` |
| `x-sluis-free-text` (field) | true | telemetry exclusion, audit length bound |
| `x-sluis-authority` (field) | true | declared only |
| `x-sluis-accepts` (field of the effective view) | a list of origins | precedence |
| `x-sluis-tighten-only` (field) | the safe direction | the one override of declared |
| `x-sluis-external` (kind) | the export kind | `export` port, consumer grants |
| `x-sluis-restore` (kind) | replace, union, skip | restore merge |
| `x-sluis-roll-forward` (kind) | true | the audit record of each write carries the full value apart from secrets |

## Consequences

- One table answers who writes, where it lives, what is backed up, what a restore does, what may leave sluis and what audit and telemetry may say. The lists that each part kept by hand are generated from the schema, and a new record kind is classified once.
- The maintainer's click-to-configure pattern exists, bounded: an operator-set record is audited with its full value, backed up and rolled forward, and it can never grant access. A stolen operator session can tighten and can configure operational records. It cannot widen authority beyond what the declared delegation policy allows for template links.
- The console must show the origin and the lock state on every row, and the held objects. That is work for every console page that shows configuration.
- `sluis config check` and `explain` gain a `--live` mode, which needs operator credentials. The offline mode stays what CI runs.
- Live reload of declared snapshots replaces "a new value is a deploy" from v1 ADR 0036. The snapshot channel, the validation and the atomic swap have to be tested on every runtime. A field that a running instance cannot change in place, such as a driver choice, is applied at the next start; the schema marks such fields. (inferred)
- Backups hold more than before: operator-set records, pins, account links, approvals, quota balances, the issuance ledger and vendor-issued secrets. The archive format and the escrow ceremony become part of backup (ADR 0035), and a restore needs no vendor ceremony.
- Secrets written after the last archive are lost on restore and must be rotated or reconnected. The restore preview makes that visible.
- Wording changes across the site and the requirements: "learned secret" becomes "vendor-issued secret", "console-set" becomes "operator-set", "quota counters" becomes meters (derived) and balances (issued), and the requirements catalogue's own `origin` field is renamed `source` so that "origin" has one meaning.
- Reconcilers follow these rules for holds and adoption; the reconciler record (ADR 0028) keeps membership only, ticks under a lease and the two kinds of channel.
- This record succeeds v1 ADRs 0018, 0036 and 0067, generalises 0019, 0020 and 0075, and carries the general parts of 0021 and 0022. v1 ADR 0075 kept the recovery switch out of backups, so that an older archive could not reopen recovery; here it is backed up with a union merge, which keeps that property and also survives a restore into an empty installation.

## Roads not taken

### Three origins: declared, learned, operator secrets

- Pros: the earlier, shorter set. Familiar from the first v2 pages.
- Cons: no place for configuration made on the console. Non-rebuildable values are called learned. Configuration only, so backup, export and audit keep their own lists.
- Why not: it cannot express the click-to-configure pattern, and it gives a restore the wrong answer for vendor-issued secrets and pins.

### An operator-supplied secret as its own origin

- Pros: one word for "the operator gave us this secret".
- Cons: it mixes a sensitivity with an origin, and it hides the backup difference between a reference and a value put at run time.
- Why not: declared with a secret sensitivity (by reference, never archived by sluis) and operator-set with a secret sensitivity (put at run time, archived sealed) give the right backup rule without a new origin.

### Derived folded into learned

- Pros: four origins instead of five.
- Cons: learned is written by integrations from an external system, derived by core actors from sluis's own log. The rebuild source and the restore order differ, and the writer rule would need an exception.
- Why not: derived is its own origin.

### Declared seeds a value and the console overrides it

- Pros: familiar from many products.
- Cons: git no longer shows the effective value, and review loses its meaning.
- Why not: declared locks. A per-field choice between lock and seed was also weighed; it gives two behaviours to explain and test per field and drifts toward seeding.

### Last writer wins, or declared takes over an operator-set object automatically

- Pros: no window in which an object is managed by neither side.
- Cons: a git change silently erases an operator's record, or an operator silently undoes a reviewed change. v1 ADR 0020 removed exactly this take-over.
- Why not: an object defined twice is held, and adoption is an explicit step that names the revision it adopts.

### Refuse the whole configuration when two declared fragments collide

- Pros: the simplest rule: reviewed configuration is consistent or nothing runs.
- Cons: one publisher's mistake stops every other publisher's change.
- Why not: scope ownership makes a collision inside one's own scope impossible, so only the fragment that writes outside its scope is refused. Holding the object was also weighed; two reviewed sources disagreeing is a review failure, and holding would hide it.

### Operator-set authority with a second person's approval

- Pros: a console for everything, clients and grants included.
- Cons: it rebuilds a writable policy API, gives `policy` a state port and makes the approval flow the attack surface.
- Why not: authority is declared. The console links declared templates within a declared delegation policy, and anything else is rendered for review.

### Signed operator writes

- Pros: non-repudiation like a signed declared fragment, for example a passkey assertion over the record's digest.
- Cons: it needs passkeys at every operator write, and it adds friction to the pattern the maintainer wants.
- Why not: an authenticated, permission-checked write audited under a concrete person is enough for 2.0. A two-person approval for chosen kinds was weighed for the same reason and set aside.

### The console, the `console` actor or `entry` writes every operator-set record

- Pros: one writer for all operator-set kinds.
- Cons: one actor would validate records whose rules belong to another, and it would hold write access to every console-configurable kind.
- Why not: the owning actor writes its own operator-set records with its own validation. The `console` actor checks the operator's delegation and routes the write to it, and the manifest and the state backend's per-caller check enforce it.

### A separate store for operator-set records

- Pros: hard isolation from runtime state.
- Cons: a second store to run and back up on every installation; the boards would need a second replicated database. A blob of signed documents was also weighed; it has no compare-and-set per object, so concurrent console writes would need a lock.
- Why not: a namespace and a trust class of their own in the installation's `state`, enforced by the generated deny matrix.

### Three sensitivity levels: public, private, secret

- Pros: shorter.
- Cons: "private" cannot say whether a value is personal, which decides telemetry, erasure and identity treatment.
- Why not: four levels and the free-text flag express the telemetry rule ("nothing secret, personal or free-text") directly.

### Learned facts as part of the effective configuration

- Pros: one view of everything that affects behaviour.
- Cons: configuration would stop meaning operator intent, and the values that made learned look like configuration are issued pins.
- Why not: the effective configuration is declared overlaid with operator-set. Learned and issued data are referred to by configuration.

### Configuration immutable per instance, with no reload

- Pros: v1's rule (v1 ADR 0036). The simplest to reason about.
- Cons: every change to declared configuration is a redeploy, on every runtime, including a change delivered through a configuration service.
- Why not: whole versioned snapshots, validated, swapped atomically and audited, keep the property that matters, that no version changes in part.

### Vendor-issued secrets unsealed and re-wrapped for the archive, or left out of it

- Pros: re-wrapping uses the archive's own recipients and one archive key. Leaving them out keeps every secret out of every archive.
- Cons: re-wrapping gives the backup side read access to every secret. Leaving them out means every integration needs a person at the vendor after a restore, and every relying party a new client secret.
- Why not: sealed in the archive, with an escrow ceremony for the sealing key, keeps the backup side blind and a restore free of vendor ceremonies.

### A push that carries the full state

- Pros: no read back to the product.
- Cons: sluis would store whatever a notification says, and a lost or reordered notification leaves sluis wrong until the next one.
- Why not: notify, then pull from the product's API, with a periodic full re-sync. A SCIM 2.0 server stays a later option.
