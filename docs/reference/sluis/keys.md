# Keys: the logical key layout

The records the service keeps in State, by logical key. The in-memory and legacy adapters use the key as written; the AWS adapters use the [storage layout](storage-layout.md), derived in `internal/port/keys.go`. State's contract: [ports](ports.md#state).

No access pattern needs a secondary index. Sessions are listed per person under `ses.<person>.`, and `sid.<sid>` resolves a session id to its person. Listing every session scans all `ses.` partitions: an operator action.

| Key | Content | Writer | TTL |
|---|---|---|---|
| `req.<id>` | a pending authorization request, also backing a device-code poll | issuer | 30 min |
| `code.<id>` | an authorization code | issuer | 5 min |
| `codesess.<id>` | the session a redeemed code opened, for a replayed redemption | issuer | 5 min |
| `ses.<person>.<sid>` | a per-client session: identity, client, how it began, how the person was proved (`method`: a provider's kind or `recovery`), scopes, SSO session, refresh token (hashed), authentication time | issuer | the session lifetime |
| `sid.<sid>` | pointer from a session id to `<person>`; written with the session, deleted with it | issuer | the session lifetime |
| `rt.<hash>` | live refresh token to `<person>.<sid>`. Once spent, the same key holds `spent:<unix ms>:<sealed successor>:<session id>`: when it was spent, the successor sealed with AES-256-GCM under a key derived from the spent token (SHA-256 of a label, a zero byte and the token), and the session it belonged to. Inside the 30-second grace a replay is answered with the successor; after it, a presentation ends that session ([sessions](../../concepts/sluis/sessions.md#refresh-token-reuse)) | issuer | the session lifetime; once spent, the family's absolute deadline, `max(auth_time + absolute limit - now, 30 s)` (24 h by default), or the session's end when there is no `auth_time` or no absolute limit (at most 12 h) |
| `rtrot.<hash>` | legacy: a spent refresh token's successor, as an older version wrote it. The spent mark is now carried by `rt.<hash>` itself (see the `rt.<hash>` row). Still read for one release, never written, then removed | issuer | 30 s |
| `sso.<id>` | the browser-wide SSO session and the clients it covers; the record carries `cookie_hash` | issuer | the session lifetime |
| `issuer:sso-cookie:<hash>` | the browser's SSO cookie, hashed, pointing to the sign-in id; the cookie itself is a random secret and is never stored. It has no dot-spelling form because it was introduced after that layout | issuer | the sign-in's |
| `tok.<jti>` | a minted token's own record, for userinfo and revocation | issuer | until the token expires |
| `keyring.<kid>` | a signing key's schedule: first seen, activation | issuer replicas | 30 days, renewed on each poll |
| `ws.dir.<id>` | a connected directory workspace: its record, **with its credential in Secrets** (`credentials/workspace/<id>/<ref>`) | console | permanent |
| `ws.slack.<workspace>` | a connected Slack workspace: its record, with its client secret and bot token in Secrets | console | permanent |
| `gh.org.<org>` | a connected GitHub organisation: its record, with its App key in Secrets | console | permanent |
| `gh.link.<account>` | a GitHub account's link, keyed by the **account id**; the token pair is in Secrets, the rest of the link, `RefreshingSince` and the `Revision` counter included, is plain | link flow, GitHub tick | permanent |
| `app.gh.link` | the link App: record, with the client secret in Secrets | console | permanent |
| `app.gh.runner.<tier>.<org>` | a runner App: record, with the key in Secrets | console | permanent |
| `app.gh.cat.<id>` | a catalogue GitHub App: record, with the key in Secrets | console | permanent |
| `app.slack.cat.<id>` | a catalogue Slack App: record, with the client secret and bot token in Secrets | console | permanent |
| `rec.slack.shared.<name>` | a Slack Connect channel's definition | console | permanent |
| `rec.slack.channel.<workspace>.<name>` | a console channel's record | console | permanent |
| `rec.console.session-key` | the key the console signs its sessions with, in Secrets (`credentials/console/session-key`); created by the first replica that starts | console | permanent |
| `rec.maintenance` | the maintenance flag: present while the module's table is being restored; the module refuses writes until it is gone | restore function | permanent |
| `rec.backup.run.<id>` | one backup run: its state, counts and, while unfinished, the checkpoint a resume needs; the backup module's table only, never in a backup | backup | permanent |
| `rec.backup.restore.<id>` | one restore: its state, the backup it restores from, the counts of the last slice and whether the maintenance flag is still set; the backup module's table only, never in a backup | restore | permanent |
| `rec.backup.retention` | the last retention pass: what it kept and removed | backup | permanent |
| `lease.<target>` | the holder of a target's tick, by id | ticks | seconds, renewed |
| `gate.<target>.<name>` | a held-once ledger entry, a breaker, a fingerprint. Written today: `gate.github.<org>.confirm` and `.pass`, `gate.slack.<workspace>.confirm[.<channel>]` and `.pass` (an operator's confirmation of a removal set, 24 h; a request for a pass now, 24 h), `gate.github-claim.<account>` (the marker of a link claim, below) | ticks, console | by gate |
| `share.<host>.<channel>` | a Slack Connect share: the guests that were invited and each side's state; written by the host's tick, its write enqueues the guest's tick, and the guest's tick marks its own side accepted | host tick, guest tick | 14 days while a guest is pending, then 7 days once every guest has accepted |
| `cache.slack.user.<workspace>.<id>` | who a Slack member is (address, team, bot, guest): `users.info` once a day, not once a pass. A deactivated account is never cached | slack tick | 24 h |
| `cache.<digest>.<name>` | a shared input (a group's holders, an address's state), keyed by the policy digest. **Not written yet**: the Slack controller's shared inputs stay in memory ([why](../../concepts/sluis/domain-stores.md)) | ticks | the digest's lifetime |
| `dedupe.<id>` | an idempotency marker for an external write | ticks | by use |

Only the records marked permanent (`ws.`, `gh.org.`, `gh.link.`, `app.`, `rec.`) have no TTL. A key absent from the table is not written. Name segments (id, login, channel) encode every byte but letters, digits, `-` and `_` as `~XX`, so a dot cannot end a segment.

A credential is never written to State. Domain stores put it in [Secrets](ports.md#secrets) under `credentials/<kind>/<id>` and leave a marker in the record.
