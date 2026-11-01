# 0064 — Audit records are OCSF events with one `unmapped.sluis` object, and usage is a separate stream

**Status:** Accepted
**Date:** 2026-11-01

Decided by the maintainer on 2026-11-01: audit records are OCSF events of core classes, with the facts that OCSF has no home for in one namespaced object, `unmapped.sluis`, and with no OCSF extension in 2.0; usage (metering) is a separate stream. The record format was [OD-40](/open-decisions/#od-40). Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). Build: [issue #841](https://github.com/truvity/sluis/issues/841) (emitter, action table, metering) and [issue #915](https://github.com/truvity/sluis/issues/915) (quota). Points marked "(inferred)" are choices made while writing this record, for the maintainer to confirm.
This record takes the record formats out of [0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/), which keeps the log. v1's record format is dropped without compatibility or migration.
Rewritten in place on 2026-11-01, the day it was first written (the maintainer decided against an OCSF extension in 2.0: the sluis fields without a core home go in `unmapped.sluis`, validated by sluis's own JSON Schema; the action table lives in code and no catalogue document or catalogue version is stored; the audit export is Event Log Activity / Export and a step-up is a Logon with `is_mfa`; the actor chain of an exchange and an issued certificate are recorded in `unmapped.sluis`).

## Context

[0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/) decides how a log is laid out, sealed, locked, encrypted and kept. This record decides what is in a log entry: the audit record, and the usage event that travels with it.

- **OCSF is the security event schema that is maintained and consumed.** The Open Cybersecurity Schema Framework is a Linux Foundation project under Apache-2.0, with written compatibility rules: a minor release only adds, and only a major release removes. Its current release is 1.9.0 (2026-08-03), with about three minor releases a year and no 2.0 announced. It has core classes for authentication, user, role and entity management and API activity, a `security_control` profile for policy decisions, and a `record_integrity` profile. It is the native schema of AWS Security Lake and is read by the pipeline vendors. (Checked 2026-11-01 against the schema repository at tag `1.9.0` and the [1.9.0 schema](https://schema.ocsf.io/1.9.0/).)
- **Most of what sluis records has a core home.** Actor, session, source, device, target, certificate presented to a sign-in, token id and times, MFA, policy decision, tenant, correlation, outcome and the record-integrity fingerprint all have an exact or close core attribute in 1.9.0. A mapping of 2026-10-31 and a second look on 2026-11-01 found that the rest is a short list (below).
- **Some facts have no core home in 1.9.0.** The DPoP key thumbprint and the mTLS certificate thumbprint that bind a token to its sender; the audience and scopes of a token (the schema has no home on a token before 1.10, where an `audience` attribute is already merged); the `acr` and `amr` assurance claims; the kind, algorithm, digest algorithm and store of a key; key and device attestation; the digest of a configuration; the type and digest of an enrolment proof; a quota meter, amount and balance; the extended key usage and the certificate-transparency entry of a certificate; the critical options, extensions and CA fingerprint of an SSH certificate; and the approver of an impersonation. (Checked against the 1.9.0 schema on 2026-11-01.)
- **OCSF's text advises an extension for native producers, and that is advice.** The [OCSF FAQ](https://github.com/ocsf/ocsf-docs/blob/main/faqs/schema-faq.md) says that `unmapped` is not recommended for event producers and that "a native event producer should extend the schema", with "an extension ... preferred". The schema's own description of `unmapped` says that consumers should not rely on a stable structure in it and that the preferred approach is "a custom extension with the desired structure". Neither text forbids `unmapped`. The decision below uses `unmapped` on purpose, and the reasons are in the next section.
- **OCSF has no class for issuing a certificate or a token,** and none for using a key. Those actions use the nearest core class, as the mapping below says.
- **Billing and quota want a different record than security does.** A usage fact (an account, a meter, an amount) is read by quota and by finance, has its own retention and its own consumers, and has a usage-event standard of its own. OCSF has no usage class.
- **The audit log is sealed and verifiable,** and a hash must be reproducible by any implementation. That needs one canonical serialisation of a record (RFC 8785), and numbers that survive it.

The volume measured today is about 2.4 records a second at peak and about 75,000 a week. The threats this record answers are T-10, T-12, T-13, T-14 and T-26 of the [threat model](/security/threat-model/).

## Decision

### The version of OCSF is pinned, and an upgrade is a checked change

- **Each sluis release pins one OCSF version,** named in every record's `metadata.version`. The first release pins **OCSF 1.9.0**. A development version (`-dev`) is never pinned.
- **The pinned schema is part of the release.** The JSON Schema that the writer enforces is generated in CI from the pinned OCSF schema, the action table (below) and sluis's own rules, and ships inside the release ([The action table is code](/decisions/0064-audit-records-are-ocsf-events-with-one-unmapped-sluis-object-and-usage-is-a-separate-stream/#the-action-table-is-code-and-the-release-identifies-it)).
- **An upgrade moves one OCSF minor release at a time, as a deliberate pull request** (inferred: no more than one per sluis minor release). CI generates the old and the new schema, compares them under OCSF's compatibility rules, and fails when the action table or a field of `unmapped.sluis` uses an item the new version marks deprecated, or when a new core attribute has the name of a field of `unmapped.sluis` (REQ-AUDIT-089). A name match is a prompt for the reviewer, who decides whether the core attribute is now the home of the field: OCSF 1.10 is expected to bring `audience`, so the token audience will be such a case.
- **Records are never rewritten.** A log holds records of several OCSF versions and several sluis releases, each naming its own, and readers, the indexer and exporters handle each record by the versions it names.
- **A new OCSF major release needs a new record.** Until then sluis stays on 1.x.

### The action table is code, and the release identifies it

- **Each action's class and activity, its required fields, its sensitivity and its declared meters are a table in code.** The table also says, for each action, the paths of its subject, target and request id and which fields are filterable, facetable or searchable ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)), and the data origin and sensitivity of the record kind ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). Each emitting product (the sluis actors, and an application that uses the emitter) holds its own table in its own code. Its entries have a name, which is the action's `metadata.event_code`.
- **The JSON Schema 2020-12 of the records is generated from the table in CI,** together with the pinned OCSF schema and the schema of `unmapped.sluis`. The generated schemas are published with the release and compiled into the emitters and the writer. Types for Go, Rust and TypeScript are generated from the same source.
- **No catalogue document is stored at run time, and no record carries a catalogue field.** The record's `metadata.product.version` (the release of the product that emitted it) and its `metadata.event_code` identify the table entry in force, and `metadata.product.name` names the product. The log holds no schema entry, no witness cosigns a schema, and a log is no longer written with copies of the schemas. An auditor takes the schema from the published release a record names.
- **A writer must know the table of every release that emits to it.** It is upgraded before its emitters, as with any schema, and for an application that is not part of sluis its generated schema is installed beside the writer, selected by `metadata.product` (inferred: this is the one place where a runtime copy of a schema is needed; it is a release artefact the operator installs, not a versioned document in the log or in the record).

### The class mapping

The table of each action names one class and activity per action. Classes and activity ids were checked against the OCSF 1.9.0 schema on 2026-11-01. A record uses the core class and the core attributes wherever they exist, and `unmapped.sluis` only where OCSF 1.9.0 has no home.

| sluis action | OCSF class / activity |
|---|---|
| Sign-in (browser, passkey, upstream OpenID Connect) and recovery sign-in | 3002 Authentication / 1 Logon |
| Sign-out, end of a session | 3002 Authentication / 2 Logoff |
| Step-up of a session | 3002 Authentication / 1 Logon, with `is_mfa` true, the `auth_factors` used, the same `session.uid` as the session being raised, `is_new_logon` false and `activity_name` "Step-Up" |
| Token grant at `/token`, token refresh, token exchange, token revocation | 3002 Authentication / 99 with `activity_name`; the audience, scopes and sender binding of the token are in `unmapped.sluis`; an exchange names the actor chain in `unmapped.sluis.actor_chain`; an impersonating exchange is 7 Account Switch with `account_switch_type_id` 2 (Impersonate) and names its approver in `unmapped.sluis.approver` |
| A grant of scopes | 3003 Authorize Session / 1 Assign Privileges, with the scopes in `privileges[]` |
| A certificate (X.509 or SSH) issued or revoked | 3002 Authentication / 99 "Certificate Issue" or "Certificate Revoke", with the certificate in `unmapped.sluis.certificate` |
| A credential minted to a principal, such as a GitHub token | 3007 User Management / 18 Add Programmatic Credentials |
| A credential revoked | 3007 User Management / 19 Remove Programmatic Credentials |
| Enrolment of a node, a device or a workload | 3004 Entity Management / 6 Enroll, 7 Unenroll, with the proof in `unmapped.sluis.enrolment` and any attestation in `unmapped.sluis.attestation` |
| A passkey registered or removed | 3007 User Management / 12 Enable MFA Factors, 13 Disable MFA Factors |
| A user created on first sign-in, a roster change, a role assignment | 3007 User Management and 3006 Group Management, by activity |
| A console write (configuration, a client registration, a lowered limit) | 3004 Entity Management / 1 Create, 3 Update, 4 Delete, with the full value in `updated_entity.data` for roll-forward, the configuration revision in `entity.version` and the digest in `unmapped.sluis.config` |
| A quota call (`Admit`, `Confirm`, `Cancel`, `TopUp`, `Adjust`) | 6003 API Activity / 1 Create, 3 Update, 4 Delete, with the meter, amount and balance in `unmapped.sluis.quota` |
| An audit read (search, get, tail, proof) | 6003 API Activity / 2 Read |
| An audit export | 1008 Event Log Activity / 3 Export |
| Key use (sign, decrypt) | 6003 API Activity / 99 "Sign" or "Decrypt", with the key in `resources[]` and its kind, algorithm, digest algorithm and store in `unmapped.sluis.key` |
| A key created, rotated or destroyed, including a company key shredded | 3004 Entity Management / 1 Create, 3 Update, 4 Delete, with the key in `entity` and its kind, algorithm and store in `unmapped.sluis.key` |
| A configuration reload | 6002 Application Lifecycle / 99 "Reload", with the revision and digest in `unmapped.sluis.config` |
| Writer and log lifecycle | 1008 Event Log Activity |

- **The boundary is `metadata.log_name`,** the name of the log's environment boundary, and the same boundary is the suffix of `actor.user.uid`. The writer checks that it equals the log's boundary.
- **The configuration revision is `entity.version`** where the class has an `entity` (Entity Management). Application Lifecycle has none, so a reload names the revision it loaded in `unmapped.sluis.config.revision`.
- **A policy decision is not a record of its own:** it is part of the record of the action it gates, in the `security_control` profile (`action_id`, `authorizations`, `policy`). A record that uses it declares the profile.
- **A SIEM rule keyed on Logon sees a step-up, and does not see a token grant.** A step-up is a second authentication of a session that is already open, so a rule on failed logons finds a failed step-up, which is wanted. A token grant is not a logon.
- **Every class above is a core class,** so the record's `class_uid` is the core one, and no extension scopes it.
- **The token stages stay activity 99, and the Kerberos-named activities are not used.** Authentication has activities 3 Authentication Ticket, 4 Service Ticket Request and 5 Service Ticket Renew, and the class description was widened in 1.5.0 to cover OIDC and SAML stages. But the descriptions of 3, 4 and 5 still say Kerberos ticket, and a SIEM rule or an analyst who reads them would take an OAuth grant for a ticket-granting ticket. No vendor mapping read on 2026-11-01 puts OAuth token grants, refresh or exchange in them. Activity 99 with a name, a core class and an event code per grant type says what happened without a false name.
- **One event code per grant type** (authorization code, refresh, client credentials, exchange), so the action table implies the grant type and no field carries it.
- **A refresh rotation names two tokens in one record:** `session.uid` is the refresh family, `session.credential_uid` the presented token, and `authentication_token` the new one.
- **The actor chain of an exchange is not `delegation`.** In 1.9.0 `delegation` exists only through the `ai_operation` profile, which is for AI models and agents, and it holds identifiers only (`uid`, `parent_uid`, `issuer_uid`, `created_time`); the principals themselves are the other objects of the event. A record therefore names the acting party in `actor.user` and the subject in `user`, and any further hops of an RFC 8693 `act` chain, outermost first, as `<actor>@<boundary>` names in `unmapped.sluis.actor_chain`.
- **An issued certificate is not the `certificate` object of Authentication.** In 1.9.0 that attribute is "the certificate associated with the authentication", a certificate that was presented, so a sign-in with a client certificate uses it (with its fingerprint), and an issuance does not. The details of an issued X.509 or SSH certificate are in `unmapped.sluis.certificate`; the profile digest stays in `policy.version`, and a revocation reason is `status_detail`. A certificate-validation activity or class is under discussion in OCSF (issue 1513), and the field moves to a core home if one comes.
- **A passkey's credential id is a `resources[]` entry of the User Management record,** not a device.
- **A request id appears only where it is needed,** such as quota's idempotency, in `api.request.uid` on API Activity. The trace id joins the records of one request.

### The sluis fields: `unmapped.sluis`

- **Every sluis fact without a core home goes in one object, `unmapped.sluis`.** The base event has an `unmapped` attribute in every class of every OCSF version, so the object survives an exporter that maps between versions. Nothing of sluis goes in any other part of `unmapped`, and nothing else goes in `unmapped.sluis`. A field with a core home in OCSF 1.9.0 uses it.
- **`unmapped.sluis` is validated by sluis's own JSON Schema,** generated in CI from the action table. The schema is closed (no unlisted field), each action's entry names the fields it requires, and the writer enforces it like the rest of the record.
- **No OCSF extension in 2.0.** The extension, its registry uid, its compile step and its own version are not part of 2.0. A field of `unmapped.sluis` is versioned with the sluis release (`metadata.product.version`). Within 2.x a field is only added: it is never removed or retyped, except by a major version (REQ-AUDIT-105). When OCSF adds a core home for a field, the field is marked deprecated, new records use the core home, and old records keep the field.
- **The fields at first** (inferred; the generated schema is the exact list):

  | Field under `unmapped.sluis` | Type | What it says |
  |---|---|---|
  | `sender_binding` | object (`type_id`: 1 DPoP `jkt`, 2 mTLS `x5t#S256`, 99 other; `thumbprint`: the base64url thumbprint) | The confirmation key of a token |
  | `token` | object (`audience`: array of strings; `scopes`: array of strings) | The audience and scopes of the token that the record issues, refreshes or exchanges |
  | `assurance` | object (`acr`: string; `amr`: array of strings) | The authentication context class and methods of the session or token |
  | `key` | object (`kind_id`: token signing, X.509 CA, SSH CA, log, mint grant, tenant, data; `algorithm`; `digest_algorithm`; `store`: object of `type_id` software, cloud KMS, PIV token, other, and `slot`) | The kind, algorithm, digest algorithm and store of a key that was used, created or destroyed. The key's id stays in `resources[]` or `entity` |
  | `attestation` | object (`subject_id`: 1 key, 2 device; `format`; `digest`: a SHA-256 of the attestation statement) | The attestation of a key (such as a PIV attestation chain) or of a device or node. The statement is kept where the key or device is registered, not in the record |
  | `config` | object (`digest`; `revision`) | The digest of a configuration that was loaded or changed. `revision` only on a reload, whose class has no `entity` |
  | `enrolment` | object (`proof_type_id`; `proof_digest`) | The proof that a node, a device or a workload gave |
  | `quota` | object (`meter`; `amount`; `balance`) | The meter, the signed integer amount and the balance after a quota call, as a reader of the security log needs them. The usage stream stays the source for roll-forward and billing |
  | `certificate` | object (`kind_id`: 1 X.509, 2 SSH user, 3 SSH host; `serial_number`; `subject`; `sans`; `not_before`; `not_after`; `fingerprint`; `eku`: object identifiers; `ct_entry`; `ssh`: `key_id`, `principals`, `critical_options`, `extensions`, `ca_fingerprint`) | An issued or revoked certificate. `eku` and `ct_entry` are for X.509, `ssh` for the SSH kinds |
  | `actor_chain` | array of strings | The further hops of an RFC 8693 `act` chain, as `<actor>@<boundary>` |
  | `approver` | string | The `<actor>@<boundary>` that approved an impersonation or a break-glass action |

- **What stays out of `unmapped.sluis`:** the data origin and the sensitivity of a record kind ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)), which the action table entry that `metadata.event_code` names already says for every record of that action; the pseudonym key epoch, which is encoded in the pseudonym; the boundary and the configuration revision, which have core homes; and the roll-forward value of a console write, which is a free-form value typed per event code and stays in `updated_entity.data`, OCSF's own `json_t` slot.
- **Why sluis uses `unmapped` where OCSF's text advises an extension.**
  - No consumer needs typed sluis fields today. A SIEM that flattens JSON queries `unmapped.sluis.key.algorithm` as easily as an extension attribute, and sluis's own writer and reader are the only strict validators.
  - AWS Security Lake takes OCSF 1.3 or earlier for a custom source ([custom sources](https://docs.aws.amazon.com/security-lake/latest/userguide/custom-sources.html)) and has no extension to load, so an extension would be dropped there, while `unmapped` is part of every version's base event.
  - An extension needs an entry in the [public extensions registry](https://github.com/ocsf/ocsf-schema/blob/main/extensions.md), a source tree and a version, a compile step in CI and in every strict consumer, and an artefact to own at every OCSF release.
  - The vendor mappings read on 2026-11-01 (OCSF's own examples for AWS CloudTrail, Okta and GitHub, and the AWS pipelines for Okta, Entra ID and GitHub audit logs) map identity events into core classes and keep the rest in `unmapped`. None adds an extension for it.
- **An extension may be added later, in a minor release,** only when a real consumer needs typed fields. It is then a new record, it takes the fields that `unmapped.sluis` holds, and old records keep the shape they were written in.

### The record: lean, canonical and validated

- **An audit record is an OCSF event of the pinned version.** The sluis action is `metadata.event_code`; the producing product and its release are `metadata.product.name` and `metadata.product.version`; the company is `metadata.tenant_uid`; the log's boundary is `metadata.log_name`; the record id is `metadata.uid`; the outcome is `status_id` with the reason code in `status_code`; the trace id is `metadata.correlation_uid`.
- **The actor is `actor.user.uid = <actor>@<boundary>`,** the same name as every other layer ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)), for every kind of principal. A person or an external actor is a keyed pseudonym in front of the `@`. `actor.user.type_id` gives the kind: 1 user, 2 operator (admin), 3 sluis actor (system), 4 workload or client principal (service). The OAuth client through which the principal acted is `actor.application`, and the upstream identity provider is `actor.idp`.
- **A record is stored as RFC 8785 canonical JSON, and the same bytes travel end to end.** The emitter produces the canonical bytes, the transport carries them as the message body, and the writer stores them unchanged. The reader's Connect API returns them as opaque `bytes`, beside typed envelope fields for filtering. Nothing re-encodes a record, because a re-encoding would break the hash, and proto3's JSON mapping turns 64-bit integers into strings.
- **The canonical form is made reproducible by rules on the record:**
  - strings are UTF-8, and a string is never normalised by the writer;
  - numbers are integers within ±(2^53−1), because RFC 8785 serialises numbers as IEEE 754 doubles; there is no floating point; a 64-bit identifier or counter that can exceed the bound is a string; times are integer milliseconds (`timestamp_t`);
  - an object has no duplicate keys, and key order is whatever the emitter wrote before canonicalisation, because canonical form sorts them;
  - free text is bounded in length.
- **The writer checks canonical form** (inferred): it parses the record, canonicalises it again, and refuses a record whose bytes differ from the result. A producer that is not canonical is therefore found at the writer, not by an auditor years later. The writer still stores the received bytes, not its own.
- **A verifier hashes the stored bytes** and never re-serialises. The record's SHA-256 over those bytes is the fingerprint that the export's `record_integrity` attestation carries. The tree's leaf is the hash of the whole entry, ciphertext included, as 0021 decides, so a log verifies after a company's key is shredded.
- **The stored record is lean:** enum ids only, with no enum captions (an `activity_name` is written only where the activity is 99 or the table names it, as for "Step-Up"), no `observables`, no enrichments, no `raw_data`, and no `unmapped` other than `unmapped.sluis`. An export adds the derived data.
- **A record validates in three places.** The writer enforces the JSON Schema 2020-12 generated from the action table, which joins the pinned OCSF schema, the schema of `unmapped.sluis` and sluis's rules (no names, e-mail addresses or phone numbers, no floating point, bounded text), and stays authoritative. Go and Rust emitters validate their own entries before they emit; a browser emitter does not. The OCSF toolkit, which is pre-1.0, runs in CI only, on the action table's example records, and CI also runs one set of test vectors through the validators of Go, Rust and TypeScript and fails on any disagreement. The schema is laid out as one shared definitions document with a slim schema per action, so validator memory does not grow with the table.
- **Libraries** (checked 2026-10-31): JSON Schema with `santhosh-tekuri/jsonschema` v6 (Go), `jsonschema` 0.60 (Rust) and ajv 8 (TypeScript); RFC 8785 with Go's `encoding/json/jsontext` (with `gowebpki/jcs` as the fallback), `serde_json_canonicalizer` (Rust) and `canonicalize` (TypeScript). sluis generates its own Go, Rust and TypeScript types from the generated schema, for the classes and objects the action table uses.
- **A record never carries a secret, an e-mail address, a name or a token** (REQ-AUDIT-003, REQ-AUDIT-037). A secret appears as a fingerprint and a revision.
- **A record may carry a trace id,** one way only: the record points at the trace, and the trace never carries what the record says (ADR 0036).

### Export

- **The audit export in 2.0 is NDJSON of OCSF events,** decrypted for the caller's grant ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)). It adds what the stored form leaves out: enum captions, `observables`, and the `record_integrity` profile's attestation, whose fingerprint (SHA-256 over the RFC 8785 bytes) and chain id point back to the log entry. It keeps `metadata.log_name` and `unmapped.sluis` as they were written. An OCSF-aware pipeline reads it as OCSF with no mapping, and sees `unmapped.sluis` as the source-specific data that `unmapped` is for.
- **Exporters for a particular target are drivers, later.** AWS Security Lake, for example, takes Parquet and OCSF 1.3 at most for a custom source (checked 2026-10-31), so its exporter maps down between versions. The down-mapping lives in the exporter, never in the record, and `unmapped` is in every version's base event.

### The usage stream

- **Usage is a separate stream.** It is a log kind of its own, written by the same writer as the audit log, with its own prefix or bucket, checkpoints, lock mode, off-site copy, consumers and retention, signed with the installation's one log key (each signed head names its log) ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)). The audit log is read by security and audit readers and searched by the indexer. The usage log is read by quota and by the finance export, and has no search index (REQ-AUDIT-098).
- **Each stream declares its own retention.** A production installation declares both and there is no default, so billing data can be kept as long as finance needs it, and security records as long as an audit needs them, and neither period binds the other.
- **A usage event is a CloudEvents 1.0 event in the structured content mode, as JSON, in RFC 8785 canonical form** (the current release of the specification is 1.0.2). It has these attributes:

  | Attribute | Value |
  |---|---|
  | `specversion` | `1.0` |
  | `id` | `<message id>/<n>`: the audit record's `metadata.uid` (a UUIDv7, RFC 9562) and the position of the event in the message's usage part, counted from 0 (inferred) |
  | `source` | `<actor>@<boundary>`, the emitting actor |
  | `type` | the meter event, named by the action table |
  | `subject` | the account, the aggregation key, as in OpenMeter |
  | `time` | the time of use |
  | `datacontenttype` | `application/json` |
  | `data` | the unit, the amount as an integer with its sign, the request id where there is one, the subject's pseudonym, and for a quota message the named balance the amount is applied to |

  Amounts are integers within ±(2^53−1), so canonical JSON keeps them exact. There are no decimals ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).
- **The idempotency key of a usage event is its `source` and `id`,** as CloudEvents and OpenMeter deduplicate. The writer removes duplicates of the message by `metadata.uid`, so a usage event is written exactly as often as its audit record, and no usage event can be written twice by a redelivery.
- **Only quota sends a balance effect.** An application's usage part carries meters only, and the writer refuses a balance effect from any actor but `quota`, by its action table entry ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).
- **The consumers are quota and finance.** Quota follows the usage log by checkpoint, derives its meters and hourly aggregates, rolls its balances forward and rebuilds its remembered request ids from it. The finance export is a table derived from the log with the column names of FinOps FOCUS 1.4 (released 2026-06-04) where they fit: `ChargePeriodStart`, `ChargePeriodEnd`, `ConsumedQuantity`, `ConsumedUnit`, `BillingAccountId`, `SubAccountId` and `SkuMeter`. sluis holds no prices. A metering system such as OpenMeter reads the export, or the log with a read grant, with little mapping. Nothing else reads the usage log.

### Usage is derived from the same event, once

- **The emitter library builds both parts of a message from one call.** The application describes the action once, with its meters. The library gives the message one id, writes it as the audit record's `metadata.uid` and as the prefix of each usage event's `id`, and sends one message. There is no call that emits a usage part alone, and no call that emits an audit part twice.
- **The action table entry declares which meters an action may carry,** with their units. The writer refuses a message whose usage part has a meter its entry does not declare, an amount outside the bounds, or an id that is not the audit record's id.
- **Every usage event has an audit record, and every audit record may have usage.** An action that uses something is audited as every action is, so the audit trail and the usage stream cannot disagree about what happened.
- **The writer splits the message and registers both logs' sealed batches in one atomic state batch,** with the removed duplicate ids and its transport cursor (REQ-AUDIT-094). A crash leaves both or neither, so the two logs never disagree about which messages were written.
- **A retried quota call reuses its message.** Quota writes the message it is about to send into its state in the same atomic batch as the change to the balance or the reservation, together with the request id, and a relay sends it and clears it. A retry with the same request id finds the stored message and sends that, with the same id, so the writer removes the duplicate; a crash between the change and the send leaves the message in state, and the relay sends it on restart (inferred). A balance therefore changes once and has one usage event, however often the call is retried. How quota keeps strict enforcement is not changed by this ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).

## Consequences

- A decrypted audit record is an OCSF event of a published version, with its sluis-specific facts in one `unmapped.sluis` object. A tool that knows only OCSF finds every core field and sees `unmapped.sluis` as source-specific data, which is what `unmapped` means. A SIEM that flattens JSON can query it. Nothing validates it except sluis's own schema, and OCSF says consumers should not rely on a stable structure there, so the structure is sluis's to keep stable and to publish with each release.
- There is no extension to own: no registry entry, no source tree, no compile step in CI or in a consumer, and no version of its own. What sluis owns instead is the JSON Schema of `unmapped.sluis`, generated from the action table in CI, and the rule that a field is only added within 2.x.
- A fact that OCSF later gives a core home (the token audience in 1.10, for example) moves at an OCSF upgrade. New records use the core attribute, old records keep the field, and one log then holds two shapes, which readers handle by the release a record names.
- A writer must know the action table of every release that emits to it, because no catalogue is stored in the log. It is upgraded before its emitters, and for an application outside sluis its generated schema is installed beside it.
- Following OCSF means following its releases. Each upgrade is a pinned, checked change, and OCSF keeps deprecating classes (1.9.0 replaced Account Change by User Management); the CI comparison catches that, and a core attribute that has the name of a field of `unmapped.sluis`.
- Some sluis actions map to OCSF only approximately: token grants and certificate issuance use activity 99 of Authentication, key use is API Activity 99, a quota call is API Activity, and a step-up is a Logon told apart by its event code and `activity_name`. A security tool sees these as what they are in OCSF terms, not by their sluis names. An extension with classes of its own would improve the fit and would be a new record.
- An OCSF record is about 2.8 times the size of a minimal record of sluis's own (about 1.1 KB for a sign-in, measured 2026-10-31); `unmapped.sluis` adds from nothing on a sign-in to a few hundred bytes on a certificate issuance. At about four million records a year that is a few gigabytes a year per boundary, which object storage absorbs. Encrypted records do not compress.
- The integer bound and the canonical-form check make the writer reject records that other OCSF producers could send, such as one with a 64-bit counter above 2^53. Emitters must write such a value as a string, and the action table must say which.
- Billing and quota read a stream made for them, in a usage-event standard, with a retention of their own, and never parse security records. The usage log is one more log kind per writer: a second set of checkpoints to witness and copy, and a second retention to declare. It has no key of its own.
- One combined emit and one atomic registration keep the two logs consistent by construction. A writer outage stops both logs together, and a malformed usage part fails its whole message, audit part included.
- Quota holds an outbox in its state, and a relay. That is more code on the quota path, and a restore must treat a stored message as unsent.
- `REQ-AUDIT-086` and the records beside it change from "the sluis extension" to "core classes with `unmapped.sluis`", the catalogue records become records about the action table, and new records state the canonical-form check and the single-call derivation of usage.
- The standards register changes the OCSF row: core classes, the pin, and `unmapped.sluis` in place of an extension.

## Roads not taken

### The full sluis extension (the earlier form of this record)

- Pros: every sluis field in one typed place, a profile `sluis/audit` that OCSF tools can load, and a required `boundary` the writer can check. It is what the OCSF FAQ advises for a native producer.
- Cons: an entry in the public extensions registry (a pull request, and a uid that is not ours until it merges), a source tree, a version of its own, and a compile step in CI and in every consumer that validates strictly. An artefact to own at every OCSF release. AWS Security Lake takes only OCSF 1.3 or earlier for a custom source and has nothing to load an extension with, so the exporter would drop it. No vendor mapping read on 2026-11-01 extends the schema for identity events. Of six extension fields, `boundary` and the configuration revision have core homes (`metadata.log_name` and `entity.version`), and the catalogue version is gone with the catalogue, so little would be left to type. No consumer needs typed sluis fields today.
- Why not: decided by the maintainer on 2026-11-01. The OCSF FAQ and the schema's description of `unmapped` advise an extension, and that is advice, not a rule. An extension may be added later, in a minor release, when a real consumer needs typed fields.

### A small extension for only the fields without a core home

- Pros: the same typing for about four objects instead of six, namespaced so that a later core attribute cannot collide with it.
- Cons: the same registry entry, source tree, compile step and release work as the full extension, and the same down-mapping loss, for fields that no consumer reads in typed form yet.
- Why not: the cost of owning an extension is the same for a small one. `unmapped.sluis` has the same namespacing and sluis's own schema.

### Plain OCSF with the facts in opaque `json_t` slots (the earlier decision of 0021)

- Pros: a record any OCSF tool reads with nothing else to know, and no extension. A check of 2026-10-31 found the mapping complete this way.
- Cons: seven facts sit scattered over `policy.data`, `resources[].data` and `entity.data`, slots that no schema types and that only the action table explains for each event code. A SIEM cannot find the sender binding or a key's algorithm in one place.
- Why not: one namespaced object, validated by sluis's own schema, keeps the same facts together and checked.

### New event classes for token, certificate and key activity

- Pros: the fit is exact: a token grant is not a logon, and a key use is not an API call. Detections could name the class.
- Cons: a class in an extension has a uid scoped by the extension, so a SIEM rule or a Security Lake query keyed on a core class does not see it, and every consumer must compile the extension to know the class at all. It is more schema to maintain before anyone has asked for it.
- Why not: 2.0 uses core classes with activity 99. Classes can be added by a later record if a consumer needs them, together with an extension.

### Core tags and labels for the fields without a home

- Pros: core, no registry, survives down-mapping, and visible to OCSF tools as `metadata.tags` or `resources[].tags`.
- Cons: everything is a string, with no enums and no nesting; tags are for classification, not for facts; each SIEM indexes them differently; and sluis would still have to define the allowed keys, which is an unregistered schema again.
- Why not: `unmapped.sluis` is typed and validated.

### A catalogue document in the log, with its version in each record

- Pros: a log describes itself, and witnesses cosign which schemas were in force.
- Cons: a second versioned artefact beside the release, a schema entry in the log, a field in every record, a write path for catalogues and a rule for which one is in force. The release already identifies the table.
- Why not: decided by the maintainer on 2026-11-01. The action table lives in code, and the release version in `metadata.product.version` with `metadata.event_code` identify it.

### Authentication Ticket, Service Ticket Request and Service Ticket Renew for the token stages

- Pros: Authentication has these activities, the class description now covers OIDC and SAML stages, and a rule keyed on them would see a grant, an exchange and a refresh as authentication events.
- Cons: their descriptions say "Kerberos", so an analyst or a rule built from the descriptions would read an OAuth grant as a ticket-granting ticket. No vendor mapping read puts OAuth token grants in them. Preauth (6) is a poorer fit for a step-up, with the same wording.
- Why not: activity 99 with a name keeps the class and does not mislead. A step-up is a Logon, which is what it is: a second authentication of an open session.

### `delegation` for the actor chain of an exchange

- Pros: it is the core object for delegation, with a parent link between hops.
- Cons: it exists only through the `ai_operation` profile, which is for AI models and agents, and it carries identifiers only; the principals are the other objects of the event. A record would declare a profile that has nothing to do with it.
- Why not: `actor.user` and `user` name the acting party and the subject, and `unmapped.sluis.actor_chain` names the further hops.

### An issued certificate in the `certificate` object of Authentication

- Pros: the closest typed object, with serial, subject, names, validity and fingerprints, and what a SIEM reads for certificates.
- Cons: the attribute means the certificate that was presented to the authentication, so a rule on it would read an issuance as a sign-in with a certificate. OCSF has not decided how to record certificate issuance (issue 1513 is open).
- Why not: the issued certificate is in `unmapped.sluis.certificate`, and the `certificate` object keeps its meaning. A core home replaces the field if OCSF adds one.

### The audit export as API Activity / 99 "Export"

- Pros: no new class in the table, since the audit read is already API Activity.
- Cons: API Activity has create, read, update and delete only, and Event Log Activity has an Export activity of its own, with `file` and a log name.
- Why not: an export of the log is 1008 Event Log Activity / 3 Export, and an audit read stays API Activity / 2 Read.

### Track the latest OCSF release

- Pros: always the newest classes and fields, and no pinning work.
- Cons: records of one release would not validate against another's, a log would have no stated schema, and a deprecation would land without a review.
- Why not: a pin per release, a schema published with the release, and a deliberate upgrade.

### Pin OCSF for the whole of 2.x

- Pros: no upgrade work, and one schema to index for the life of the line.
- Cons: sluis would miss classes and fields OCSF adds, and the pin would grow stale against the tools that read the export.
- Why not: OCSF's compatibility rules (a minor release only adds) make a stepwise upgrade safe, so sluis upgrades deliberately and keeps old records as they are.

### Hash the received bytes with no canonical form

- Pros: no canonicalisation rules, no integer bound and no check at the writer.
- Cons: two implementations that re-serialise a record get different hashes, so an export or a re-played record cannot be verified against the log by a tool that did not keep the bytes.
- Why not: RFC 8785 makes the hash reproducible from the parsed record, which costs a bound on numbers and a check.

### Deterministic Protobuf, or CBOR, as the canonical form

- Pros: smaller records, and typed producers.
- Cons: proto3's JSON mapping does not match OCSF's types, a deterministic encoding is not canonical across languages, and no OCSF tool reads either without a conversion that would sit in the hashed path.
- Why not: OCSF is JSON, and RFC 8785 is the JSON canonical form with implementations in all three languages.

### Keep v1's record: a Protobuf core with JSON Schema slots

- Pros: built and tested in v1, compact, typed in every language.
- Cons: a format of sluis's own that no security tool reads without a mapping, and a drift test between the Protobuf and the JSON Schema to keep.
- Why not: the maintainer asked for a standards-based format, and v1's record is dropped with no compatibility or migration.

### A minimal record of sluis's own, with an OCSF exporter

- Pros: about a third of the size of an OCSF record, and no dependence on OCSF's releases.
- Cons: an invented format, and a mapping to OCSF in every exporter that must be kept right for every action.
- Why not: sluis follows standards and invents no format where one exists.

### A CloudEvents envelope with OCSF as its data

- Pros: a common framing with bindings for HTTP, Kafka, AMQP and NATS.
- Cons: it repeats `id`, `time` and `type` beside OCSF's own fields and adds nothing the log entry and the envelope do not have. Its binary mode puts attributes in transport headers, which a hash over the body does not cover.
- Why not: the stored audit record is the OCSF event itself. CloudEvents is used where it fits, for usage.

### DMTF CADF, the OpenTelemetry log model, or ECS

- Pros: CADF's initiator, target and observer fit an audit trail well; OpenTelemetry and ECS are widely deployed.
- Cons: CADF has had one version, in 2014, and nobody outside OpenStack reads it. OpenTelemetry has no semantic conventions for authentication or audit events (v1.44.0, 2026-08-04). ECS is one vendor's vocabulary that is being merged into OpenTelemetry.
- Why not: OCSF is the one maintained security event schema with identity classes, neutral governance and consumers.

### Certificate issuance as User Management / Add Programmatic Credentials

- Pros: "a credential added to a principal" is the closer meaning.
- Cons: User Management has no `certificate` object, and its programmatic credential has only a type, an id and a last-used time, so the certificate's serial, names and validity would be in `unmapped.sluis` anyway, under an activity whose name says nothing about a certificate authority.
- Why not: Authentication (3002) with activity 99 "Certificate Issue" keeps the action visible to a rule on certificates, and the details are in `unmapped.sluis.certificate`.

### The scopes only in the grant record

- Pros: the grant record's `privileges[]` types them once, and the token record would carry the grant id.
- Cons: a refresh or an exchange issues a new token whose audience and scopes can be narrower than the grant's, and a reader of a token record would have to join to the grant to know what the token was for. OCSF 1.9.0 has no audience or scopes on a token.
- Why not: the records that issue a token carry its audience and scopes in `unmapped.sluis.token`, and the grant record keeps `privileges[]` for the grant of scopes to a principal.

### The OCSF toolkit in the writer

- Pros: OCSF's cross-field rules are checked on every record, at about 7 µs each.
- Cons: the toolkit is pre-1.0, and a minor release may break the writer.
- Why not: it runs in CI on the action table's example records, and the writer enforces the JSON Schema generated from the action table.

### Protobuf on the transport, converted to JSON by the writer

- Pros: smaller messages and typed producers.
- Cons: the writer would produce the bytes that are hashed, so the emitter's record and the stored record could differ.
- Why not: the same canonical bytes end to end.

### Store the enriched form

- Pros: an export would need no work, and a reader sees captions at once.
- Cons: every record carries derived data, larger, and the derived data would be part of the hashed evidence.
- Why not: the lean form is stored and an export enriches it.

### Billing derived from the audit log, with usage in an OCSF extension class

- Pros: one log, one writer path and one retention.
- Cons: billing data in a security schema, an extension class that no OCSF consumer knows, a retention that ties finance and security together, and billing readers that must decrypt and parse security records. OCSF has no usage class, so every meter would be an API Activity with its meaning in a field OCSF does not define.
- Why not: a separate usage stream in a usage-event standard, with its own retention and readers.

### The writer derives the usage event from the audit record

- Pros: applications emit only OCSF, and no application can disagree with its audit record about usage.
- Cons: the amount of a meter (bytes, tokens, a count) is often not in the security record, so the writer would need a second input anyway; the writer would parse a record to find numbers, and an action table change would change billing silently.
- Why not: the emitter builds both parts from one call, and the action table entry declares the meters, so the writer only checks.

### A usage API that emits on its own

- Pros: usage that has no action to audit, such as a periodic gauge, could be sent without a record.
- Cons: a second path that can emit twice or emit without an audit record, which is the double-emission bug this record is written to prevent.
- Why not: every usage event comes from a message that also has an audit record. A gauge is an action of its own and is audited.

### A separate writer for the usage log

- Pros: the usage path could be scaled and placed on its own.
- Cons: two writers fed from one emit are a dual write: a crash between them leaves one log with an event the other lacks, and reconciling them needs a protocol of its own. It is also a second writer per node to fence and key.
- Why not: the same writer splits one message into both kinds and registers both in one atomic state batch.

### A usage `id` that is the bare message id

- Pros: the usage event's `id` equals the audit record's `metadata.uid`, so the join is trivial.
- Cons: a message with two meters would have two events with one `id`, and CloudEvents and OpenMeter deduplicate by `source` and `id`, so one would be dropped.
- Why not (inferred): `<message id>/<n>` keeps the prefix join and makes each event unique.

### OCSF for the usage log as well

- Pros: one record format for both kinds.
- Cons: OCSF has no usage or metering class, so every meter would sit in a field that OCSF does not define, which is the extension again, for a reader that is not security.
- Why not: CloudEvents with typed usage data is the shape metering systems already read.
