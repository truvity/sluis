# Storage layout

Layout v4 for SSM, and the AWS adapters' tables. A record has a kind and an id: `pk` and `sk` in DynamoDB, `credentials/<kind>/<id>/<ref>` in Secrets, `a/b` for a two-part id. The mapping is in `internal/port/keys.go`, logical keys in [keys](keys.md). Decided in [ADR 0036](../../decisions/0036-configuration-is-immutable-per-instance.md).

## SSM (the `ssm` Secrets adapter)

`<root>` is `/sluis/<instance>`, the serve document's `secrets.root`. An instance is not named `private`, `export`, `internal` or `external`. `<root>/internal/*` is sluis's alone: `config/` holds secrets a document names ([names](secrets.md#the-names)), `credentials/` what sluis writes. `<root>/external/<kind>/<id>` is a typed document a consumer reads ([secrets](secrets.md#the-external-documents)).

| SSM path | What it is | Who writes it |
|---|---|---|
| `<root>/internal/config/providers/google/<provider>/client-id`, `client-secret` | the Google OAuth client of the directory (`oauthClient.provider`) | an operator seeds it; sluis reads it |
| `<root>/internal/config/clients/<oidc-client-id>/secret` | the secret of a confidential client of the issuer (read as the `oidc/v1` document `external/oidc/<id>`) | an operator seeds it |
| `<root>/internal/config/issuer/state-secret` | the issuer's sign-in state secret (32 random bytes, base64) | `deploy/pulumi` generates it (`StateSecretParameterName(instance)`) |
| `<root>/internal/config/recovery/password` | the recovery password | `deploy/pulumi` generates it |
| `<root>/internal/config/directory/<id>/key` | a declared workspace's service-account key | an operator seeds it |
| `<root>/internal/config/valkey/password` | the shared store's password | an operator seeds it |
| `<root>/internal/credentials/console/session-key` | the key the console signs its sessions with | sluis (one stable item) |
| `<root>/internal/credentials/directory/<provider>/<workspace-id>/<ref>` | an identity directory's credential (`directory/google/<workspace-id>`; later `directory/entra/<tenant-id>`) | sluis |
| `<root>/internal/credentials/github-org/<org>/<ref>` | an organisation's App key | sluis |
| `<root>/internal/credentials/github-app/link/<ref>` | the link App's client secret | sluis |
| `<root>/internal/credentials/github-app/<app-id>/<ref>` | a catalogue GitHub App's key, until it is installed or when it is not exported | sluis |
| `<root>/internal/credentials/github-link/<github-user-id>/<ref>` | a person's GitHub token pair | sluis |
| `<root>/internal/credentials/slack-workspace/<team>/<ref>` | a Slack workspace's client secret and bot token | sluis |
| `<root>/internal/credentials/slack-app/<app-id>/<ref>` | a catalogue Slack App's client secret | sluis |
| `<root>/external/oidc/<id>` | a generated or seeded client's secret, an `oidc/v1` document | sluis, or an operator |
| `<root>/external/github/<id>` | an installed App's key: a catalogue App with `export: true`, or `runner-<tier>-<org>` for a runner App, a `github/v1` document | sluis |
| `<root>/external/slack/<id>` | a catalogue Slack App's bot token, a `slack/v1` document | sluis |

| Rule | Meaning |
|---|---|
| `<ref>` | A fresh random name per write, kept by the record that names it. A credential is never replaced in place, so a losing writer cannot overwrite the winner's secret |
| Console session key | The one stable item without a ref |
| Unsafe id segment | A `~`, an empty segment or one starting `u-` is written `u-` plus its bytes in hex |
| `directory/<provider>/` | The provider is the record's `backend` (`google`). A credential saved before its record is under `google` |

IAM follows the root. Layout v3 (`private/` and `export/`) was removed in v1.75; to move an installation still on it, see [move the secrets to layout v4](../../guides/sluis/migrate/migrate-secrets-layout.md).

## DynamoDB (the `dynamodb` State, Index and Trigger adapter)

One table with string `pk` (hash) and string `sk` (range). `pk` is the kind, so `dynamodb:LeadingKeys` grants a role the kinds it writes. Adapter behavior is in [adapter details](port-adapters.md#the-dynamodb-adapter).

| Attribute | Type | Meaning |
|---|---|---|
| `pk` | S | partition key: the record kind, or the Index set's kind for a member |
| `sk` | S | sort key: the id (at most 1 KiB), or `<set id>/<member>` |
| `lkey` | S | the logical key the item was written for (the set, for a member) |
| `v` | B | the value (State) |
| `rev` | N | the revision: a random 64-bit number drawn on every write |
| `expires` | N | epoch seconds the item is dead from; absent when permanent. The table's TTL attribute |
| `k` | S | `i` for an Index member; absent for a State record, so no State listing returns a member |

| pk (kind) | sk (id) | What it is |
|---|---|---|
| `directory` | `<provider>/<workspace-id>` | an identity directory: `google/C01ipl6j0`; later `entra/<tenant-id>` |
| `github-org` | `<org>` | a connected GitHub organisation |
| `github-app` | `link` | the link App |
| `github-app` | `<app-id>` | a catalogue GitHub App (the id `link` is the link App's, and refused) |
| `github-link` | `<github-user-id>` | a person's GitHub link |
| `github-runner-app` | `<tier>/<org>` | a runner App |
| `github-claim` | `<github-user-id>` | the marker of a link claim |
| `github-gate` | `<org>/confirm`, `<org>/pass` | an operator's confirmation of a removal set, a request for a pass |
| `slack-workspace` | `<team>` | a connected Slack workspace |
| `slack-app` | `<app-id>` | a catalogue Slack App |
| `slack-shared` | `<name>` | a Slack Connect channel definition |
| `slack-channel` | `<team>/<channel>` | a console channel's record |
| `slack-gate` | `<team>/confirm[/<channel>]`, `<team>/pass` | the same for Slack |
| `slack-share` | `<host>/<channel>` | a Slack Connect share |
| `slack-user-cache` | `<team>/<user>` | who a Slack member is (24 h) |
| `console` | `session-key` | (the key itself is in Secrets) |
| `lease` | `<kind>/<target>` | a tick's lease: kinds `github-tick`, `github-links`, `slack-tick`, `refresh` |
| `notify` | `<target>` | a notification (a minute) |
| `gate`, `cache`, `dedupe` | the rest of the key, `/`-separated | ledger entries, shared inputs, idempotency markers |
| `keyring` | `<alg>/<kid>` | a signing key's schedule (`ES384/<kid>`) |
| `keyring-retired` | `<alg>/<kid>` | the tombstone of a retired key |
| `keyring-index` | `<alg>/<kid>` | the members of the key ring (Index) |
| `issuer-request` | `<uuid>` | a pending authorization request |
| `issuer-code`, `issuer-code-session` | `<id>` | an authorization code, the session it opened |
| `issuer-token` | `<uuid>` | a minted token's record |
| `issuer-session`, `issuer-sso`, `issuer-sso-of` | `<id>`, `<id>`, `<identity>` | sessions and the browser's SSO session |
| `issuer-sso-cookie` | `<hash>` | pointer from the browser's SSO cookie, hashed, to the sign-in id; the record's lifetime. The `issuer-sso` record carries the same hash as `cookie_hash` |
| `issuer-session-token` | `<hash>` | a live refresh token; once spent, `spent:<unix ms>:<sealed successor>:<session id>` |
| `issuer-session-rotated` | `<hash>` | legacy: a spent token's successor as an older version wrote it; read for one release, never written, then removed |
| `issuer-held` | `<identity>` | an identity's last-known directory groups, kept for the hold window (`lifetimes.hold`) |
| `issuer-guard` | `state-secret-fingerprint` | the guard that a state secret has not changed |
| `session`, `session-pointer` | `<person>/<sid>`, `<sid>` | a session of the layout's own form |
| `sessions-of`, `sessions-for`, `sso-clients` (Index) | `<identity or client or sso id>/<member>` | the transitional session index |
| `sessions-index`, `sso-index` (Index) | `all/<member>` | every session, every sign-in |
| `other` | the whole logical key | a key the layout names no kind for (a test's) |

| Session rule | Behavior |
|---|---|
| Rotation mark | `issuer-session-token` holds `spent:<unix ms>:<sealed successor>:<session id>`. The successor is sealed with AES-256-GCM under a key derived from the spent token. A retry within the 30-second grace finds the successor; a later presentation ends the session the mark names. A mark that does not open with the presented token counts as an unknown token |
| Mark lifetime | `max(auth_time + absolute limit - now, 30 s)` (24 hours by default). Without `auth_time` or an absolute limit: until the session ends, at most the refresh lifetime (12 hours) |
| Mark write | Only over the revision read, so a concurrent refresh is a replay and a revocation is not undone |
| Index membership | `issuer-session` carries `IndexedUntil` and `Involved`. A member is added with twice the refresh lifetime and re-added only when it would lapse: one `Add` per 12 hours by default |
| `issuer-held` | Rewritten only when the groups change, when the record is not this process's own write (`RevisionPeeker`), or when older than hold window / 8. A hold can end up to an eighth early, never late |
| `issuer-session-rotated` | Legacy: read, never written |

| Listing | Query |
|---|---|
| Prefix in one kind (`rec.slack.channel.acme.`) | `Query` on `pk` with `begins_with(sk, "acme/")`, in key order |
| Prefix naming several kinds (`ws.`, `gate.`, empty) | `Scan` filtered on `lkey`, sorted in memory; for operators and `sluis migrate` |
| Index member | `sk` = `<set id>/<member>`; `/` and `~` in a set id are `~2F` and `~7E` |

The logical key maps to kind and credential path as follows.

| Logical key (unchanged) | v2 `pk` / `sk` | credential path under `<root>/internal/` |
|---|---|---|
| `ws.dir.<provider>.<id>` (was `ws.dir.<id>`) | `directory` / `<provider>/<id>` | `credentials/directory/<provider>/<id>/<ref>` |
| `gh.org.<org>` | `github-org` / `<org>` | `credentials/github-org/<org>/<ref>` |
| `app.gh.link` | `github-app` / `link` | `credentials/github-app/link/<ref>` |
| `app.gh.cat.<id>` | `github-app` / `<id>` | `credentials/github-app/<id>/<ref>` |
| `app.gh.runner.<tier>.<org>` | `github-runner-app` / `<tier>/<org>` | `credentials/github-runner-app/<tier>/<org>/<ref>` |
| `gh.link.<account>` | `github-link` / `<account>` | `credentials/github-link/<account>/<ref>` |
| `gate.github-claim.<account>` | `github-claim` / `<account>` | none |
| `gate.github.<org>.confirm`, `.pass` | `github-gate` / `<org>/confirm`, `<org>/pass` | none |
| `ws.slack.<team>` | `slack-workspace` / `<team>` | `credentials/slack-workspace/<team>/<ref>` |
| `app.slack.cat.<id>` | `slack-app` / `<id>` | `credentials/slack-app/<id>/<ref>` |
| `rec.slack.shared.<name>` | `slack-shared` / `<name>` | none |
| `rec.slack.channel.<team>.<name>` | `slack-channel` / `<team>/<name>` | none |
| `gate.slack.<team>.confirm[.<channel>]`, `.pass` | `slack-gate` / `<team>/confirm[/<channel>]`, `<team>/pass` | none |
| `share.<host>.<channel>` | `slack-share` / `<host>/<channel>` | none |
| `cache.slack.user.<team>.<id>` | `slack-user-cache` / `<team>/<id>` | none |
| `rec.console.session-key` | `console` / `session-key` | `credentials/console/session-key` |
| `lease.<kind>:<target>` | `lease` / `<kind>/<target>` | none |
| `notify.<target>` | `notify` / `<target>` | none |
| `issuer:request:<id>` | `issuer-request` / `<id>` | none |
| `issuer:code:<id>`, `issuer:code-session:<id>` | `issuer-code`, `issuer-code-session` / `<id>` | none |
| `issuer:token:<jti>` | `issuer-token` / `<jti>` | none |
| `issuer:sso:<id>`, `issuer:sso-of:<identity>` | `issuer-sso`, `issuer-sso-of` | none |
| `issuer:sso-cookie:<hash>` | `issuer-sso-cookie` / `<hash>` | none |
| `issuer:session:<id>` | `issuer-session` / `<id>` | none |
| `issuer:session-token:<hash>` | `issuer-session-token` / `<hash>` | none |
| `issuer:session-rotated:<hash>` (legacy, read only) | `issuer-session-rotated` / `<hash>` | none |
| `issuer:keyring:entry:<alg>:<kid>` | `keyring` / `<alg>/<kid>` | none (a `kms-wrapped` entry also carries `wrapped`, the private key encrypted under the symmetric KMS key; never plaintext) |
| `issuer:keyring:retired:<alg>:<kid>` | `keyring-retired` / `<alg>/<kid>` | none |
| `issuer:keyring:index:<alg>` (Index) | `keyring-index` / `<alg>/<kid>` | none |
| `issuer:held:<identity>` | `issuer-held` / `<identity>` | none |
| `issuer:kms:state-secret-fingerprint` | `issuer-guard` / `state-secret-fingerprint` | none |
| `ses.<person>.<sid>`, `sid.<sid>`, `req.`, `code.`, `tok.`, `sso.`, `rt.`, `rtrot.` (legacy), `keyring.` | the layout's dotted forms of the above | none |

## S3 (the `s3` Blob adapter)

| Prefix | Content |
|---|---|
| `reports/github/<target>`, `reports/slack/<target>` | What each controller last reported |
| `snapshots/<directory>` | The hub's cache, never migrated |

## Exports (retired)

The `<root>/export/<path>` copies of layout v3 are retired ([ADR 0041](../../decisions/0041-the-secret-contract.md)). A consumer reads `<root>/external/<kind>/<id>`: [secrets](secrets.md#the-external-documents) has the kinds and schemas, [exports](exports.md) the old sources.
