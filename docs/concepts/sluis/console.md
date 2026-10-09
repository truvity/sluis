# What does the console do?

The console is the web UI of the sluis process. It reads everything and changes only bootstrap, removals and confirmations.

## One origin

The issuer sits at the root of the domain and the console takes the path `/console/`. The issuer URL is the `iss` claim in every token, and discovery lives at `/.well-known/openid-configuration` at an origin root. A path on the issuer would move discovery somewhere Kubernetes and AWS handle badly.

The console is therefore same-origin: its session pages call the issuer with the browser's own cookie. The process strips the prefix, but every link handed to a browser must carry it. The mount is read from the address the console is published at.

The admin-consent callback stays at the origin root. It runs before anybody can sign in, and its redirect URI is registered with every corporate tenant.

## Sign-in as a client

A person with no session is sent to `/authorize` with the console's declared client. They sign in at the issuer's login page and return with the issuer's session cookie. The console has no login of its own and no proxy in front of it.

The code the flow returns is never redeemed. The console needs the session, not a token, because it reads the directory and the policy in the same process. The code expires unused and is stripped from the URL. See [the console app](../../guides/sluis/connect/console-app.md).

## What it shows

It shows every person, directory group, internal group, rule, open session, GitHub organisation and Slack workspace, with what each controller would change. A person's page states the chain for each internal group held, one hop at a time:

```text
stage:k8s:viewer ← implied by stage:k8s:operator ← wildcard *:k8s:admin ← directory group sre@example.com
```

See [mapping wildcards](../../reference/sluis/taxonomy.md#mapping-wildcards), the [vocabulary](../../reference/sluis/policy.md#vocabulary) and [inheritance](../../reference/sluis/taxonomy.md#inheritance).

The pages group into four clusters. Identity: directories, directory groups, people, rules. Access: internal groups, clients, sessions. Systems: GitHub, Slack and Cloudflare. Admin: audit and settings. A disabled GitHub organisation or Slack workspace is still derived every pass, so its page is the dry run to read before enabling it.

## What it changes

It cannot change who is in an internal group. That is the policy, reviewed in git. It does these things, each audited:

- Connect a directory by admin consent, a GitHub organisation by its owner installing an App, and a Slack workspace by a pasted throwaway configuration token. Each needs a browser, because no code path obtains that credential.
- Keep console channel records (`_channel.<workspace>.<name>.json`) and Slack Connect records (`_shared.<name>.json`). They take directory groups and addresses, never internal groups, and grant no infrastructure access.
- Request a pass with Refresh, at most once a minute per Slack workspace or GitHub organisation.
- Revoke a session, or disconnect something it connected. Disconnecting revokes at the other side, then forgets.
- Confirm a set of removals a controller held, and import GitHub links approved elsewhere.
- Archive a console channel in Slack when its record is forgotten, if asked. Slack Connect channels are never archived.

An operator may do these over the installation, or over the one directory that owns the organisation or workspace. Everyone else is a viewer. See [console roles](../../reference/sluis/console-roles.md).

Cloudflare tokens are minted on demand from the policy's `cloudflare.grants`, shown once and held nowhere. See [mint short-lived Cloudflare tokens](../../guides/sluis/cloudflare-tokens.md).

## The Sessions page

A person's page lists sessions grouped under the sign-in that opened them, one group per browser. A signed-out browser stays listed while it holds the agent connections the sign-out kept. Each row shows the client, its class (`interactive` or `agent`), its deadline and its expiry. *End this browser* ends the sign-in and every session under it. A row's own revoke ends only that session.

Your own page has three buttons: *Sign out all browsers and apps*, *Disconnect all agents* and *Sign out everything*. Somebody else's page offers only *Sign out everything*. See [sessions](sessions.md#sign-out-keeps-agent-connections-and-says-so).

A client's page lists its sessions across people. An operator can press *End for everybody*, which sets `every_identity` on `RevokeSessions` and audits `roster.session.revoked` with scope `client_every_identity`.

## The issuer's own pages

Four pages run before anyone can be authorized, so none can be a console page.

| Path | Purpose |
|---|---|
| `/login` | The sign-in chooser, built from the declared policy and the validated request. |
| `/logout` | The sign-out a person follows, with no `id_token_hint` needed. |
| `/signed-out` | The landing page when a client declares none. |
| A refusal | What `/authorize` and `/end_session` show when no safe redirect exists. |

Re-read these pages when behaviour changes, because nothing fails when they go stale.

## Decided in

- [ADR 0001: sessions and an absolute limit](../../decisions/0001-sessions-and-an-absolute-limit.md)
- [ADR 0040: agent-class sessions](../../decisions/0040-agent-class-sessions.md)
- [ADR 0021: Slack Connect channels are console records](../../decisions/0021-slack-connect-channels-are-console-records.md)
- [ADR 0022: the console archives only ordinary channels, only when asked](../../decisions/0022-the-console-archives-only-ordinary-channels-only-when-asked.md)
