# 0064 — Audit records are OCSF events with a sluis extension, and usage is a separate stream

**Status:** Accepted
**Date:** 2026-11-01

Decided by the maintainer on 2026-11-01: audit records are OCSF-native with a sluis extension, and usage (metering) is a separate stream. The record format was [OD-40](/open-decisions/#od-40). Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). Build: [issue #841](https://github.com/truvity/sluis/issues/841) (emitter, catalogue, metering) and [issue #915](https://github.com/truvity/sluis/issues/915) (quota). Points marked "(inferred)" are choices made while writing this record, for the maintainer to confirm.
This record takes the record formats out of [0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/), which keeps the log, and changes one of them: 0021 had decided plain OCSF with no extension; the extension below replaces that. v1's record format is dropped without compatibility or migration.

## Context

[0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/) decides how a log is laid out, sealed, locked, encrypted and kept. This record decides what is in a log entry: the audit record, and the usage event that travels with it.

- **OCSF is the security event schema that is maintained and consumed.** The Open Cybersecurity Schema Framework is a Linux Foundation project under Apache-2.0, with written compatibility rules: a minor release only adds, and only a major release removes. Its current release is 1.9.0 (2026-08-03), with about three minor releases a year and no 2.0 announced. It has core classes for authentication, user, role and entity management and API activity, a `security_control` profile for policy decisions, and a `record_integrity` profile. It is the native schema of AWS Security Lake and is read by the pipeline vendors. (Checked 2026-11-01 against the schema repository at tag `1.9.0`.)
- **OCSF says a native producer extends the schema.** The OCSF FAQ advises native event producers against the `unmapped` object and asks them to capture product-specific data in an extension, "using either a vendor developed profile, or in some cases a new event class". A profile adds attributes to existing classes and objects, and an extension can patch the core classes with it, so no new class is needed.
- **A check on 2026-10-31 found the mapping complete without an extension,** at the price of seven facts in opaque `json_t` slots: the sender binding of a token, a key's algorithm and digest, configuration digests, the enrolment proof, and a few more. The maintainer now prefers those facts typed, so that a SIEM and the catalogue's own JSON Schema can see them, and decided that sluis defines an extension.
- **OCSF has no class for issuing a certificate or a token,** and none for using a key. Those actions use the nearest core class, as the mapping below says.
- **Billing and quota want a different record than security does.** A usage fact (an account, a meter, an amount) is read by quota and by finance, has its own retention and its own consumers, and has a usage-event standard of its own. OCSF has no usage class.
- **The audit log is sealed and verifiable,** and a hash must be reproducible by any implementation. That needs one canonical serialisation of a record (RFC 8785), and numbers that survive it.

The volume measured today is about 2.4 records a second at peak and about 75,000 a week. The threats this record answers are T-10, T-12, T-13, T-14 and T-26 of the [threat model](/security/threat-model/).

## Decision

### The version of OCSF is pinned, and an upgrade is a checked change

- **Each sluis release pins one OCSF version,** named in every record's `metadata.version`. The first release pins **OCSF 1.9.0**. A development version (`-dev`) is never pinned.
- **The compiled schema is stored.** The OCSF schema compiler builds the pinned core schema together with the sluis extension (below), and the compiled result is stored write-once with the catalogue. The schema entry in the log names it, so witnesses cosign which schema was in force ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)).
- **An upgrade moves one OCSF minor release at a time, as a deliberate pull request** (inferred: no more than one per sluis minor release). CI compiles the old and the new schema, compares them under OCSF's compatibility rules, and fails when the catalogue or the extension uses an item the new version marks deprecated, or when a new core attribute has the name of an extension attribute (REQ-AUDIT-089).
- **Records are never rewritten.** A log holds records of several OCSF versions, each naming its own, and readers, the indexer and exporters handle each record by the version it names.
- **A new OCSF major release needs a new record.** Until then sluis stays on 1.x.

### The class mapping

The catalogue names one class and activity per action. Classes and activity ids were checked against the OCSF 1.9.0 tag on 2026-11-01. The mapping of 2026-10-31 stands; it was validated with the OCSF toolkit on 31 example records.

| sluis action | OCSF class / activity |
|---|---|
| Sign-in (browser, passkey, upstream OpenID Connect) and recovery sign-in | 3002 Authentication / 1 Logon |
| Sign-out, end of a session | 3002 Authentication / 2 Logoff |
| Step-up of a session | 3002 Authentication / 99 "Step-Up" |
| Token grant at `/token`, token refresh, token exchange, token revocation | 3002 Authentication / 99 with `activity_name`; an exchange carries the actor chain in `delegation`; an impersonating exchange is 7 Account Switch |
| A grant of scopes | 3003 Authorize Session / 1 Assign Privileges, with the scopes in `privileges[]` |
| A certificate (X.509 or SSH) issued or revoked | 3002 Authentication / 99 "Certificate Issue" or "Certificate Revoke", with the `certificate` object |
| A credential minted to a principal, such as a GitHub token | 3007 User Management / 18 Add Programmatic Credentials |
| A credential revoked | 3007 User Management / 19 Remove Programmatic Credentials |
| Enrolment of a node, a device or a workload | 3004 Entity Management / 6 Enroll, 7 Unenroll |
| A passkey registered or removed | 3007 User Management / 12 Enable MFA Factors, 13 Disable MFA Factors |
| A user created on first sign-in, a roster change, a role assignment | 3007 User Management and 3006 Group Management, by activity |
| A console write (configuration, a client registration, a lowered limit) | 3004 Entity Management / 1 Create, 3 Update, 4 Delete, with the full value in `updated_entity.data` for roll-forward |
| A quota call (`Admit`, `Confirm`, `Cancel`, `TopUp`, `Adjust`) | 6003 API Activity / 1 Create, 3 Update, 4 Delete |
| An audit read (search, get, tail, proof) and an export | 6003 API Activity / 2 Read, and 99 "Export" |
| Key use (sign, decrypt) | 6003 API Activity / 99 "Sign" or "Decrypt", with the key in `resources[]` and its kind and algorithm in the extension |
| A key created, rotated or destroyed, including a company key shredded | 3004 Entity Management / 1 Create, 3 Update, 4 Delete, with the key in `entity` |
| A configuration reload | 6002 Application Lifecycle / 99 "Reload" |
| Writer and log lifecycle | 1008 Event Log Activity |

- **A policy decision is not a record of its own:** it is part of the record of the action it gates, in the `security_control` profile (`action_id`, `authorizations`, `policy`). A record that uses it declares the profile.
- **A SIEM rule keyed on Logon does not see a token grant.** That is correct, because a grant is not a logon.
- **The extension adds no class in 2.0** (below), so every class above is a core class and the record's `class_uid` is the core one.
- **One event code per grant type** (authorization code, refresh, client credentials, exchange), so the catalogue implies the grant type and no field carries it.
- **A refresh rotation names two tokens in one record:** `session.uid` is the refresh family, `session.credential_uid` the presented token, and `authentication_token` the new one.
- **Certificate details that the `certificate` object lacks follow fixed catalogue meanings:** `fingerprints[0]` is the certificate's hash, `created_time` its not-before, `certificate.uid` its certificate-transparency entry, SSH critical options and extensions are implied by the profile digest in `policy.version`, and a revocation reason is `status_detail`.
- **A passkey's credential id is a `resources[]` entry of the User Management record,** not a device.
- **A request id appears only where it is needed,** such as quota's idempotency, in `api.request.uid` on API Activity. The trace id joins the records of one request.

### The sluis extension

- **sluis defines one OCSF extension, named `sluis`.** Its source is in this repository, in the OCSF extension layout (`extension.json`, `dictionary.json`, `objects/`, `profiles/`, and patch files under `events/`), and is public. It is compiled with the pinned core schema by the OCSF schema compiler.
- **The extension is a profile.** The profile `sluis/audit` adds one object-typed attribute, `sluis`, to the core classes that the catalogue uses. Patch files add the profile to those classes without creating new ones, which is how OCSF extensions patch the core schema. Every record declares `sluis/audit` in `metadata.profiles` (REQ-AUDIT-102) and names the extension, with its name, uid and version, in `metadata.extensions`. The deprecated `metadata.extension` is not used.
- **One namespaced attribute, not many.** Everything of sluis is under `sluis`, so a later OCSF minor release that adds a core attribute of the same name cannot collide with it (inferred).
- **The extension holds only what OCSF lacks.** If a core attribute exists, the record uses it. When a later OCSF release adds a core home for a field, the field is deprecated in the extension with OCSF's own `@deprecated` marker, and removed only by a major version of the extension.
- **What the extension holds at first** (inferred; the source of the extension is the exact list):

  | Field under `sluis` | Type | What it says | It replaces |
  |---|---|---|---|
  | `boundary` | string, required | The environment boundary that owns the log. The writer checks it equals the log's boundary | The log's identity, which a record taken out of its log lost |
  | `catalogue` | object (`app`, `version`) | The catalogue entry version in force | The schema entry in the log |
  | `sender_binding` | object (`type_id`: 1 DPoP, 2 mTLS, 99 other; `thumbprint`) | The confirmation key of a token | `policy.data` |
  | `key` | object (`kind_id`: token signing, X.509 CA, SSH CA, log, mint grant, tenant, data; `algorithm`; `digest_algorithm`) | The kind and algorithm of a key that was used, created or destroyed. The key's id stays in `resources[]` or `entity` | `resources[].data` |
  | `config` | object (`digest`, `revision`) | The digest and revision of a configuration that was loaded or changed | `entity.data` |
  | `enrolment` | object (`proof_type_id`, `proof_digest`) | The proof that a node, a device or a workload gave | `entity.data` |

- **What stays out of the extension:** the data origin and the sensitivity of a record kind ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)), which the catalogue entry that `metadata.event_code` names already says for every record of that action; the pseudonym key epoch, which is encoded in the pseudonym; the OAuth scopes, which are the `privileges[]` of the grant record; and the roll-forward value of a console write, which is a free-form value typed per event code and stays in `updated_entity.data`, OCSF's own `json_t` slot.
- **The extension defines no event class and no category in 2.0.** A class of its own would be scoped by the extension's uid, and a SIEM rule keyed on a core class would not see it.
- **The extension's uid comes from the OCSF extensions registry.** The registry hands out the next unused vendor uid, counting down from 998. On 2026-11-01 the lowest taken uid was 984, so the next was **983** (inferred: the uid is 983 if nobody takes it before the registration pull request is merged, and the number written here is corrected then). The maintainer registers the name `sluis` with a pull request that only adds a row to the registry, and confirms it by e-mail from an official address, as OCSF's contributing guide asks. If sluis ever defines classes, a class uid is `extension uid × 100000 + category uid × 1000 + the class's own uid`, and a category uid is 30 or more.
- **The extension has its own SemVer version,** in `extension.json` and in `metadata.extensions`, independent of the OCSF version and of the sluis release. A minor version only adds fields. A field is never removed or retyped within a major version. A major version needs a new record.
- **The extension is compiled in CI with the pinned core schema,** and the catalogue's JSON Schema is generated from the compiled result, so the extension's fields are validated like any other.

### The record: lean, canonical and validated

- **An audit record is an OCSF event of the pinned version with the `sluis/audit` profile.** The sluis action is `metadata.event_code`; the company is `metadata.tenant_uid`; the record id is `metadata.uid`; the outcome is `status_id` with the reason code in `status_code`; the trace id is `metadata.correlation_uid`.
- **The actor is `actor.user.uid = <actor>@<boundary>`,** the same name as every other layer ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)), for every kind of principal. A person or an external actor is a keyed pseudonym in front of the `@`. `actor.user.type_id` gives the kind: 1 user, 2 operator (admin), 3 sluis actor (system), 4 workload or client principal (service). The OAuth client through which the principal acted is `actor.application`, and the upstream identity provider is `actor.idp`.
- **A record is stored as RFC 8785 canonical JSON, and the same bytes travel end to end.** The emitter produces the canonical bytes, the transport carries them as the message body, and the writer stores them unchanged. The reader's Connect API returns them as opaque `bytes`, beside typed envelope fields for filtering. Nothing re-encodes a record, because a re-encoding would break the hash, and proto3's JSON mapping turns 64-bit integers into strings.
- **The canonical form is made reproducible by rules on the record:**
  - strings are UTF-8, and a string is never normalised by the writer;
  - numbers are integers within ±(2^53−1), because RFC 8785 serialises numbers as IEEE 754 doubles; there is no floating point; a 64-bit identifier or counter that can exceed the bound is a string; times are integer milliseconds (`timestamp_t`);
  - an object has no duplicate keys, and key order is whatever the emitter wrote before canonicalisation, because canonical form sorts them;
  - free text is bounded in length.
- **The writer checks canonical form** (inferred): it parses the record, canonicalises it again, and refuses a record whose bytes differ from the result. A producer that is not canonical is therefore found at the writer, not by an auditor years later. The writer still stores the received bytes, not its own.
- **A verifier hashes the stored bytes** and never re-serialises. The record's SHA-256 over those bytes is the fingerprint that the export's `record_integrity` attestation carries. The tree's leaf is the hash of the whole entry, ciphertext included, as 0021 decides, so a log verifies after a company's key is shredded.
- **The stored record is lean:** enum ids only, with no enum captions, no `observables`, no enrichments, no `raw_data` and no `unmapped`. An export adds the derived data.
- **A record validates in three places.** The writer enforces the catalogue's JSON Schema 2020-12, generated from the compiled schema (OCSF plus the extension), narrowed by sluis's rules (no names, e-mail addresses or phone numbers, no floating point, bounded text), and stays authoritative. Go and Rust emitters validate their own entries before they emit; a browser emitter does not. The OCSF toolkit, which is pre-1.0, runs in CI only, on the catalogue's example records, and CI also runs one set of test vectors through the validators of Go, Rust and TypeScript and fails on any disagreement. The schema is laid out as one shared definitions document per profile set with a slim schema per entry, so validator memory does not grow with the catalogue.
- **Libraries** (checked 2026-10-31): JSON Schema with `santhosh-tekuri/jsonschema` v6 (Go), `jsonschema` 0.60 (Rust) and ajv 8 (TypeScript); RFC 8785 with Go's `encoding/json/jsontext` (with `gowebpki/jcs` as the fallback), `serde_json_canonicalizer` (Rust) and `canonicalize` (TypeScript). sluis generates its own Go, Rust and TypeScript types from the compiled schema, for the classes and objects the catalogue uses.
- **A record never carries a secret, an e-mail address, a name or a token** (REQ-AUDIT-003, REQ-AUDIT-037). A secret appears as a fingerprint and a revision.
- **A record may carry a trace id,** one way only: the record points at the trace, and the trace never carries what the record says (ADR 0036).

### Export

- **The audit export in 2.0 is NDJSON of OCSF events,** decrypted for the caller's grant ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)). It adds what the stored form leaves out: enum captions, `observables`, and the `record_integrity` profile's attestation, whose fingerprint (SHA-256 over the RFC 8785 bytes) and chain id point back to the log entry. It carries the log's boundary in `metadata.log_name`. An OCSF-aware pipeline reads it with no mapping beyond loading the extension, and a pipeline that drops unknown attributes still gets the core fields.
- **Exporters for a particular target are drivers, later.** AWS Security Lake, for example, takes Parquet and OCSF 1.3 at most for a custom source (checked 2026-10-31), so its exporter maps down between versions and drops the extension. The down-mapping lives in the exporter, never in the record.

### The usage stream

- **Usage is a separate stream.** It is a log kind of its own, written by the same writer as the audit log, with its own prefix or bucket, log key, checkpoints, lock mode, off-site copy, consumers and retention ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)). The audit log is read by security and audit readers and searched by the indexer. The usage log is read by quota and by the finance export, and has no search index (REQ-AUDIT-098).
- **Each stream declares its own retention.** A production installation declares both and there is no default, so billing data can be kept as long as finance needs it, and security records as long as an audit needs them, and neither period binds the other.
- **A usage event is a CloudEvents 1.0 event in the structured content mode, as JSON, in RFC 8785 canonical form** (the current release of the specification is 1.0.2). It has these attributes:

  | Attribute | Value |
  |---|---|
  | `specversion` | `1.0` |
  | `id` | `<message id>/<n>`: the audit record's `metadata.uid` (a UUIDv7, RFC 9562) and the position of the event in the message's usage part, counted from 0 (inferred) |
  | `source` | `<actor>@<boundary>`, the emitting actor |
  | `type` | the meter event, named by the catalogue |
  | `subject` | the account, the aggregation key, as in OpenMeter |
  | `time` | the time of use |
  | `datacontenttype` | `application/json` |
  | `dataschema` | the catalogue schema of the event |
  | `data` | the unit, the amount as an integer with its sign, the request id where there is one, the subject's pseudonym, and for a quota message the named balance the amount is applied to |

  Amounts are integers within ±(2^53−1), so canonical JSON keeps them exact. There are no decimals ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).
- **The idempotency key of a usage event is its `source` and `id`,** as CloudEvents and OpenMeter deduplicate. The writer removes duplicates of the message by `metadata.uid`, so a usage event is written exactly as often as its audit record, and no usage event can be written twice by a redelivery.
- **Only quota sends a balance effect.** An application's usage part carries meters only, and the writer refuses a balance effect from any actor but `quota`, by its catalogue entry ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).
- **The consumers are quota and finance.** Quota follows the usage log by checkpoint, derives its meters and hourly aggregates, rolls its balances forward and rebuilds its remembered request ids from it. The finance export is a table derived from the log with the column names of FinOps FOCUS 1.4 (released 2026-06-04) where they fit: `ChargePeriodStart`, `ChargePeriodEnd`, `ConsumedQuantity`, `ConsumedUnit`, `BillingAccountId`, `SubAccountId` and `SkuMeter`. sluis holds no prices. A metering system such as OpenMeter reads the export, or the log with a read grant, with little mapping. Nothing else reads the usage log.

### Usage is derived from the same event, once

- **The emitter library builds both parts of a message from one call.** The application describes the action once, with its meters. The library gives the message one id, writes it as the audit record's `metadata.uid` and as the prefix of each usage event's `id`, and sends one message. There is no call that emits a usage part alone, and no call that emits an audit part twice.
- **The catalogue entry declares which meters an action may carry,** with their units. The writer refuses a message whose usage part has a meter its entry does not declare, an amount outside the bounds, or an id that is not the audit record's id.
- **Every usage event has an audit record, and every audit record may have usage.** An action that uses something is audited as every action is, so the audit trail and the usage stream cannot disagree about what happened.
- **The writer splits the message and registers both logs' sealed batches in one atomic state batch,** with the removed duplicate ids and its transport cursor (REQ-AUDIT-094). A crash leaves both or neither, so the two logs never disagree about which messages were written.
- **A retried quota call reuses its message.** Quota writes the message it is about to send into its state in the same atomic batch as the change to the balance or the reservation, together with the request id, and a relay sends it and clears it. A retry with the same request id finds the stored message and sends that, with the same id, so the writer removes the duplicate; a crash between the change and the send leaves the message in state, and the relay sends it on restart (inferred). A balance therefore changes once and has one usage event, however often the call is retried. How quota keeps strict enforcement is not changed by this ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).

## Consequences

- A decrypted audit record is an OCSF event of a published version plus one extension. A tool that loads the extension types every field. A tool that does not still finds every core field, and sees the `sluis` object as an attribute it does not know; a pipeline that drops unknown attributes loses only sluis's own context, and one that rejects them needs the extension loaded (a risk to test against the pipelines that matter, such as the Security Lake exporter). The facts that were opaque JSON are typed, which the catalogue's JSON Schema and a SIEM can use.
- The extension is an artefact to own: an entry in the OCSF registry, a source tree, a version, a compile step in CI and in every consumer that validates strictly. Registering it needs a pull request and an e-mail from the maintainer, and the uid is not ours until it merges.
- Following OCSF means following its releases. Each upgrade is a pinned, checked change, and OCSF keeps deprecating classes (1.9.0 replaced Account Change by User Management); the CI comparison catches that, and a core attribute that collides with the extension. One namespaced attribute makes that collision unlikely.
- Some sluis actions map to OCSF only approximately: token grants and certificate issuance use activity 99 of Authentication, key use is API Activity 99, and a quota call is API Activity. A security tool sees these as what they are in OCSF terms, not by their sluis names. Adding classes later would improve the fit and is a new record.
- An OCSF record is about 2.8 times the size of a minimal record of sluis's own (about 1.1 KB for a sign-in, measured 2026-10-31); the extension adds about 100 bytes. At about four million records a year that is a few gigabytes a year per boundary, which object storage absorbs. Encrypted records do not compress.
- The integer bound and the canonical-form check make the writer reject records that other OCSF producers could send, such as one with a 64-bit counter above 2^53. Emitters must write such a value as a string, and the catalogue must say which.
- Billing and quota read a stream made for them, in a usage-event standard, with a retention of their own, and never parse security records. The usage log is one more log kind per writer: a second log key, a second set of checkpoints to witness and copy, and a second retention to declare.
- One combined emit and one atomic registration keep the two logs consistent by construction. A writer outage stops both logs together, and a malformed usage part fails its whole message, audit part included.
- Quota holds an outbox in its state, and a relay. That is more code on the quota path, and a restore must treat a stored message as unsent.
- `REQ-AUDIT-086` and the records beside it change from "no extension" to "the sluis extension", and new records state the extension, the canonical-form check and the single-call derivation of usage.
- The standards register changes the OCSF row: an extension, with the pin and the registration.

## Roads not taken

### Plain OCSF with no extension (the earlier decision of 0021)

- Pros: a record any OCSF tool reads with nothing to compile in, and no registry entry, source tree or version to own. The check of 2026-10-31 found no gap.
- Cons: seven facts sit in opaque `json_t` slots that no schema types, which the OCSF FAQ advises native producers against; the catalogue alone says what `policy.data` holds for each event code; and a SIEM cannot query the sender binding or a key's algorithm.
- Why not: the maintainer decided on 2026-11-01 that sluis defines an extension for what OCSF lacks.

### The extension as many top-level attributes

- Pros: shorter paths (`boundary` instead of `sluis.boundary`) and one level less in the record.
- Cons: every name competes with the core dictionary, and a later OCSF minor release that adds a core attribute of the same name breaks the compile or silently shadows ours.
- Why not: one object under `sluis` keeps our names out of the core namespace.

### New event classes for token, certificate and key activity

- Pros: the fit is exact: a token grant is not a logon, and a key use is not an API call. Detections could name the class.
- Cons: a class in an extension has a uid scoped by the extension, so a SIEM rule or a Security Lake query keyed on a core class does not see it, and every consumer must compile the extension to know the class at all. It is more schema to maintain before anyone has asked for it.
- Why not: 2.0 uses core classes with activity 99. Classes can be added by a later record if a consumer needs them.

### Keep the compiled extension private, with no registry entry

- Pros: no pull request, no e-mail, no public name.
- Cons: nothing stops another vendor from taking the same name or uid, and a record naming an unregistered uid is ambiguous to anyone who meets it.
- Why not (inferred): the registry reserves the name for the cost of one pull request, and the schema source is public anyway.

### Use the registry's development extension (uid 999)

- Pros: nothing to register, and it works today.
- Cons: the uid is shared by every development extension and is meant to be replaced, so records written under it would name an extension that is not ours.
- Why not: records are permanent, and the uid in them must be ours.

### Track the latest OCSF release

- Pros: always the newest classes and fields, and no pinning work.
- Cons: records of one release would not validate against another's, a log would have no stated schema, and a deprecation would land without a review.
- Why not: a pin per release, a schema stored with the log, and a deliberate upgrade.

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

### OCSF's `unmapped` object for sluis fields

- Pros: valid OCSF with no extension.
- Cons: the OCSF FAQ advises native producers against it, and its content is free-form.
- Why not: nothing goes into `unmapped`, and the catalogue forbids it.

### Certificate issuance as User Management / Add Programmatic Credentials

- Pros: "a credential added to a principal" is the closer meaning.
- Cons: User Management has no `certificate` object, so the certificate's serial, issuer, subject, names and validity would be opaque JSON.
- Why not: Authentication (3002) with activity 99 "Certificate Issue" keeps the certificate typed.

### The scopes in the token record

- Pros: one record says everything about a token.
- Cons: the scopes are repeated from the grant, and a field for them would be one more thing in the extension.
- Why not: the grant record's `privileges[]` types them once; the token record carries the grant id.

### The OCSF toolkit in the writer

- Pros: OCSF's cross-field rules are checked on every record, at about 7 µs each.
- Cons: the toolkit is pre-1.0, and a minor release may break the writer.
- Why not: it runs in CI on the catalogue's example records, and the writer enforces the catalogue's JSON Schema.

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
- Cons: the amount of a meter (bytes, tokens, a count) is often not in the security record, so the writer would need a second input anyway; the writer would parse a record to find numbers, and a catalogue change would change billing silently.
- Why not: the emitter builds both parts from one call, and the catalogue entry declares the meters, so the writer only checks.

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
