# 0021 — The audit log is sealed batches in a blob with a lock mode

**Status:** Accepted
**Date:** 2026-10-31

Decided by the maintainer. The record format was [OD-40](/open-decisions/#od-40). Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). Build: [issue #837](https://github.com/truvity/sluis/issues/837), [issue #838](https://github.com/truvity/sluis/issues/838), and the off-site copy from Garage, later, in [issue #984](https://github.com/truvity/sluis/issues/984).
Rewritten in place on 2026-10-31 (the record format, the last open point of [issue #910](https://github.com/truvity/sluis/issues/910), decided: an audit record is a plain OCSF 1.x event with no sluis extension, and a parallel usage log in CloudEvents 1.0 JSON is written by the same writer. Every rule of this record now holds for each log kind. The record is Accepted).
Rewritten in place on 2026-10-31 (the OCSF check finished with no gap and no extension; the maintainer decided its follow-ups as recommended: certificate issuance is Authentication with the `certificate` object, scopes live in the grant record, the key binding goes in `policy.data`, the OCSF toolkit runs in CI only, and Go and Rust emitters validate their own entries; the pinned version, the profiles and the library picks are recorded).
Rewritten in place on 2026-10-31 (on the boards the log is on R2 in the first release; Garage, with the hourly off-site copy, is a later backend, after the spike in [issue #982](https://github.com/truvity/sluis/issues/982)).
Rewritten in place on 2026-10-31 (the write-once fence stays in the blob store, and only blob backends with an atomic create-if-absent are supported; the finer points of the OCSF mapping are recorded; the points that earlier rewrites marked as inferred are confirmed by the maintainer, and their markers are removed).

## Context

[0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/) split audit into four pieces and decided the shape of the log piece: exactly one writer per log, one log per node on a cluster, a log store that is a `blob` with a lock mode, an optional witness and no notary. It left open how a log is laid out in the blob store, how and when the writer seals what it has received, what a record contains, what each blob backend's lock mode means for an installation, and how long the log is kept.

v1 answered the log questions for one runtime. Records went to S3 with Object Lock in compliance mode as the record, every other store being a projection (v1 ADR 0045, on `master`). A notary sealed each hour per profile and tenant with a JWS over an RFC 6962 Merkle root (v1 ADRs 0050 and 0061). The record was a Protobuf core with JSON Schema extension slots (v1 ADR 0043). v2 starts again from the decisions below; the maintainer asked for a record format that follows a published standard, and v1's format is not carried over.

Six things shape v2:

- **The formats are the C2SP ones.** sluis follows published specifications and invents no log format. The maintainer adopted C2SP `signed-note` (with the P-384 log key), `tlog-checkpoint`, `tlog-tiles`, `tlog-cosignature` (witness keys Ed25519 or ML-DSA-44) and `tlog-witness`, and `ca` logs its certificates in a C2SP `static-ct-api` log ([0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/) names the algorithm exception: a followed format fixes its own algorithm).
- **There are several backends with different locks.** AWS S3 has Object Lock in compliance mode. Cloudflare R2 has a lock that an account administrator can remove. Garage, a blob store the boards could run themselves, has no lock, no versioning and no conditional writes (measured on v2.5.0 on 2026-10-31, [issue #982](https://github.com/truvity/sluis/issues/982)). A local file system has none. On the boards the log lives on R2 in the first release, and Garage is a later backend ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).
- **Boundaries and companies.** Each environment boundary owns its audit log. Each company is a reader tenant of that log with its own audit tenant key, and a company that leaves has its slice exported and its tenant key crypto-shredded ([0009](/decisions/0009-one-installation-per-boundary/)).
- **Data has an origin.** [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/) says what an audit record carries for each origin and that records carry enough to roll state forward after a restore. The log itself is outside the origin model: it is the record.
- **OCSF is the security event schema that is maintained and consumed.** The Open Cybersecurity Schema Framework has been a Linux Foundation project since 2024-11, under Apache-2.0, with written compatibility rules: a minor release only adds, and only a major release removes. Its current release is 1.9.0 (2026-08-03), with about three minor releases a year and no 2.0 announced. It has core classes for authentication, user, role and entity management and API activity, a profile for policy decisions, and a `record_integrity` profile whose fingerprint can name RFC 8785 as its serialisation. It is the native schema of AWS Security Lake and is read by the pipeline vendors. It has no class of its own for issuing a certificate or a token, so those use the nearest core activity. (Checked 2026-10-31 against the schema repository at tag `1.9.0` and the live validator at `schema.ocsf.io`.)
- **Billing and quota want a different record than security does.** A usage fact (an account, a meter, an amount) is read by quota and by finance, has its own retention, and has a usage-event standard of its own; putting it into a security schema would tie both audiences to one format and one retention.

The volume measured today is about 2.4 records a second at peak and about 75,000 a week. The threats this record answers are T-10, T-12, T-13, T-14 and T-26 of the [threat model](/security/threat-model/).

## Decision

### Two log kinds: audit and usage

- **A writer writes two kinds of log: an audit log and a usage log.** The audit log holds what happened, as OCSF events (below). The usage log holds what was used, as CloudEvents usage events (below), for quota and for billing.
- **Every rule in this record holds for each log kind separately.** Each kind has its own prefix or bucket, its own log key, its own checkpoints, its own retention and lock mode, its own declared readers and its own off-site copy. Where this record says "the log", it means one log of one kind.
- **The emitter sends one combined message:** an OCSF audit part, plus an optional usage part for an action that uses something. The same writer validates both parts and splits the message into the two log kinds.
- **Both logs' sealed batches are registered in the one atomic state batch,** together with the removed duplicate ids and the transport cursor ([Data](/concepts/data/#the-atomic-batch)). The audit log and the usage log therefore never disagree about which messages were written. There is no dual write and no second writer.
- **Witnesses cosign the checkpoints of both logs,** with the same cadence.
- **Usage entries are encrypted per company, like audit entries** (below).
- **The usage log has no search index.** Quota follows it, and the finance export is derived from it ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).

### The log is a C2SP tiled transparency log in a blob

- **Each log is a `tlog-tiles` log in the blob store.** Its entries are the records of its kind. Entry bundles and hash tiles are static objects that anyone with read access can fetch without a server. The tree hashes follow RFC 6962.
- **A checkpoint is a C2SP `signed-note` in the `tlog-checkpoint` shape,** signed with the log key. It names the log's origin, its size and its root hash. Two consecutive checkpoints of one log are consistent: removing, reordering or altering an entry breaks every later checkpoint.
- **Every object is written once, and the blob store is the fence.** Entry bundles and tiles, including partial tiles, are created only if absent, so a second writer that believes it owns a log cannot overwrite or fork it (T-12). sluis supports only blob backends with an atomic create-if-absent (decided 2026-10-31): **every blob driver refuses to overwrite an existing object on a create-if-absent write** (REQ-AUDIT-103). How each driver does it:

  | Driver and backend | Atomic create-if-absent |
  |---|---|
  | `s3-compatible` on AWS S3 and Cloudflare R2 | `PutObject` with `If-None-Match: *`. The store answers 412 Precondition Failed when the key exists, and of concurrent writers to an absent key at most one succeeds (AWS and Cloudflare documentation, checked 2026-10-31). On a versioned bucket a delete marker counts as absent, which is one more reason no writer holds a delete right |
  | `fs` | Write a temporary file and fsync it, then `link()` or `renameat2(RENAME_NOREPLACE)` it to the target name, both of which fail if the target exists, then fsync the directory. This holds on local POSIX filesystems, not on network filesystems without exclusive-create guarantees |
  | Garage | Excluded until its conditional writes land (upstream [issue 1052](https://git.deuxfleurs.fr/Deuxfleurs/garage/issues/1052)): v2.5.0 accepts `If-None-Match: *` and overwrites ([issue #982](https://github.com/truvity/sluis/issues/982), [0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)) |

- **The port conformance suite tests create-if-absent under concurrent writers for every blob driver** ([issue #851](https://github.com/truvity/sluis/issues/851), REQ-TEST-009).
- **One prefix per log, `<kind>/<boundary>/<log>/`, with the `tlog-tiles` paths below it,** where `<kind>` is `audit` or `usage`. On a store with a bucket per actor, such as Garage, the prefix is inside the writer's bucket. An installation may put the usage log in a bucket of its own instead, for example to give it another lifecycle.
- **Every checkpoint is also written once to `checkpoints/<size>` below the log's prefix, create-if-absent.** The `tlog-tiles` `checkpoint` object, the one object the format rewrites, is a convenience pointer, written with a conditional put on its previous version where the store supports it. Verifiers trust the cosigned history, not the pointer, so the full checkpoint history survives on a store with no versioning.
- **Catalogues and schemas are written once to `<kind>/<boundary>/catalogue/<app>/<version>`,** so each log describes itself. The audit log's schemas include the compiled OCSF schema of the pinned version.
- **The first time a writer uses a catalogue version, it appends a schema entry to the log** that names the digest of the catalogue and schema objects. Witnesses therefore cosign which schemas were in force, and an auditor checks an old record against the schemas its own log names.
- **The origin line of a checkpoint is `<installation host>/<kind>/<boundary>/<log>`.** It names the log to witnesses.
- **There is a log key per log, so a writer holds the use of two log keys, one per kind.** On a node the writer reaches it through its own `key-holder` socket, and `key-holder` lets only the writer use it (T-10). On AWS it is a KMS `ECC_NIST_P384` key whose key policy names only the writer's role. A software key is allowed only where [0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/) allows one (refused in production).

### The writer seals batches

- **The writer receives messages from the transport, validates them, removes duplicates and seals them in batches.** For each message it checks the audit part and any usage part against the schemas of their catalogue entry, refuses an unknown action, and appends each part to the current batch of its log kind. It stamps the time it recorded the entry and its own instance (`writer@<boundary>`, [0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)) in the entry's clear envelope, never into the record, whose bytes it stores unchanged. The emitter is authenticated by the transport driver's own layer (T-13).
- **A batch is sealed at 256 records or 5 seconds after its first record, whichever comes first.** The bound counts messages, and one seal seals the audit batch together with the usage batch of the same messages, if that is not empty (inferred: one atomic state batch registers both). An empty batch is never sealed, so an idle log signs nothing.
- **Each batch ends with a seal entry** that records the batch's record count, the writer instance and the observed clock offset (REQ-AUDIT-017, REQ-AUDIT-036). The offset sits inside the tree, so the witnesses cosign it. Seal entries and schema entries are not records: they are entry kinds of their own, in canonical JSON (RFC 8785) and in clear, because they carry no company data. (their encoding is inferred)
- **A batch is sealed when its checkpoint is signed and written.** The writer writes the batch's entry bundles and tiles, asks the keys port to sign the new checkpoint, writes it, and in one atomic state batch registers the sealed batches of both log kinds, records the ids it removed as duplicates and moves its transport cursor ([Data](/concepts/data/#the-atomic-batch)). A crash leaves either all of that or none of it, for both kinds at once.
- **Delivery to the writer is at least once, and the writer removes duplicates by record id** (REQ-AUDIT-014, REQ-AUDIT-034). The id is a UUIDv7 (RFC 9562), set by the emitter, and it identifies the whole message: the audit record's `metadata.uid` and the usage event's `id` carry the same value. The writer remembers ids for 14 days, the longest retention of a supported queue. A record older than that is quarantined and reported, not written, and an operator releases the quarantine.
- **A blocking emit waits until the record is durable in the transport:** accepted by the queue, acknowledged by the writer in direct mode, or fsync'd to the local spool. Credential and administrative changes use it, so the change fails when that fails (REQ-AUDIT-015). A record that is durable but invalid is found by the writer later; it is quarantined and alarmed.
- **On serverless the writer runs with a reserved concurrency of 1 on a standard SQS queue.** Correctness comes from the create-if-absent fence, not from the limit; the throttling is measured at the Lambda step ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).

### Witnesses

- **A witness is optional everywhere, and `sluis config check` warns when there is none** ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). In production a log without a compliance lock needs one (below).
- **Witness drivers are `none`, `peer` and `external`.** They speak `tlog-witness` and sign `tlog-cosignature` with an Ed25519 or ML-DSA-44 key. On the boards the other two nodes are each log's peer witnesses, two of three.
- **The writer asks every witness to cosign each new checkpoint of each log kind, at most one request per witness and log every 5 seconds.** The window in which only the writer vouches for the log is about one seal.
- **When a log is idle, the writer asks again for the latest checkpoint every 5 minutes.** A cosignature carries a timestamp, so a reader can tell a quiet log from a writer that stopped publishing, to within 5 minutes.

### Lock modes

- **The log store is a blob with an explicit lock mode** of `none`, `removable` or `compliance` (REQ-AUDIT-008). The backend sets it:

  | Backend of the `s3-compatible` or `fs` driver | Lock mode | What it means for the log |
  |---|---|---|
  | AWS S3 | `compliance` | Object Lock in compliance mode: nobody, not even the account's root, can shorten an object's retention or delete it before then |
  | Cloudflare R2 | `removable` | A bucket lock rule exists, and an account administrator can remove it |
  | Garage (later) | `none` | No lock and no versioning; tamper evidence comes from the witnesses |
  | Local file system (`fs`) | `none` | Nothing stops a local administrator; tamper evidence comes from the witnesses |

- **`sluis config check` reports the lock mode of the configured driver** (REQ-AUDIT-028).
- **A production log needs a `compliance` lock, or a weaker lock mode plus a witness** (`peer` or `external`). `sluis config check` refuses anything else in a production boundary, and warns in development and staging. The boards pass with their peer witnesses. A single-node production installation on `fs` needs an `external` witness.
- **No actor's credential carries a delete right on the log store,** except the expiry sweeper's right on expired objects (below). (inferred from the write-once rule)

### Logs per installation

- **A cluster keeps one log of each kind per node,** each with its own writer, spool and log key (REQ-AUDIT-009, REQ-AUDIT-045). The local installation has one log of each kind. The lambda installation has one of each, written by its one writer function. A Kubernetes installation has one of each per writer, each with its KMS key and a lease on the state store.
- **Readers merge the logs of a boundary by the time each record was recorded,** keeping each log's own order and breaking ties by log id (REQ-AUDIT-035). The reader shows each log's clock offset and claims no order across logs closer than that offset. Within one log the order is exact.
- **On the boards each log of each kind is on R2 in the first release,** so it is readable and verifiable from R2 alone. (inferred)
- **With Garage, later, an hourly job copies new log objects from Garage to R2,** for both kinds, each to its own R2 prefix. It is a scheduled job of the audit installation, a timer, not the writer: the writer never touches R2, so a compromised writer cannot reach the off-site copy, and an R2 or internet outage delays the copy, never the log. It copies create-if-absent, reads the log with a read-only Garage key and writes only to its own R2 prefix with credentials from `cloud-cloudflare@<boundary>`. It reports its lag, which the audit console shows as log health ([0062](/decisions/0062-audit-front-is-a-slim-entry-the-reader-as-its-api-and-a-console-of-its-own/)). Where it runs is derived like every placement ([OD-32](/open-decisions/#od-32)); its unit is built in [issue #984](https://github.com/truvity/sluis/issues/984).

### The audit record is a plain OCSF event

- **An audit record is an OCSF 1.x event, with no sluis extension.** It uses core classes, core objects and core profiles only. Where no core activity fits, it uses `activity_id` 99 (`Other`) with an `activity_name`. The sluis action, for example `ssh.certificate.issue`, is `metadata.event_code`; the company is `metadata.tenant_uid`; the record id is `metadata.uid`; the outcome is `status_id` with the reason code in `status_code`; a policy decision is the `security_control` profile's `action_id` and `authorizations`.
- **A record declares the OCSF profiles it uses in `metadata.profiles`:** `security_control` wherever it carries `action_id`, `authorizations` or the top-level `policy`, and `ai_operation` for the actor chain of a token exchange (`delegation`). The catalogue fixes the profile set of each entry (REQ-AUDIT-102).
- **Activity 99 (`Other`) carries the actions OCSF has no activity for:** token issue, refresh, exchange and revoke; certificate issue and revoke; step-up; key sign and decrypt; configuration reload; and audit export. `activity_name` names the activity, and `metadata.event_code` names the sluis action exactly.
- **The record is stored as RFC 8785 canonical JSON, and the same bytes travel end to end.** The emitter produces the canonical bytes, the transport carries them as the message body, and the writer validates them and stores them unchanged. The reader's Connect API returns them as opaque `bytes`, beside typed envelope fields for filtering. Nothing re-encodes a record, into Protobuf or anything else, because a re-encoding would break the hash and proto3's JSON mapping turns 64-bit integers into strings.
- **Each sluis release pins one OCSF version,** named in every record's `metadata.version`. The compiled OCSF schema of that version is stored write-once with the catalogue, and the schema entry names it (above). A minor upgrade is a deliberate change: CI compares the old and the new compiled schemas under OCSF's compatibility rules and fails on any item the catalogue uses that is marked deprecated. Records are never rewritten, so a log holds records of several OCSF versions, each naming its own. The first release pins **OCSF 1.9.0** (2026-08-03).
- **The stored record is lean:** enum ids only, with no enum captions, no `observables` and no enrichments. An export adds them (below).
- **The actor is `actor.user.uid = <actor>@<boundary>`,** the same name as every other layer ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)), for every kind of principal. A person or an external actor is a keyed pseudonym in front of the `@`. `actor.user.type_id` gives the kind: 1 for a user, 2 for an operator (admin), 3 for a sluis actor (system) and 4 for a workload or client principal (service). The OAuth client through which the principal acted is `actor.application`, and the upstream identity provider is `actor.idp`. (the `type_id` values and the two objects are from the research of 2026-10-31)
- **Nothing outside OCSF is added to a record.** Every sluis concept has a core home, or is known from the log or the catalogue:

  | sluis concept | Where it is |
  |---|---|
  | The boundary | The log: each log belongs to one boundary. An export carries it in `metadata.log_name` |
  | The data origin and the sensitivity of what is recorded ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)) | The catalogue entry that `metadata.event_code` names |
  | The catalogue version | The schema entry written in the log |
  | The grant under which the action ran | The OCSF `policy` and `authorizations` objects, with the grant id in `policy.uid` |
  | The OAuth scopes granted | The grant record's typed `privileges[]` (3003 Authorize Session). A token record carries the grant id, not the scopes |
  | The sender binding of a token (DPoP `jkt`, mTLS `x5t#S256`) | `policy.data`, typed by the catalogue |
  | Certificate details (serial, key fingerprint, validity) | The OCSF `certificate` object, which only Authentication (3002) has, so issuance is `3002 / 99 "Certificate Issue"` |
  | A key that was used, created or destroyed | Core resource objects (`resources[]`, or `entity` in Entity Management), with the key's id |
  | The pseudonym key epoch | Encoded in the pseudonym itself |
  | The trace id | `metadata.correlation_uid` |
  | The request id | Only where it is needed, such as quota's idempotency: `api.request.uid` on API Activity. The trace id joins the records of one request |
  | A balance effect or a meter | Not in the audit record: it is the usage event of the same message (below) |

- **Facts with no typed OCSF field go into OCSF's own `json_t` slots,** typed per event code by the catalogue: `policy.data`, `resources[].data`, `entity.data` and `updated_entity.data`, and `application.data`. The check found seven such facts: OAuth scopes and grant type, the sender binding, key algorithm and digest, the quota effect, the roll-forward value, configuration digests and enrolment proof. The scopes are kept typed in the grant record instead (above), and the grant type is implied by its event code (below).
- **The check found no gap (2026-10-31).** 29 catalogue entries and 31 example records, two of them denials, were written in OCSF 1.9.0 with no extension, no `unmapped` and no `enrichments`. The official OCSF toolkit validated all 31 against the compiled schema with no error and no warning, and the generated JSON Schemas gave the same verdict on all 51 test vectors in 16 validator configurations in Go, Rust and JavaScript. A minimal extension comes only if a SIEM later needs one of the `json_t` facts typed, such as the sender binding, as a change to this record.

### How sluis actions map to OCSF classes

The catalogue names one class and activity per action. The mapping, checked on 2026-10-31 against OCSF 1.9.0 with the OCSF toolkit:

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
| Key use, and a key created, rotated or destroyed (including a company key shredded) | 6003 API Activity / 99 "Sign" or "Decrypt", with the key in `resources[]`; 3004 Entity Management |
| A configuration reload | 6002 Application Lifecycle / 99 "Reload" |
| Writer and log lifecycle | 1008 Event Log Activity |

A policy decision is not a record of its own: it is part of the record of the action it gates. OCSF has no class for issuing a certificate or a token, so a SIEM rule keyed on Logon does not see a token grant; that is correct, because a grant is not a logon.

The finer points of the mapping (decided 2026-10-31):

- **One event code per grant type** (authorization code, refresh, client credentials, exchange), so the catalogue implies the grant type and no field carries it.
- **A refresh rotation names two tokens in one record:** `session.uid` is the refresh family, `session.credential_uid` the presented token, and `authentication_token` the new one.
- **Certificate details that the `certificate` object lacks follow fixed catalogue meanings:** `fingerprints[0]` is the certificate's hash, `created_time` its not-before, `certificate.uid` its certificate-transparency entry, the certificate type follows from the event code, SSH critical options and extensions are implied by the profile digest in `policy.version`, and a revocation reason is `status_detail`. The catalogue records the meaning of each field.
- **A key use carries the key's algorithm, and the digest or the encryption context, in `resources[].data`.**
- **A passkey's credential id is a `resources[]` entry of the User Management (3007) record**, not a device.
- **A request id appears only where it is needed** (above).

### The usage event

- **A usage event is a CloudEvents 1.0 event in the structured content mode, as JSON, in RFC 8785 canonical form.** Its `specversion` is `1.0`; the current release of the specification is 1.0.2 (checked 2026-10-31). Its `id` is the message's record id, its `source` is `<actor>@<boundary>`, its `type` names the meter event, its `subject` is the account (the aggregation key, as in OpenMeter), its `time` is the time of use, and its `dataschema` names the catalogue schema.
- **Its `data` is typed usage data:** the account, the meter, the unit, the amount as an integer with its sign, the request id and the subject's pseudonym. Amounts are integers bounded to ±(2^53−1), so canonical JSON keeps them exact; there are no decimals ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).
- **The shape follows OpenMeter's usage events:** a CloudEvents event whose `type` selects the meter, with the value and its grouping keys in `data`, deduplicated by `id` and `source` (OpenMeter documentation, checked 2026-10-31). An OpenMeter-style metering system can therefore read the usage log with little mapping. (the comparison is from the documentation, not a test)
- **Only quota sends a balance effect.** Quota's `Admit`, `Confirm`, `Cancel`, `TopUp` and `Adjust` messages carry a usage part with the effect on a named balance. An application's usage part carries meters only, and the writer refuses a balance effect from any actor but `quota`, by its catalogue entry.
- **The finance export is a FOCUS-style table derived from the usage log.** Its rows use the column names of the FinOps FOCUS specification, version 1.4 (released 2026-06-04, checked 2026-10-31), where they fit: `ChargePeriodStart`, `ChargePeriodEnd`, `ConsumedQuantity`, `ConsumedUnit`, `BillingAccountId`, `SubAccountId` and `SkuMeter`. sluis holds no prices, so cost columns are left to the billing system.

### Rules for every record

- **Every success and every denial is audited.** A denial carries its reason code (REQ-AUDIT-001, REQ-AUDIT-021).
- **A record validates against the schema of its catalogue entry,** and its hash is computed over its canonical JSON form (RFC 8785) (REQ-AUDIT-002, REQ-AUDIT-004). The catalogue's JSON Schema 2020-12 is generated from the compiled OCSF schema, narrowed by sluis's own rules: no names, e-mail addresses or phone numbers, no `raw_data` or `unmapped`, no floating point, and bounded free text. It is laid out as one shared definitions document per profile set with a slim schema per entry, so validator memory does not grow with the catalogue.
- **The writer enforces the catalogue's JSON Schema and stays authoritative.** Go and Rust emitters also validate their own entries in production, loading only those, so an ill-formed record is refused before it is emitted; a browser emitter does not (REQ-AUDIT-099).
- **The OCSF toolkit runs in CI only,** because it is pre-1.0. CI runs the catalogue's example records through it, for OCSF's cross-field rules such as `type_uid`, and runs the test vectors through the validators of Go, Rust and TypeScript; it fails on any toolkit error and on any disagreement (REQ-AUDIT-100, REQ-AUDIT-101).
- **Libraries** (checked 2026-10-31): JSON Schema with `santhosh-tekuri/jsonschema` v6 (Go), `jsonschema` 0.60 (Rust) and ajv 8 (TypeScript); RFC 8785 with Go's `encoding/json/jsontext` (with `gowebpki/jcs` as the fallback for a build without json/v2), `serde_json_canonicalizer` (Rust) and `canonicalize` (TypeScript). sluis generates its own Go, Rust and TypeScript types and builders from the compiled OCSF schema, for the classes and objects the catalogue uses, because no OCSF type library is mature enough.
- **A record never carries a secret, an e-mail address, a name or a token** (REQ-AUDIT-003, REQ-AUDIT-037). A secret appears as a fingerprint and a revision. A free-text field is bounded in length (REQ-AUDIT-046).
- **An external actor is recorded under a keyed pseudonym** (REQ-AUDIT-023). The pseudonymisation mechanism is ADR 0040.
- **What a record says follows the origin of what it records,** as the table in [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/#audit-and-telemetry-by-origin) says. A write to a kind marked for roll-forward carries the full value apart from secrets (REQ-AUDIT-019).
- **A record may carry a trace id,** in `metadata.correlation_uid`, one way only: the record points at the trace, and the trace never carries what the record says (ADR 0036).
- **Balance effects live in the usage log, and only quota writes them** ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)). Quota rolls its balances forward from the usage log.

### Export

- **The audit export in 2.0 is NDJSON of OCSF events,** decrypted for the caller's grant ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)). It adds what the stored form leaves out: enum captions and `observables`, and the `record_integrity` profile's attestation, whose fingerprint (SHA-256 over the RFC 8785 bytes) and chain id point back to the log entry. It carries the log's boundary in `metadata.log_name`. Any OCSF-aware pipeline reads it with no sluis-specific mapping.
- **Exporters for a particular target are drivers, later.** AWS Security Lake, for example, takes Parquet and OCSF 1.3 at most for a custom source (checked 2026-10-31), so its exporter maps down between versions. The down-mapping lives in the exporter, never in the record.

### Companies, encryption and erasure

- **There is one audit log per environment boundary, and each company is a reader tenant with its own audit tenant key** ([0009](/decisions/0009-one-installation-per-boundary/)). Records are routed to a tenant at write time (REQ-AUDIT-020).
- **Each entry is an envelope in clear plus the record, encrypted with HPKE (RFC 9180) to the company's tenant key.** The envelope holds the record id, its times, the company, the action class, the outcome and the actor kind; the boundary is the log's own. The record is encrypted with a data key per (company, batch), and the data key is sealed with HPKE to the tenant's public key, so the writer never holds a key that decrypts what it wrote. A machine-plane record, which belongs to no company, is encrypted to the boundary's own audit key.
- **The envelope uses OCSF field names** (`metadata.uid`, `time`, `metadata.tenant_uid`, `class_uid` and `activity_id`, `status_id`, `actor.user.type_id`), plus the writer's stamps (above). Apart from those stamps it is a projection of the record, computed by the writer before it encrypts, so a reader that decrypts checks that the two agree, and a writer that lies in the envelope is caught. The envelope is not an OCSF event of its own, and it leaves out `metadata.event_code`, which says more than the class does. (the projection check is from the research of 2026-10-31)
- **A usage entry has the same form:** an envelope in clear (the id, the time, the company and the event `type`) plus the usage event encrypted to the same company key. Shredding a company's key makes its usage entries unreadable too.
- **The tree's leaf is the hash of the whole entry, ciphertext included.** The record's own RFC 8785 hash is inside the ciphertext. A log therefore verifies after a company's key is shredded (T-14, T-26).
- **The operator's own view of a tenant's records is the envelope:** the class, the outcome and rotating pseudonyms (REQ-AUDIT-038).
- **A company that leaves has its slice exported, then its audit tenant key crypto-shredded** (the promotion runbook of [0009](/decisions/0009-one-installation-per-boundary/)). Its records stay in the log, sealed and verifiable, and nobody can read them again.
- **There are no per-subject keys in 2.0.** The erasure of one person is met by the pseudonym: the mapping from a pseudonym to the person lives outside the log, and erasure deletes it. The company key is the only key that shreds the log. REQ-AUDIT-018, which asked for a per-subject key, is withdrawn in favour of this.

### Retention and legal hold

- **Each log declares its retention, and a production log must declare one.** The audit log and the usage log declare theirs separately, so billing data can be kept as long as finance needs it without holding security records as long. There is no built-in default, so the period is chosen consciously and is visible in the install file and in `sluis config check`.
- **Each backend enforces the retention in its own way:** as per-object retention on a compliance store, set when the object is written and only ever extended; as the bucket lock rule on R2; and as a lifecycle rule or a separate expiry sweeper elsewhere. On Garage and `fs` the sweeper holds a delete right on expired objects only.
- **A trial in governance mode before compliance is recommended in the documentation, not enforced.** A wrong period locked in compliance mode cannot be shortened.
- **Legal holds stay outside sluis.** A hold is placed and lifted with the store's own means, under cloud IAM or a break-glass role, and the person who does it records it as an audit record under their own name. sluis neither sets nor checks holds.
- **Nothing in sluis stops a company-key shred during a hold.** The shred procedure of the promotion runbook must therefore check for a standing hold before it destroys a company's key, and the expiry procedure on a store without a hold flag (R2, Garage, `fs`) must suspend expiry for the held range by hand. (that expiry must be suspended by hand is inferred)
- **The log is not part of the ordinary backup.** It is protected by its lock, its witnesses and its copies ([Backup](/concepts/backup/#the-audit-log-is-handled-separately)).

### Recovery

- **Recovery spools its audit record locally and delivers it later,** so a down audit path does not block the emergency way in (ADR 0027).

### Not decided here

- The pseudonymisation of identities in audit, and its key providers: ADR 0040.
- Compliance profiles (purpose-bound copies, and time-stamp anchoring of checkpoints with RFC 3161, REQ-AUDIT-031): ADR 0041.
- Lifecycle tiers (moving old objects to colder storage): ADR 0042. The retention period, the legal hold and the bucket layout, which the plan had put in 0042, are decided here.
- How a reader is authorised: [0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/) and [0062](/decisions/0062-audit-front-is-a-slim-entry-the-reader-as-its-api-and-a-console-of-its-own/). How an emitter is authenticated is decided by each transport driver's own layer ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).
- What a restore does with a witness, and the restore of a node of a cluster: ADR 0035.
- Which actor produces the FOCUS-style finance export, and its contract: with the quota contract ([0023](/decisions/0023-quotas-api-is-idempotent-and-its-data-follows-the-origin-model/)).
- Whether a SIEM needs one of the `json_t` facts typed: that would bring a minimal extension, as a change to this record (above).

## Consequences

- An auditor verifies a log with standard tools: fetch the tiles and checkpoints, check the signature against the published log key and the cosignatures against the witness keys, and recompute the tree. Nothing of sluis's own is needed to read the format.
- A decrypted audit record is a plain OCSF event, so any OCSF-aware tool reads it with no sluis schema and no extension to compile in. The cost is that some sluis meaning lives outside the record: the boundary in the log, the origin and the sensitivity in the catalogue. A record taken out of its log or export loses its boundary, which is why the export writes it into `metadata.log_name`.
- An OCSF record is about 2.8 times the size of a minimal record of sluis's own (about 1.1 KB against 386 bytes for a sign-in, measured in the research of 2026-10-31, with an extension object that the plain form now leaves out). At about four million records a year that is a few gigabytes a year per boundary, which object storage absorbs; the tile count stays the same. Encrypted records do not compress.
- Following OCSF means following its releases. Each upgrade is a pinned, checked change, and readers and the indexer handle each record by the version it names. OCSF keeps deprecating classes (1.9.0 replaced Account Change by User Management), and the CI check catches that.
- Some sluis actions map to OCSF only approximately: token grants and certificate issuance use activity 99 of Authentication, and a quota call is API Activity. A generic OCSF consumer sees the `json_t` facts as opaque JSON, and a reader joins a token record to its grant record by the grant id to see its scopes. A security tool sees these as what they are in OCSF terms, not by their sluis names.
- Billing and quota read a log made for them, in a usage-event standard, with a retention of their own, and never parse security records. The usage log is one more log kind per writer: a second log key, a second set of checkpoints to witness and copy, and a second retention to declare.
- One combined emit and one atomic registration keep the two logs consistent by construction. A writer outage stops both logs together, and a malformed usage part fails its whole message, audit part included.
- A witness can be anyone who speaks `tlog-witness`, so an `external` witness is a choice of party, not a sluis integration. An external witness may rate-limit a busy log; the 5-second bound is what it is asked to bear.
- Every object but the checkpoint pointer is written once, so a compliance lock and create-if-absent together leave no object a writer can change. Each seal writes the checkpoint twice, once as history and once as the pointer.
- A seal costs one signature per log kind that has entries, so at most two. At 256 records or 5 seconds that is at most about 17,000 signatures a day per log when busy and none when idle: a fraction of a second each on a node's YubiKey, a billed request each on KMS. The index, the witnesses and the quota meters lag the log by a few seconds.
- The seal entry and the schema entry are two more entry kinds that the verifier, the indexer and quota must know.
- Garage and `fs` give no lock, and R2's lock can be removed by an account administrator. On the boards the peer witnesses carry the tamper evidence, and a compromised writer that also holds a majority of witnesses is not detected. The risk is written down, as 0004 requires. In production the lock-mode rule refuses a log that has neither a lock nor a witness, and warns about it elsewhere.
- In the first release the boards write each batch to R2, so losing the boards loses no sealed log, and an outage of the link or of R2 delays the log instead. With Garage, later, the off-site copy is up to an hour behind: if the boards are lost, up to an hour of log is lost with them, and that hour has no off-site copy that a party outside the boards has seen. (inferred)
- One log per node means a boundary on the boards has three logs to verify, copy and merge, and two events on different nodes within the clock offset may show in either order.
- The index must decrypt to search beyond the envelope, so the indexer is a declared reader of each company's tenant key ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)). The envelope leaks metadata by design: who did what kind of thing, when, and with what outcome.
- Crypto-shredding a company leaves its ciphertext in a locked log for the rest of the retention, and the log stays verifiable because the leaves hash the ciphertext.
- Pseudonymised records of one person stay linkable to each other after the mapping is deleted, and a contract that demands per-subject shredding cannot be met in 2.0.
- A production installation needs a declared retention. Garage, when it lands, needs an expiry sweeper, the one credential with a delete right on the log, limited to expired objects.
- A legal hold works only where the store has one and only as far as the operator's procedure goes. A shred or an expiry during a hold is stopped by the runbook, not by sluis.
- A spool that was down for more than 14 days needs an operator to release its quarantine.

## Roads not taken

### Keep v1's hourly JWS seals over a Merkle root

- Pros: built and tested in v1; any JOSE library reads a seal.
- Cons: a format of sluis's own, signed by a notary that v2 no longer has, with no standard witness protocol. Readers need sluis's bucket contract to find and recompute anything.
- Why not: the maintainer adopted the C2SP formats, so the log is a tiled transparency log, and witnesses cosign its checkpoints.

### A database as the log

- Pros: easy to query and to page; the index and the log would be one store.
- Cons: a database row can be changed by whoever administers it, and nothing outside proves it was not.
- Why not: the log is a blob with a lock mode, and the index is derived from it ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)).

### Seal every second, or every minute

- Pros: every second puts the index and the witnesses within about a second; every minute signs least, with the clock offset in a checkpoint extension line instead of an entry.
- Cons: every second is up to 86,400 signatures a day per log, which keeps a YubiKey busy and costs money on KMS. Every minute leaves a minute of lag and a larger unwitnessed window, and other C2SP tools ignore extension lines.
- Why not: 256 records or 5 seconds keeps the lag to a few seconds and signs nothing when idle.

### Cosign once a minute, or hourly

- Pros: far fewer witness calls; kind to external witnesses.
- Cons: up to a minute, or an hour, of history that only the writer has vouched for, and freshness proven only to that interval.
- Why not: a cosignature per checkpoint, at most every 5 seconds, with a refresh every 5 minutes when idle.

### Overwrite the checkpoint and rely on versioning, or a bucket per log

- Pros: overwriting is the `tlog-tiles` format exactly; a bucket per log gives per-log credentials and lifecycle on every backend.
- Cons: Garage and `fs` have no versioning, so overwriting loses the checkpoint history and breaks the create-if-absent fence. A bucket per log means many buckets, and creating one becomes part of adding a node.
- Why not: one prefix per log, with every checkpoint also written once under its size.

### ULID ids with a 7-day window, or no window at all

- Pros: ULID is v1 parity; checking every id against the index never misses a late duplicate.
- Cons: ULID is not an RFC, and 7 days is shorter than SQS retention, so a late redelivery can be written twice. Checking the index makes the writer depend on a store that is derived and may be absent.
- Why not: UUIDv7 with a 14-day window.

### A blocking emit that waits for a signed checkpoint, or no blocking emit

- Pros: waiting for the checkpoint means a change happens only when its record is in the log; dropping blocking delivery leaves one code path.
- Cons: waiting adds seconds to every credential change and fails it whenever the writer or its key is down. Dropping it lets an emit error on a credential change go unnoticed.
- Why not: durable in the transport is milliseconds, needs no writer in queue mode, and cannot lose the record after the change.

### Encrypt only the personal fields, or not at all

- Pros: search and verification work on most fields without decrypting; no encryption is simplest.
- Cons: field lists drift, and anything mis-annotated stays readable after a shred. Without encryption a company's actions, targets and data stay readable to anyone with bucket access for the whole retention, and leaving is not real.
- Why not: the whole record is encrypted to the company, and only the envelope is in clear.

### A key per (company, subject)

- Pros: meets REQ-AUDIT-018 as it was written.
- Cons: millions of keys, a key lookup per record on write and read, and backups that hold keys until they age out.
- Why not: the pseudonym mapping is deleted instead, and REQ-AUDIT-018 is withdrawn.

### Require Object Lock in compliance mode everywhere, or only warn

- Pros: compliance everywhere is the strongest rule; only warning refuses nothing.
- Cons: only AWS S3 offers compliance among the supported backends, so the boards could never hold a production log. Only warning leaves a production log with no protection at all one warning away.
- Why not: production needs compliance or a witness, and other environments are warned.

### A built-in retention, or keep forever

- Pros: a default of 400 days works out of the box and covers a yearly audit; keeping forever needs no expiry code.
- Cons: a wrong default locked in compliance mode cannot be shortened. Keeping forever grows storage without bound and keeps a company's ciphertext after its shred.
- Why not: a declared retention, required in production.

### Legal holds as a sluis record that the writer enforces

- Pros: one mechanism on every backend; a hold could not be dodged by shredding, because the shred would be refused.
- Cons: the writer gains the right to set legal holds, and the expiry sweeper and the key shred must check holds.
- Why not: the maintainer kept holds outside sluis, under cloud IAM and a break-glass role, recorded as audit records. The shred procedure checks them.

### The writer writes to R2 too, or a replicator actor per log

- Pros: the writer writing both stores keeps the copy never behind; a replicator per log copies within a minute.
- Cons: the writer would need internet access and R2 credentials, and an R2 outage would slow or block sealing. A replicator is one more actor kind in the audit pieces.
- Why not: an hourly job is simple and cheap, and keeps the writer away from R2. The cost is the hour of exposure above. This road is about the later Garage backend; in the first release the boards' log is on R2 itself.

### A causal order across node logs, or a merge log

- Pros: a hybrid logical clock gives a real order for events that saw each other; a merge log gives one verifiable order for everything.
- Cons: a hybrid clock makes writers read their peers' logs, coupling the nodes that one log per node keeps apart. A merge log needs a fourth writer and key per boundary, which is the one-writer problem again.
- Why not: merge by recorded time, and claim no order closer than the clock offset.

### A log per company

- Pros: per-company retention and lifecycle by prefix, and leaving is deleting a prefix.
- Cons: a writer and a key per company and node, many more checkpoints to sign and witness, and a record that concerns two companies has no single home.
- Why not: the maintainer chose one log per environment with each company as a reader tenant that has its own key; leaving is crypto-shredding that key.

### A shared log per cluster with a leader as the writer

- Pros: one log to read and verify.
- Cons: a failover can leave two writers for a moment, and the log forks.
- Why not: decided in 0004: one log per node, and no election.

### Time-stamp anchoring (RFC 3161) as the main protection against rewriting

- Pros: a third party dates each checkpoint, which a writer cannot fake.
- Cons: a time-stamp authority proves when a hash existed, not that a reader was shown the same history as everyone else, and it is another dependency.
- Why not: witnesses cosign checkpoints with the C2SP protocol and protect against a split view. Anchoring stays a possible compliance-profile option (ADR 0041).

### Keep v1's record: a Protobuf core with JSON Schema slots

- Pros: built and tested in v1, compact, typed in every language.
- Cons: a format of sluis's own that no security tool reads without a mapping, and a drift test between the Protobuf and the JSON Schema to keep.
- Why not: the maintainer asked for a standards-based format and not to carry v1's over.

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

### A registered `sluis` OCSF extension with one namespaced object

- Pros: every sluis field (boundary, origin, sensitivity, catalogue entry, grant, credential, key, pseudonym epoch) in one place in every record; the research of 2026-10-31 leaned this way.
- Cons: an extension id to reserve and maintain, a compile step for every consumer that validates, and pipelines that drop unknown keys or need a rule for them. Most of its fields repeat what the log, the catalogue or a core object already says.
- Why not: the maintainer chose plain OCSF. Each concept has a core home or is known from the log or the catalogue, and the check of 2026-10-31 found no gap; an extension comes only if a SIEM needs a `json_t` fact typed.

### Certificate issuance as User Management / Add Programmatic Credentials

- Pros: "a credential added to a principal" is the closer meaning, and the first mapping used it.
- Cons: User Management has no `certificate` object, so the certificate's serial, issuer, subject, names and validity would be opaque JSON.
- Why not: Authentication (3002) with activity 99 "Certificate Issue" keeps the certificate typed (decided 2026-10-31).

### The scopes in the token record's `policy.data`

- Pros: one record says everything about a token.
- Cons: the scopes are opaque to a SIEM there, and repeated from the grant.
- Why not: the grant record's `privileges[]` types them once; the token record carries the grant id (decided 2026-10-31).

### The OCSF toolkit in the writer

- Pros: OCSF's cross-field rules are checked on every record, at about 7 µs each.
- Cons: the toolkit is pre-1.0, and a minor release may break the writer.
- Why not: it runs in CI on the catalogue's example records, and the writer enforces the catalogue's JSON Schema (decided 2026-10-31).

### The fence in the state store, a signing proxy, or both

- Compare-and-set on the checkpoint in the state store. Pros: it works on any blob backend, Garage included. Cons: the authority over the log moves from the log store to state, a second store the writer must reach on every seal.
- A signing proxy in front of the blob store that refuses overwrites. Pros: any backend, and a place for finer scopes. Cons: a mediator on the trusted path that holds a master key and re-implements the store's policy.
- Both. Cons: two mechanisms to build, test and keep in agreement.
- Why not: the store that holds the log is the fence, and only backends that can fence are supported (decided 2026-10-31).

### OCSF's `unmapped` object for sluis fields

- Pros: valid OCSF with no extension.
- Cons: the OCSF FAQ advises native producers against it, and its content is free-form.
- Why not: nothing goes into `unmapped`, and the catalogue forbids it.

### Protobuf on the transport, converted to JSON by the writer

- Pros: smaller messages and typed producers.
- Cons: the writer would produce the bytes that are hashed, so the emitter's record and the stored record could differ, and proto3's JSON mapping does not match OCSF's types.
- Why not: the same canonical bytes end to end.

### Store the enriched form

- Pros: an export would need no work, and a reader sees captions at once.
- Cons: every record carries derived data, larger, and the derived data would be part of the hashed evidence.
- Why not: the lean form is stored and an export enriches it.

### Billing derived from the audit log through an OCSF extension

- Pros: one log, one writer path and one retention.
- Cons: billing data in a security schema, an extension after all, a retention that ties finance and security together, and billing readers that must decrypt and parse security records.
- Why not: a parallel usage log in a usage-event standard, with its own retention and readers.

### A separate writer for the usage log

- Pros: the usage path could be scaled and placed on its own.
- Cons: two writers fed from one emit are a dual write: a crash between them leaves one log with an event the other lacks, and reconciling them needs a protocol of its own. It is also a second writer per node to fence and key.
- Why not: the same writer splits one message into both kinds and registers both in one atomic state batch.

### OCSF for the usage log as well

- Pros: one record format for both kinds.
- Cons: OCSF has no usage or metering class, so every meter would be API Activity with its meaning in a field that OCSF does not define, which is the extension again.
- Why not: CloudEvents with typed usage data is the shape metering systems already read.
