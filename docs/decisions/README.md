# Architecture decisions

One record per decision that shapes sluis from the outside: what a user must do, what an installation must accept, what a release removes.
A record says why the thing is shaped the way it is, what else was weighed, and what follows.
Explanation pages describe the target state and carry a status badge; a page marked `shipped` describes what shipped. When a page and a record disagree, the record is the reasoning and the page is fixed.

How a decision is made is in [GOVERNANCE.md](../../GOVERNANCE.md).

sluis shares its design with three neighbours, each with its own records: [atom](https://github.com/truvity/atoom/tree/master/docs/decisions) (ports, drivers and blocks), [audit](https://github.com/truvity/audit/tree/master/docs/decisions) and [session](https://github.com/truvity/session/tree/master/docs/decisions). A decision lives in one repository; the others link to it. A record here whose subject moved keeps its number and says where it went.

## Numbering

v2 numbering starts again at `0001`. `0000` is the [template](0000-template.md).
v1 had its own records on the `master` branch. Refer to them as "v1 ADR NNNN" and link to `master`, for example "v1 ADR 0007". Do not renumber them and do not copy them here.
A v2 record that replaces a v1 decision says so in its context.

## Files

A record is `NNNN-short-title-in-kebab-case.md`. A new record takes the number after the highest one in use, archived records included. A decision that is planned but not written has an issue and no number: prose names the issue, never a future number.

A superseded record is moved to [archive/](archive/) under its own number and name and is never edited again. It is not an index row; the [archive table](#archive) lists it. A record that is superseded only in part stays where it is, and its status says which record supersedes it (`Accepted; partly superseded by NNNN`); the new record names the superseded parts exactly.

## Format

Every record has these parts, in this order:

- **Status.** One of `Proposed`, `Accepted`, `Rejected`, `Superseded by NNNN`, `Accepted; amended by NNNN` or `Accepted; partly superseded by NNNN`.
- **Layer.** Optional. One of `core` or `add-on`: the layer of the product the decision belongs to ([0044](0044-sluis-is-a-core-and-add-ons.md)). A record with no Layer line is core, and so is a record that spans the layers, such as the runtimes or the release train. The layer `customer identity` was withdrawn by [0048](0048-product-apis-take-the-identity-providers-tokens.md); a record that carries it keeps it as decided (`customer identity`), and no new record takes it.
- **Date.** The day of the latest decision the record states.
- **Context.** The forces at play. What problem, what constraints, what the reader needs to know.
- **Decision.** What we do, in the active voice.
- **Consequences.** What gets easier, what gets harder, what must follow. Include costs.
- **Roads not taken.** Each other option that was weighed, one bullet each: the option in bold, then why it lost. This part is required.
- **Follow-ups.** The named tasks and planned records that follow, each with its issue, or "None.". A part that is still open is a line that starts `Open:`.

Under the status and the date, one line links the proposal issue: `Proposal: [issue #N](...)`.

## Rules

- **Records are append-only.** The records were rewritten once, cleanly, so that each one states the current design and stands alone; from that rewrite on, a record is not edited to change a decision. A changed mind gets a new record that supersedes the old one. The old record's status line changes to point at the new one and the record moves to the archive. Its body stays. When only a part is superseded, the record stays in place with the status line that says so, and the new record lists the parts.
- **Typos, links and built-or-planned marks** may be fixed in place. A record says what is built and what is planned; when something is built, the mark changes and nothing else.
- **One decision per record.** Keep it short. A page of reasoning is better than ten. A rule is stated in one record; the others link to it.
- **Accepted means decided.** An `Accepted` record has no open part: it does not say "not decided" or that another record will decide it. What it leaves out is under Follow-ups, with its issue, or as an `Open:` line. A record with an open decision at its centre is `Proposed`.
- **Public means generic.** A record names no private deployment, account, hostname, ticket or person other than the maintainer. Use neutral examples.
- **Draft ADRs are internal.** Reasoning starts in internal discussion. Only the final record is written here, once the maintainer has decided.
- **Standards.** A deliberate deviation from a standard in the register needs a record.
- **Scope.** New product scope, and changes to the design model, need a record.
- **Links.** A link to a record uses the site path, `/decisions/NNNN-slug/` or `/decisions/NNNN-slug/#heading`. CI checks that every "ADR NNNN" and every such link names a record that exists, and that every anchor is a heading in it.

## Index

The records, grouped in reading order. The Layer column is the record's own `Layer` line.

### The product, its lines and its actors

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0001](0001-v1-and-v2-lines.md) | v1 and v2 are two lines, releases of one major version work with the one before, and v2 replaces v1 by a parallel switch | core | Accepted |
| [0044](0044-sluis-is-a-core-and-add-ons.md) | sluis is a core and add-ons, and no core actor calls an add-on | core | Accepted; partly superseded by 0048 |
| [0002](0002-families-of-actors.md) | Actors form three families: work actors hold no credential at rest, and only the data and keys families hold credentials | core | Accepted |
| [0003](0003-units-transports-and-fusion.md) | One model, many transports: a unit per actor, the same calls over sockets, invokes and pods, and fusion only by explicit choice | core | Accepted |

### Data, keys, policy and boundaries

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0004](0004-the-data-model.md) | Data is kinds that sluis declares in the state registry, each with an address, one storage class, a treatment, a profile and one writer | core | Accepted |
| [0005](0005-the-store.md) | store@ is the thin host of atoom's state block: it holds every storage credential, selects the stores of each class, enforces sluis's rules and supplies signed configuration | core | Accepted |
| [0006](0006-keys-and-sealing.md) | key@ hosts atoom's seal block: it holds a boundary's keys, seals, opens and signs in place, with five data keys per tenant and one key per purpose and algorithm | core | Accepted |
| [0007](0007-policy-and-sealed-grants.md) | Policy is Cedar in a Rust actor over four layers, and every tenant-scoped write carries a grant that policy sealed | core | Accepted |
| [0008](0008-boundaries-and-tenants.md) | A boundary is the unit of keys and policy on a cumulative ladder from L0 to L4, and a tenant is a scope with data keys of its own | core | Accepted |

### Identity, tokens and sessions

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0017](0017-identity-and-sign-in.md) | Each boundary has its own issuer, a token has one audience, staff sign in with passkeys and step-up, and break-glass recovery uses passkeys held in config | core | Accepted |
| [0019](0019-person-and-upstream-link.md) | A person is tied to an upstream account by issuer and subject claim | core | Accepted |
| [0020](0020-grant-vocabulary-and-token-claims.md) | Grant names come from a declared vocabulary, and a token carries fixed claims for one audience | core | Accepted; partly superseded by 0048 |
| [0018](0018-sign-in-sessions.md) | Sign-in sessions have two classes with limits per class, and session chains are temp kinds | core | Accepted |
| [0021](0021-clients.md) | Clients: installation clients are config, tenant clients are durable, and a self-described client is admitted only from allow-listed hosts | core | Accepted |
| [0038](0038-fapi-2-profile.md) | FAPI 2.0 is a profile a client declares, with PAR, DPoP, private-key client authentication and its own signing algorithm, and sender constraint is declared per audience | core | Accepted |
| [0036](0036-volumetric-abuse.md) | Volumetric abuse is stopped at the edge first, and sluis bounds what one client or source can cost with a token bucket kept as a temp kind | core | Accepted |
| [0022](0022-workload-identity-and-host-enrolment.md) | Workload identity follows SPIFFE, and hosts enrol the same way | core | Accepted |
| [0041](0041-a-compromised-key-is-revoked-then-replaced.md) | A compromised key is revoked, then replaced: it leaves at once, its replacement signs at once, and only a person declares | core | Accepted |

### Customers and the group's identity provider

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0048](0048-product-apis-take-the-identity-providers-tokens.md) | Customers sign in at the group's identity provider, product APIs take its tokens, and sluis exchanges one only for calls down to infrastructure | core | Accepted |
| [0032](0032-customer-identity-installations.md) | Customers sign in through the upstream identity provider, realms follow boundaries, and a customer-identity installation serves one boundary whose tenants each have their own data keys | customer identity | Accepted; partly superseded by 0048 |

### Integrations and the console

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0025](0025-integrations.md) | Integrations are adapters, work actors that return facts or carry out authorised effects, hold nothing at rest, and mint only for declared targets | core | Accepted |
| [0026](0026-reconcilers.md) | A reconciler manages membership only, one per target under a lease | core | Accepted |
| [0027](0027-the-console.md) | The console checks delegation and routes writes, and its pages follow the storage class and profile of what they show | core | Accepted |

### Export, backup, KV and limits

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0009](0009-export.md) | exporter@ publishes the kinds marked exportable to destinations outside sluis, never to a secret source | core | Accepted |
| [0010](0010-backup-and-recovery.md) | A backup is a logical archive of the authoritative kinds through the store; a regional outage is accepted and a drill measures recovery | core | Accepted |
| [0047](0047-kv-is-a-core-service-of-stores-that-no-tenant-owns.md) | KV is a core service of stores that no tenant owns: policy decides by path, each store has its own data key, and External Secrets reads it through a Vault KV v2 subset | core | Accepted; partly superseded by 0049 |
| [0049](0049-ssm-stays-the-only-secret-store-for-now.md) | SSM stays the only secret store for now, and External Secrets reading sluis KV is a later option | core | Accepted |
| [0046](0046-limits-on-the-request-path-and-usage-in-audit.md) | Limits are checked on the request path, balances live in the product, and usage records go to audit | core | Accepted; partly superseded by 0048 |

### Runtimes, release, telemetry and the site

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0011](0011-runtimes-and-installations.md) | Runtimes are three kinds, four installations are tested, and every Lambda function runs behind the Web Adapter | core | Accepted |
| [0012](0012-the-language-follows-the-executable.md) | The language follows the executable: Go by default, Rust at the edge, in the trusted base and on the smallest boards, and the AWS-backed drivers are Go | core | Accepted |
| [0013](0013-release-train-packaging-and-distribution.md) | One repository, one version, one release train, and packaging follows the runtime | core | Accepted |
| [0016](0016-telemetry.md) | Telemetry is OpenTelemetry with fixed conventions, and a log level is raised at run time only with an expiry | core | Accepted |
| [0037](0037-capacity-is-measured-before-it-is-promised.md) | Capacity is measured before it is promised: a dated spike of the signing path, the hops to store@ and key@ and the engines, and tokens stay signed in place meanwhile | core | Accepted |
| [0014](0014-the-site-is-static-on-cloudflare-workers.md) | The site is static on Cloudflare Workers, deployed by GitHub Actions, with a live status API, and it serves the whole family | core | Accepted |

### Add-ons

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0023](0023-certificate-authorities.md) | The ca and ssh-ca actors are the certificate authorities; every leaf has a fixed profile and a short lifetime | add-on | Accepted |
| [0024](0024-root-custody-and-the-signing-api.md) | sluis holds no root: it asks root custody to certify its intermediates, and signs artifacts through a signing API | add-on | Accepted |
| [0042](0042-sluis-syncs-company-issued-openpgp-keys-to-staff-github-accounts.md) | sluis syncs company-issued OpenPGP keys to staff GitHub accounts | add-on | Proposed |
| [0043](0043-a-person-binds-their-own-keys-to-their-identity-in-sluis.md) | A person binds their own keys to their identity in sluis | add-on | Proposed |

### Moved to truvity/audit

| ADR | Decision | Layer | Status |
|-----|----------|-------|--------|
| [0028](0028-audit-pieces.md) | Audit is its own product, truvity/audit: its pieces, transport, search and front moved there | core | Accepted |
| [0029](0029-the-audit-log.md) | The audit log is audit's: the tiled log, sealing, witnesses, locks, pseudonyms, retention and tiers moved to truvity/audit | core | Accepted |
| [0030](0030-audit-records-and-the-usage-stream.md) | Audit records are OCSF events and usage is a separate stream: both moved to truvity/audit, and sluis keeps its action table and its `unmapped.sluis` fields | core | Accepted |
| [0040](0040-meters-and-usage-evidence.md) | Meters and usage evidence moved to truvity/audit: sluis declares its meters through audit's `Meter` kind | core | Accepted |

## Archive

Superseded records, kept as they were decided, under their own numbers. The site redirects the old addresses; [renumbered.json](renumbered.json) is the map.

| ADR | Decision | Status |
|-----|----------|--------|
| [0031](archive/0031-ciam-is-a-broker.md) | CIAM is a broker: a customer-identity product owns the accounts, sluis owns the session and the tokens, and the contract between them is written down | Superseded by 0032 |
| [0032](archive/0032-customer-identity-installations.md) | A customer-identity installation is a front and a few customer boundaries whose tenants each have their own data keys, a tenant's client IDs carry its name, and a move keeps every subject | Superseded by 0032 |
| [0033](archive/0033-customer-connections.md) | An installation federates to a fixed set of upstreams that its operator declares, and a customer installation sees exactly one | Superseded by 0032 |
| [0034](archive/0034-end-users-and-upstream-signals.md) | An end user is a person with five states, erased by deleting its records, and upstream signals end sessions or suspend and erase | Superseded by 0032 |
| [0035](archive/0035-customer-pages.md) | Customer pages are few, branded per client and login domain, and localised per request | Superseded by 0032 |
| [0039](archive/0039-quota-allowance.md) | Quota holds an allowance per tenant that the product pushes, counts it down, refuses at zero and records every fact | Superseded by 0046 |

## Renumbered in the clean rewrite of 2026-11-21

Most records kept their number. These did not, or left the list:

| Old | Now |
|-----|-----|
| 0015 evolution and compatibility | merged into [0001](0001-v1-and-v2-lines.md) |
| 0018 sessions | [0018](0018-sign-in-sessions.md), renamed (the actor is `signin`) |
| 0031, 0033, 0034, 0035 | archived; [0032](0032-customer-identity-installations.md) holds the customer-identity design |
| 0039 quota allowance | archived; [0046](0046-limits-on-the-request-path-and-usage-in-audit.md) |
| 0044 sluis v2 is a core and add-ons | [0044](0044-sluis-is-a-core-and-add-ons.md) |
| 0045 customers sign in upstream | claim set in [0020](0020-grant-vocabulary-and-token-claims.md), the rest in [0032](0032-customer-identity-installations.md) |
| 0046 usage records are never dropped | [0046](0046-limits-on-the-request-path-and-usage-in-audit.md); the usage class moved to audit |
| 0047 KV is an add-on | [0047](0047-kv-is-a-core-service-of-stores-that-no-tenant-owns.md), renamed |
| 0028, 0029, 0030, 0040 | keep their numbers as notes; the content is in [audit](https://github.com/truvity/audit/tree/master/docs/decisions) |

An "ADR NNNN" in an issue or a commit from before 2026-11-21 means the number the record had then; the old addresses redirect.
