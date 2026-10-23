# Storage layout

Layout v5: a logical key ([keys](keys.md)) is a module, a kind and an id, mapped in `internal/port/layout5.go` ([ADR 0036](../../decisions/0036-configuration-is-immutable-per-instance.md), [ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md)).

| Module | Owns |
|---|---|
| `oidc` | the issuer's and the console's records and secrets |
| `github` | organisations, GitHub Apps, people's links |
| `slack` | workspaces, Slack Apps, channels |
| `cloudflare` | the minter and the minted tokens |
| `google` | Google Workspaces, the directory refresh |
| `backup` | the runs, the restores, the restore identity |
| layouts v3 and v4 | removed: [upgrade to v1.77.1](../../guides/sluis/upgrade/v1.77.1.md) |

## SSM (the `ssm` Secrets adapter)

| SSM path below `<root>/` | What it is | Who writes it |
|---|---|---|
| `internal/oidc/state-secret` | the issuer's sign-in state secret, base64 of 32 random bytes | `deploy/pulumi` generates it; on Kubernetes an operator seeds it |
| `internal/oidc/recovery-password` | the recovery password, read by operators only | `deploy/pulumi` generates it |
| `internal/oidc/recovery-verifier`, `conformance-verifier`, `conformance-password` | the SRP credentials of the two password accounts and the conformance password. The issuer's role reads the verifiers and is denied the passwords | `deploy/pulumi` derives them |
| `internal/oidc/signin/<provider>/client-id`, `client-secret` | the Google sign-in client (`oauthClient.provider`) | an operator seeds it |
| `internal/oidc/clients/<id>` | a delivered client secret, the document's `clients/<id>/secret` | an operator seeds it |
| `internal/oidc/console-session-key` | the key the console signs its sessions with | sluis |
| `internal/oidc/outline/<workspace>/<ref>` | an Outline workspace's API key; the issuer hosts the [Outline controller](outline.md) | sluis |
| `internal/google/workspaces/<id>/key` | a Google Workspace's service-account key, replaced in place | sluis |
| `internal/github/apps/<id>/<ref>` | a GitHub App's key ([GitHub Apps](#github-apps)) | sluis |
| `internal/github/links/<github-user-id>/<ref>` | a person's GitHub token pair | sluis |
| `internal/slack/workspaces/<team>/<ref>` | a Slack workspace's client secret and bot token | sluis |
| `internal/slack/apps/<id>/<ref>` | a Slack App's client secret | sluis |
| `internal/cloudflare/<account>/minter` | an account's minter credential | an operator seeds it |
| `internal/cloudflare/minted/<preset>` | the record of the tokens minted for a preset | sluis |
| `internal/<module>/<name>` | static S3 credentials (`credentialsRef`), in the module that reads them | an operator seeds it |
| `internal/backup/restore-identity` | the restore identity, an age key (`AGE-SECRET-KEY-1...`). The restore role reads it, the backup role never | `deploy/pulumi` generates it |
| `external/oidc/<id>` | a generated or seeded client's secret, an `oidc/v1` document | sluis, or an operator |
| `external/github/<id>` | an installed App's key, a `github/v1` document | sluis |
| `external/slack/<id>` | a catalogue Slack App's bot token, a `slack/v1` document | sluis |
| `external/cloudflare/<preset>` | a preset's current credential, a `cloudflare/v1` document | sluis |

| Rule | Meaning |
|---|---|
| `<root>` | `/sluis/<instance>`, the service document's `secrets.root`, with `secrets.layout: v5`. An instance is not named `private`, `export`, `internal` or `external` |
| `internal/<module>/*` | That module's alone; IAM follows the module ([AWS Lambda](lambda.md#iam-one-role)) |
| `external/<module>/<id>` | A typed document a consumer reads ([secrets](secrets.md#the-external-documents)) |
| Value | An `internal/` secret is a `{"value":"<base64>"}` document. The minter, the minted records, the S3 credentials and `external/` are typed JSON documents |
| `<ref>` | A fresh random name per write, kept by the record that names it. A credential is never replaced in place, so a losing writer cannot overwrite the winner's secret |
| Stable items | The console session key and a Google Workspace's key have no ref |
| Unsafe segment | An empty segment, `.`, `..`, one starting `u-`, or one with a character other than letters, digits, `.`, `_` and `-` is written `u-` plus its bytes in hex |

## DynamoDB (the `dynamodb` State, Index and Trigger adapter)

One table per module, `ports.dynamodb.tables`, by default `sluis-<instance>-<module>` ([adapter details](port-adapters.md#the-dynamodb-adapter)).

| Attribute | Type | Meaning |
|---|---|---|
| `pk` | S | partition key: the record kind, or the Index set's kind for a member. `dynamodb:LeadingKeys` grants a role the kinds it writes |
| `sk` | S | sort key: the id (at most 1 KiB), or `<set id>/<member>` |
| `lkey` | S | the logical key the item was written for (the set, for a member) |
| `v` | B | the value (State) |
| `rev` | N | the revision: a random 64-bit number drawn on every write |
| `expires` | N | epoch seconds the item is dead from; absent when permanent. The table's TTL attribute |
| `k` | S | `i` for an Index member; absent for a State record, so no State listing returns a member |

| Logical key | Table | `pk` | `sk` | What it is |
|---|---|---|---|---|
| `ws.dir.google.<id>` | `google` | `workspace` | `<id>` | a connected Google Workspace. A directory of another backend has no module and is refused |
| `lease.refresh:<id>` | `google` | `lease` | `refresh/<id>` | the directory refresh's lease |
| `gh.org.<org>` | `github` | `org` | `<org>` | a connected GitHub organisation |
| `gh.link.<account>` | `github` | `link` | `<account>` | a person's GitHub link |
| `app.gh.link` | `github` | `app` | `link` | the link App |
| `app.gh.cat.<id>` | `github` | `app` | `<id>` | a catalogue GitHub App |
| `app.gh.runner.<tier>.<org>` | `github` | `app` | `runner-<tier>-<org>` | a runner App |
| `gate.github-claim.<account>` | `github` | `claim` | `<account>` | the marker of a link claim |
| `gate.github.<org>.confirm`, `.pass` | `github` | `gate` | `<org>/confirm`, `<org>/pass` | an operator's confirmation of a removal set, a request for a pass |
| `lease.github-tick:<org>`, `lease.github-links:<org>`, `lease.export:github-app.<id>` | `github` | `lease` | `<kind>/<target>` | a tick's lease |
| `ws.slack.<team>` | `slack` | `workspace` | `<team>` | a connected Slack workspace |
| `app.slack.cat.<id>` | `slack` | `app` | `<id>` | a catalogue Slack App |
| `rec.slack.shared.<name>` | `slack` | `shared` | `<name>` | a Slack Connect channel definition |
| `rec.slack.channel.<team>.<name>` | `slack` | `channel` | `<team>/<name>` | a console channel's record |
| `gate.slack.<team>.confirm[.<channel>]`, `.pass` | `slack` | `gate` | `<team>/confirm[/<channel>]`, `<team>/pass` | the same for Slack |
| `share.<host>.<channel>` | `slack` | `share` | `<host>/<channel>` | a Slack Connect share |
| `cache.slack.user.<team>.<id>` | `slack` | `user-cache` | `<team>/<id>` | who a Slack member is (24 h) |
| `lease.slack-tick:<team>`, `lease.export:slack-app.<id>` | `slack` | `lease` | `<kind>/<target>` | a tick's lease |
| `lease.cloudflare-tick:<target>` | `cloudflare` | `lease` | `cloudflare-tick/<target>` | the rotation's lease |
| `rec.backup.run.<id>`, `rec.backup.restore.<id>` | `backup` | `run`, `restore` | `<id>` | the status of a backup run, of a restore |
| `rec.backup.retention` | `backup` | `retention` | `last` | the last retention pass |
| `lease.backup:run`, `lease.restore:run` | `backup` | `lease` | `backup/run`, `restore/run` | one backup, one restore at a time |
| `rec.console.session-key` | `oidc` | `console` | `session-key` | the console's key (the key itself is in Secrets) |
| `rec.recovery.off` | `oidc` | `recovery` | `off` | the recovery switch: the set `disabled` of `generation`, `at`, `by`. The issuer and console read it at most every 3 s, and once seen never again. Never exported by a backup |
| `issuer:request:<id>`, `req.<id>` | `oidc` | `request` | `<id>` | a pending authorization request |
| `issuer:code:<id>`, `code.<id>` | `oidc` | `code` | `<id>` | an authorization code |
| `issuer:code-session:<id>`, `codesess.<id>` | `oidc` | `code-session` | `<id>` | legacy: the session a code opened; no longer written |
| `issuer:code-spent:<sha256>` | `oidc` | `code-spent` | `<sha256>` | the tombstone of a redeemed code: the access token and session it issued |
| `srp.<handle>` | `oidc` | `srp` | `<handle>` | the half of a password sign-in exchange (SRP) that outlives its first request, for one use and two minutes. Never exported by a backup |
| `issuer:token:<jti>`, `tok.<jti>` | `oidc` | `token` | `<jti>` | a minted token's record |
| `issuer:sso:<id>`, `sso.<id>` | `oidc` | `sso` | `<id>` | the browser's SSO session |
| `issuer:sso-of:<identity>` | `oidc` | `sso-of` | `<identity>` | an identity's SSO session |
| `issuer:sso-cookie:<hash>` | `oidc` | `sso-cookie` | `<hash>` | pointer from the browser's SSO cookie, hashed, to the sign-in id, for the record's lifetime. The `sso` record carries the same hash as `cookie_hash` |
| `issuer:session:<id>` | `oidc` | `session` | `<id>` | a session |
| `ses.<person>.<sid>`, `sid.<sid>` | `oidc` | `session`, `session-pointer` | `<person>/<sid>`, `<sid>` | a session of the layout's own form |
| `issuer:session-token:<hash>`, `rt.<hash>` | `oidc` | `session-token` | `<hash>` | a live refresh token; once spent, `spent:<unix ms>:<sealed successor>:<session id>` |
| `issuer:session-rotated:<hash>`, `rtrot.<hash>` | `oidc` | `session-rotated` | `<hash>` | legacy: a spent token's successor as an older version wrote it; read, never written |
| `issuer:keyring:entry:<alg>:<kid>`, `keyring.<alg>:<kid>` | `oidc` | `keyring` | `<alg>/<kid>` | a signing key's schedule. A `kms-wrapped` entry also carries `wrapped`, the private key encrypted under the symmetric KMS key; never plaintext |
| `issuer:keyring:retired:<alg>:<kid>` | `oidc` | `keyring-retired` | `<alg>/<kid>` | the tombstone of a retired key |
| `issuer:held:<identity>` | `oidc` | `held` | `<identity>` | an identity's last-known directory groups, kept for the hold window (`lifetimes.hold`) |
| `issuer:kms:state-secret-fingerprint` | `oidc` | `guard` | `state-secret-fingerprint` | the guard that a state secret has not changed |
| `rec.maintenance` | every table | `maintenance` | `flag` | the maintenance flag: fields `state`, `since`, `by`, `reason`. While it exists the module refuses writes (503 with `Retry-After`) and keeps serving reads, discovery and the key set, cached for 10 s. Only the restore role writes it |
| `lease.<kind>:<target>` of another kind, `notify.<target>` | the writer's table | `lease`, `notify` | `<kind>/<target>`, `<target>` | a lease, a notification (a minute) |
| `gate.`, `cache.`, `dedupe.` and the rest of the key | the writer's table | `gate`, `cache`, `dedupe` | the rest of the key, `/`-separated | ledger entries, shared inputs, idempotency markers |
| any other key | the writer's table | `other` | the whole logical key | a key the layout names no kind for (a test's) |

The `oidc` table refuses a lease whose target names another module.

| Index set (all `oidc`) | `pk` | `sk` |
|---|---|---|
| `issuer:keyring:index:<alg>` | `keyring-index` | `<alg>/<kid>` |
| `issuer:sso-clients:<sso id>`, `issuer:sso-of:<identity>` | `sso-clients`, `sso-of` | `<set id>/<member>` |
| `issuer:sessions-of:<identity>`, `issuer:sessions-for:<client>` | `sessions-of`, `sessions-for` | `<set id>/<member>` |
| `issuer:sso`, `issuer:sessions` | `sso-index`, `sessions-index` | `all/<member>` |

| Session rule | Behavior |
|---|---|
| Rotation mark | `session-token` holds `spent:<unix ms>:<sealed successor>:<session id>`. The successor is sealed with AES-256-GCM under a key derived from the spent token. A retry within the 30-second grace finds the successor; a later presentation ends the session the mark names. A mark that does not open with the presented token counts as an unknown token |
| Mark lifetime | `max(auth_time + absolute limit - now, 30 s)` (24 hours by default). Without `auth_time` or an absolute limit: until the session ends, at most the refresh lifetime (12 hours) |
| Mark write | Only over the revision read, so a concurrent refresh is a replay and a revocation is not undone |
| Index membership | `session` carries `IndexedUntil` and `Involved`. A member is added with twice the refresh lifetime and re-added only when it would lapse: one `Add` per 12 hours by default |
| `held` | Rewritten only when the groups change, when the record is not this process's own write (`RevisionPeeker`), or when older than hold window / 8. A hold can end up to an eighth early, never late |
| `session-rotated` | Legacy: read, never written |

| Listing | Query |
|---|---|
| Prefix in one kind (`rec.slack.channel.acme.`) | `Query` on `pk` in the kind's table with `begins_with(sk, "acme/")`, in key order |
| Prefix naming several kinds (`ws.`, `gate.`, empty) | `Scan` filtered on `lkey`, sorted in memory; for operators and `sluis migrate` |
| Index member | `sk` = `<set id>/<member>`; `/` and `~` in a set id are `~2F` and `~7E` |

## Another Secrets store

With the `openbao` or `memory` Secrets adapter, a credential is at `credentials/<module>/<kind>/<id>/<ref>`. State names only the `<ref>`.

| Credential | Secrets path |
|---|---|
| a Google Workspace's key | `credentials/google/workspace/<id>/<ref>` |
| an organisation's App key | `credentials/github/org/<org>/<ref>` |
| the link App, a catalogue App, a runner App | `credentials/github/app/<id>/<ref>`, the id being `link`, the App's id or `runner-<tier>-<org>` |
| a person's GitHub link | `credentials/github/link/<account>/<ref>` |
| a Slack workspace | `credentials/slack/workspace/<team>/<ref>` |
| a Slack App | `credentials/slack/app/<id>/<ref>` |
| the console's session key | `credentials/oidc/console/session-key` |
| a key no module owns, such as a directory of another backend | `credentials/other/<whole key>/<ref>` |
| a generated client's record | `credentials/oidc-client/<id>/secret` |
| a segment that is empty, holds a `~`, starts `u-` or is invalid | `u-` plus its bytes in hex |

## GitHub Apps

| App | Purpose | Id | Credential | Exported |
|---|---|---|---|---|
| the link App | `link` | `link` | `<root>/internal/github/apps/link/<ref>` | never |
| a catalogue App | `catalogue` | its own id (not `link`, not `runner-…`) | `<root>/internal/github/apps/<id>/<ref>` | `<root>/external/github/<id>` with `export: true` |
| a runner App | `runner` | `runner-<tier>-<org>` | `<root>/internal/github/apps/runner-<tier>-<org>/<ref>` | `<root>/external/github/runner-<tier>-<org>`, always |

| Rule | Where |
|---|---|
| One kind `app` in the `github` table, keyed by the id; the logical keys do not change | [ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md) |
| A record has `purpose` and `labels` (16 at most, short lower-case names); one without a purpose is read with its key's | `appid` package |
| An exported key is kept once, in the external document; each internal write has a fresh `<ref>`. Only the `ssm` source exports | the GitHub Apps store |
| An organisation's `app_ref` names the App whose key it uses; on SSM, a connection without it is refused | the organisation record |
| Runner Apps whose tier and organisation join to one id are refused at the second write | the GitHub Apps store |

## Backup archive

| Object under `backup/<instance>/` | Content |
|---|---|
| `<id>.tar.gz.age` | The backup: a PAX tar, gzip, encrypted with age to `backup.recipients`. `manifest.json` first, then per module `state.jsonl`, `secrets.jsonl` and `blobs/<key>` ([format 2](backup.md#format-2)) |
| `<id>.json` | The sidecar, written last: object key, size, SHA-256, recipients and per-module counts. Never a secret. An object without its sidecar is not a backup |

## S3 (the `s3` Blob adapter)

| Prefix | Content |
|---|---|
| `github/reports/<target>`, `slack/reports/<target>` | What each controller last reported |
| `google/cache/<workspace>` | The hub cache, never migrated |

## Exports (retired)

The layout v3 copies, `<root>/export/<path>`, are retired ([ADR 0041](../../decisions/0041-the-secret-contract.md)). [Exports](exports.md) lists the old sources.
