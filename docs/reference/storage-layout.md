# Storage layout

Where sluis keeps what it keeps, on the adapters of the AWS platform (`dynamodb`,
`ssm`, `s3`). This is **layout v3**: one root per installation, `/sluis/<instance>`
([0036](../decisions/0036-configuration-is-immutable-per-instance.md)), so two
installations share an account. The storage layout of the records themselves (kind
and id) is v2's, unchanged; v3 moves the SSM paths. The **legacy** adapter (ConfigMaps, Secrets,
Valkey) is unchanged and keeps its own names until a deployment leaves it.

The rule is one sentence: **a record has a kind (a readable noun) and an id**.
DynamoDB stores the kind as `pk` and the id as `sk`; the credentials of a record
are the Secrets path `credentials/<kind>/<id>/<ref>`; an id that has two parts is
written `a/b`. The mapping lives in one place, `internal/port/keys.go`, which the
DynamoDB adapter and the domain stores share; the service's own *logical* keys
(`ws.dir.<id>`, `issuer:token:<jti>`) did not change, so the legacy adapter and
`sluis migrate` read the source as before.

## SSM (the `ssm` Secrets adapter)

The root is the installation's, `<root>` = `/sluis/<instance>` (the serve document's
`secrets.root`; hive: `/sluis/hive`, Truvity's: `/sluis/kernel`). An instance may not
be named `private` or `export`. The IAM boundary is the first level:
`<root>/private/*` is sluis's alone, `<root>/export/*` is what consumers' External
Secrets Operator reads. A port path `p` is `<root>/private/<p>`, except
`export/<name>`, which is `<root>/export/<name>`. Inside `private` there are exactly
two kinds of parameter: `config/`, the secrets a document **names** (an operator
seeds them, sluis reads them), and `credentials/`, which sluis writes.

| SSM path | What it is | Who writes it |
|---|---|---|
| `<root>/private/config/providers/google/<provider>/client-id`, `client-secret` | the Google OAuth client of the directory (`oauthClient.provider`) | an operator seeds it; sluis reads it |
| `<root>/private/config/clients/<oidc-client-id>/secret` | the secret of a confidential client of the issuer | an operator seeds it |
| `<root>/private/config/issuer/state-secret` | the issuer's sign-in state secret (32 random bytes, base64) | `deploy/pulumi` generates it (`StateSecretParameterName(instance)`) |
| `<root>/private/config/recovery/password` | the recovery password | `deploy/pulumi` generates it |
| `<root>/private/config/directory/<id>/key` | a declared workspace's service-account key | an operator seeds it |
| `<root>/private/config/valkey/password` | the shared store's password | an operator seeds it |
| `<root>/private/credentials/console/session-key` | the key the console signs its sessions with | sluis (one stable item) |
| `<root>/private/credentials/directory/<provider>/<workspace-id>/<ref>` | an identity directory's credential (`directory/google/<workspace-id>`; later `directory/entra/<tenant-id>`) | sluis |
| `<root>/private/credentials/github-org/<org>/<ref>` | an organisation's App key | sluis |
| `<root>/private/credentials/github-app/link/<ref>` | the link App's client secret | sluis |
| `<root>/private/credentials/github-app/<app-id>/<ref>` | a catalogue GitHub App's key | sluis |
| `<root>/private/credentials/github-link/<github-user-id>/<ref>` | a person's GitHub token pair | sluis |
| `<root>/private/credentials/github-runner-app/<tier>/<org>/<ref>` | a runner App's key | sluis |
| `<root>/private/credentials/slack-workspace/<team>/<ref>` | a Slack workspace's client secret and bot token | sluis |
| `<root>/private/credentials/slack-app/<app-id>/<ref>` | a catalogue Slack App's client secret and bot token | sluis |
| `<root>/export/<export-name>` | a copy for a consumer: see [Exports](#exports) | sluis |

The names under `config/` are the ones the documents give
([configuration](configuration.md#secrets)); the http function reads them by path
through the serve document's `secrets` source.

**Identity directories.** `directory/<provider>/` is the rule for every identity
directory: the provider is the record's `backend` (`google` today), a segment below
the family, so another directory is a new provider and not a new kind. A credential
saved before its record, which names no backend, is under `google`, and the record
then keeps the key it finds.

`<ref>` is a fresh random name per write, kept by the record that names it: a
credential is never replaced in place, so a writer that loses a compare-and-swap
cannot overwrite the winner's secret (a spent single-use refresh token never
overwrites the new pair). The console's session key is the one stable item with no
ref. An id segment that a secret path cannot hold (a `~`, an empty one, one that
begins `u-`) is written `u-` and its bytes in hex.

**What a deployment must change from the old layouts**

| Was (v2, one root `/sluis`) | Is (v3, `/sluis/<instance>`) |
|---|---|
| `/sluis/private/config/oauth/client-id`, `client-secret` | `<root>/private/config/providers/google/default/client-id`, `client-secret` |
| `/sluis/private/config/clients/<id>` | `<root>/private/config/clients/<id>/secret` |
| `/sluis/private/config/issuer/state-secret`, `recovery/password` | the same names under `<root>/private/config/` |
| `/sluis/private/credentials/...` | `<root>/private/credentials/...`, copied by `sluis migrate` |
| `/sluis/export/<path>` | `<root>/export/<path>`, written again by the next exports pass |

`sluis migrate ssm-layout --to-root /sluis/<instance>` copies the first three rows
and deletes nothing; `sluis migrate` moves the credentials
([configuration](configuration.md#ssm-layout-v3)). The v1.60 layout's own move
(`/sluis/private/oauth/...` to `config/`) is older still: the `<NAME>=ssm:` environment mappings and
`SLUIS_SECRET_FILES` entries it named are gone, replaced by the documents' secret names.
IAM follows the root: every grant is under `/sluis/<instance>/`.

## DynamoDB (the `dynamodb` State, Index and Trigger adapter)

One table, `pk` (string, hash) and `sk` (string, range). Every State item has the
record **kind** as `pk` and the record's **id** as `sk` (slash-separated when
compound); `lkey` holds the logical key the item was written for, `v` the value,
`rev` the revision, `expires` the TTL, and `k` = `i` marks an Index member.
Because `pk` is the kind, `dynamodb:LeadingKeys` grants a role the kinds it writes.

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
| `lease` | `<kind>/<target>` | a tick's lease: kinds `github-tick`, `github-links`, `slack-tick`, `refresh`, `export` |
| `notify` | `<target>` | a notification (a minute) |
| `gate`, `cache`, `dedupe` | the rest of the key, `/`-separated | ledger entries, shared inputs, idempotency markers |
| `keyring` | `<alg>/<kid>` | a signing key's schedule (`ES384/<kid>`) |
| `keyring-retired` | `<alg>/<kid>` | the tombstone of a retired key |
| `keyring-index` | `<alg>/<kid>` | the members of the key ring (Index) |
| `issuer-request` | `<uuid>` | a pending authorization request |
| `issuer-code`, `issuer-code-session` | `<id>` | an authorization code, the session it opened |
| `issuer-token` | `<uuid>` | a minted token's record |
| `issuer-session`, `issuer-sso`, `issuer-sso-of` | `<id>`, `<id>`, `<identity>` | sessions and the browser's SSO session |
| `issuer-session-token`, `issuer-session-rotated` | `<hash>` | a live refresh token, a spent one's successor |
| `issuer-held` | `<identity>` | an identity's last-known directory groups, kept for the hold window (`lifetimes.hold`) |
| `issuer-guard` | `state-secret-fingerprint` | the guard that a state secret has not changed |
| `session`, `session-pointer` | `<person>/<sid>`, `<sid>` | a session of the layout's own form |
| `sessions-of`, `sessions-for`, `sso-clients` (Index) | `<identity or client or sso id>/<member>` | the transitional session index |
| `sessions-index`, `sso-index` (Index) | `all/<member>` | every session, every sign-in |
| `other` | the whole logical key | a key the layout names no kind for (a test's) |

**Listing by a prefix.** A prefix that lies in one kind (`rec.slack.channel.acme.`)
is a `Query` on its `pk` with `begins_with(sk, "acme/")`, in key order. A prefix
that names several kinds (`ws.`, `gate.`, the empty one) is a `Scan` filtered on
`lkey`, sorted in memory: an operator's listing, and `sluis migrate`.
An Index member is `sk` = `<set id>/<member>` (a `/` or `~` in a set id is `~2F`
or `~7E`).

**Old to new.** What a record was called before (the logical key, which is also
what the legacy adapter keeps) and what it is now, for every kind:

| Logical key (unchanged) | v2 `pk` / `sk` | credential path under `<root>/private/` |
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
| `issuer:session:<id>` | `issuer-session` / `<id>` | none |
| `issuer:session-token:<hash>`, `issuer:session-rotated:<hash>` | `issuer-session-token`, `issuer-session-rotated` | none |
| `issuer:keyring:entry:<alg>:<kid>` | `keyring` / `<alg>/<kid>` | none (a `kms-wrapped` entry also carries `wrapped`, the private key encrypted under the symmetric KMS key; never plaintext) |
| `issuer:keyring:retired:<alg>:<kid>` | `keyring-retired` / `<alg>/<kid>` | none |
| `issuer:keyring:index:<alg>` (Index) | `keyring-index` / `<alg>/<kid>` | none |
| `issuer:held:<identity>` | `issuer-held` / `<identity>` | none |
| `issuer:kms:state-secret-fingerprint` | `issuer-guard` / `state-secret-fingerprint` | none |
| `ses.<person>.<sid>`, `sid.<sid>`, `req.`, `code.`, `tok.`, `sso.`, `rt.`, `rtrot.`, `keyring.` | the layout's dotted forms of the above | none |

## S3 (the `s3` Blob adapter)

Kept as they were: `reports/github/<target>`, `reports/slack/<target>` (what each
controller last reported) and `snapshots/<directory>` (the hub's cache, never
migrated). The names already read as `<what>/<whose>`, so nothing is gained by
moving them, and a report would otherwise be rewritten for no reason.

## Exports

An export is a copy of a secret for a program that cannot ask sluis. With a Secrets
adapter configured and `ports.export` unset, an export writes through the **Secrets
port** at `export/<path>`, which the `ssm` adapter keeps at
`<root>/export/<path>`. (`ports.export: openbao` keeps working for the Kubernetes
path, and writes to OpenBao as before.) The `<path>` is the export's `path` in
the `exports` list: **it is the name a consumer reads, and a consumer contract.**
Keep it stable.

**Value format.** One JSON object per export, text values only, so that ESO's SSM
provider extracts a property with `remoteRef: {key: /sluis/<instance>/export/<path>, property: <name>}`:

```json
{"bot_token":"xoxb-..."}
```

A replace writes exactly the properties; a patch (every App source) sets its
properties and keeps the others in the object. The property names are fixed by the
source (an export's `properties` map may rename them):

| Source | Properties |
|---|---|
| `slack-app` | `bot_token` |
| `github-app` | `app_id`, `installation_id`, `private_key` |
| `runner-app` | `github-app-id`, `github-installation-id`, `github-private-key` |
| `bundle` | one property per entry of the bundle (disaster-recovery copies; an SSM parameter holds at most 8 KiB, so a large bundle is refused: send bundles to OpenBao) |

An identical export writes nothing, so SSM makes no new version. The `exports`
event of the one Lambda function (`{"kind":"exports"}`) writes them on a
schedule.
