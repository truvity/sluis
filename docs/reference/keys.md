# Keys: the logical key layout

The records the service keeps in State, by logical key. One logical layout, two renderings: the in-memory and the
legacy adapters use the key as written; the adapters of the AWS platform use storage layout v3, a record *kind* and an
*id*, derived in one place (`internal/port/keys.go`): [storage layout](storage-layout.md). The contract of State itself
(operations, lifetimes, revisions) is in [ports](ports.md#state).

No access pattern needs a secondary index: everything a caller looks up is a key
or a prefix. Sessions are listed per person under `ses.<person>.`, a session id
is resolved to its person through the pointer `sid.<sid>`, and "every session"
is a listing over all `ses.` partitions, which is an operator action and not a
hot path.

| Key | Content | Writer | TTL |
|---|---|---|---|
| `req.<id>` | a pending authorization request, also backing a device-code poll | issuer | 30 min |
| `code.<id>` | an authorization code | issuer | 5 min |
| `codesess.<id>` | the session a redeemed code opened, for a replayed redemption | issuer | 5 min |
| `ses.<person>.<sid>` | a per-client session: identity, client, how it began, scopes, SSO session, refresh token (hashed), authentication time | issuer | the session lifetime |
| `sid.<sid>` | pointer from a session id to `<person>`; written with the session, deleted with it | issuer | the session lifetime |
| `rt.<hash>` | live refresh token to `<person>.<sid>` | issuer | the session lifetime |
| `rtrot.<hash>` | legacy: a spent refresh token's successor, as an older version wrote it. The retry grace is now carried by `rt.<hash>` itself (`spent:<successor>`, 30 s). Still read for one release, never written, then removed | issuer | 30 s |
| `sso.<id>` | the browser-wide SSO session and the clients it covers | issuer | the session lifetime |
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
| `lease.<target>` | the holder of a target's tick, by id | ticks | seconds, renewed |
| `gate.<target>.<name>` | a held-once ledger entry, a breaker, a fingerprint. Written today: `gate.github.<org>.confirm` and `.pass`, `gate.slack.<workspace>.confirm[.<channel>]` and `.pass` (an operator's confirmation of a removal set, 24 h; a request for a pass now, 24 h), `gate.github-claim.<account>` (the marker of a link claim, below) | ticks, console | by gate |
| `share.<host>.<channel>` | a Slack Connect share: the guests that were invited and each side's state; written by the host's tick, its write enqueues the guest's tick, and the guest's tick marks its own side accepted | host tick, guest tick | 14 days while a guest is pending, then 7 days once every guest has accepted |
| `cache.slack.user.<workspace>.<id>` | who a Slack member is (address, team, bot, guest): `users.info` once a day, not once a pass. A deactivated account is never cached | slack tick | 24 h |
| `cache.<digest>.<name>` | a shared input (a group's holders, an address's state), keyed by the policy digest. **Not written yet**: the Slack controller's shared inputs stay in memory ([why](../explanation/domain-stores.md)) | ticks | the digest's lifetime |
| `dedupe.<id>` | an idempotency marker for an external write | ticks | by use |

The records marked permanent are the only ones with no TTL (`ws.`, `gh.org.`,
`gh.link.`, `app.` and `rec.`: the layout was first drawn with a lifetime on a
link, "until the refresh expires", which is wrong for the links that hold no
tokens at all, a profile match or an import, and for a lost link, whose record is
what removes a person: a link that expired would read as unlinked). A key that no
longer appears in this table is not written by the service. Names that go into a
key (an id, a login, a channel) are written one segment each, every byte but a
letter, a digit, `-` and `_` as `~XX`, so a dot in a name cannot end its segment.

A credential is never written to State. The domain stores put it in
[Secrets](ports.md#secrets) under `credentials/<kind>/<id>` and leave a marker in the record; the
State store never sees a credential. (There was once a sealing step, an
envelope under a KMS key. It is retired: ADR 0027's sealing is superseded.)
