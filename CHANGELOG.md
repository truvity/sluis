## Unreleased

### Added

- **Agent-class sessions: `session: agent` on a client, and on `client_documents`, for software that holds its own refresh token and works in the background, such as MCP hosts** ([ADR 0040](docs/decisions/0040-agent-class-sessions.md)). Such a client's refresh chains are held to the service configuration's new `lifetimes.agent` instead of the installation's `lifetimes.refresh` and `lifetimes.absolute`: an idle limit of `336h` (14 days), an absolute limit of `720h` (30 days) from `auth_time`, and a mandatory `30m` cap on every access and ID token. A resource's `absolute_cap` is only ever a ceiling for an agent chain, and the client's, `client_documents`' and the resource's `ttl_cap` still shorten its tokens. The class is decided once, when the authorization completes, from the installation's policy and never from a client document, and is recorded on the session with the chain's `deadline`. A later policy or configuration change can shorten an agent chain at its next refresh but never lengthen it past that deadline or change its class; a chain recorded before this release, or by an older replica during a rollout, is interactive. Refused at load: `session` on an `exchange` client, `session: agent` with `sign_in_exchange: true`, `lifetimes.agent.absolute` above `2160h` (90 days) or not positive, `lifetimes.agent.refresh` above `lifetimes.agent.absolute` or not positive, and `lifetimes.agent.access` above `1h` or not positive. Warned at start: `session: agent` on a client with `signed_out` or `backchannel_logout_uri`. **This release does not yet put the consent page in front of an agent authorization, nor spare agent sessions at a browser sign-out** (decisions 6 and 7 of the ADR, in a later release): until then an agent authorization can complete silently, so mark a client `session: agent` only once that release is deployed. A rollback past this release needs `session` and `lifetimes.agent` removed first, since an older binary refuses both keys.

### Changed

- **`client_documents.ttl_cap` now caps a document client's access tokens, not only its ID tokens.** The token endpoint looked a client's `ttl_cap` up among declared clients only, so a document client's access token lived `lifetimes.token`.
- **A token exchange of a sign-in (`sign_in_exchange`) refuses an access token whose session is agent-class**, whatever the client's row says now: a recorded class outlives a policy change.
- **The session index sets are kept for `max(2 × lifetimes.refresh, lifetimes.agent.absolute)` (30 days by default) on every Add**, so that an interactive session's Add never shortens a set an agent session is in on an engine whose set expiry is the whole set's (memory, Valkey). An interactive session's id stays in the sets longer, and a listing that meets one whose record is gone drops it, as it always has. A spent refresh token's mark in an agent chain is kept until the chain's recorded deadline.

### Deprecated

- **A `read_only` resource's `absolute_cap` above `lifetimes.absolute`** ([ADR 0033](docs/decisions/0033-a-longer-absolute-limit-for-read-only-resources.md)'s lengthening) is deprecated in favour of `session: agent`. It is still honoured in this release, with a warning at start naming the resources and `session: agent`; a later minor release refuses it, marked **Breaking:**. Migrate by marking the clients that need a longer chain `session: agent`, then removing `absolute_cap` from those resources or lowering it to at most `lifetimes.absolute`. `absolute_cap` as a shortening cap stays.

## v1.68.0

### Added

- **The issuer serves OAuth 2.0 Authorization Server Metadata (RFC 8414) at `/.well-known/oauth-authorization-server`**, so an MCP client that probes it no longer gets a 404. It is OpenID discovery narrowed, not a second document: the same corrected values (`response_types_supported`, `grant_types_supported`, and the rest), the same content type, caching and CORS behaviour, and the same refusals for other methods and paths, kept to `issuer`, `authorization_endpoint`, `token_endpoint`, `jwks_uri`, `scopes_supported`, `response_types_supported`, `grant_types_supported`, `token_endpoint_auth_methods_supported`, `revocation_endpoint` and its auth methods, `introspection_endpoint` and its auth methods, `code_challenge_methods_supported`, and the extension `client_id_metadata_document_supported` (only when the installation admits client ID metadata documents). OIDC-only members (`userinfo_endpoint`, `end_session_endpoint`, `acr_values_supported`, the ID token members) stay in discovery. `registration_endpoint` is listed only if discovery has one, which this issuer does not. An issuer URL with a path is served at the well-known segment followed by that path, as RFC 8414 section 3.1 inserts it. No gateway or Lambda route change is needed: both already forward every path.

### Changed

- **A Lambda invocation's kind and target, a request path, a panic and the errors logged beside them, and the Valkey retry warning's key and error, now go through `logsafe`.** The values come from the invocation payload, the request or the server, and could carry a line break into a log record; `logsafe.Value` and `logsafe.Error` remove it with the replacer the code scanner recognises (CodeQL `go/log-injection`). Log messages and keys are unchanged; a value that never held a line break is logged as before, and one that did now has it removed instead of replaced by a space.
- **Ending a sign-in now removes the set of clients it involved, and "sign out everywhere" removes the sign-in from the global index.** `State.Delete` removes values only, so `issuer:sso-clients:<id>` stayed in place until its lifetime ran out and `Involved` kept naming clients of a sign-in that had ended; it is now emptied member by member through the set operations, in memory and on every port adapter alike, with no change to the State or Index contract. Ending a sign-in with N involved clients makes N more operations than before (one read of the set and N removals instead of one delete). Ending all of a person's sign-ins no longer leaves each id in the global `issuer:sso` set until a listing finds it dangling: each sign-in costs N + 1 more operations (the clients set as above, and one more removal). Refresh and grant paths are unchanged.
- **"Sign out everywhere" now sends back-channel logout to every client the sign-ins involved, as an ordinary sign-out does.** The sessions the revoke ends are announced as held (with their session id), and clients signed in with `openid` alone, known only to the sign-in's set of involved clients, are announced by subject; one logout token per client and sign-in, best effort, through the same announcer. Previously nobody was told on this path. Cost: when an announcer is wired, one more read per sign-out everywhere (listing the identity's sessions before they are revoked); the set of involved clients is read once, for both the announcement and the drop. Every running chain is revoked in the same call, so none is spared.
- **A logout token sent at "sign out everywhere" names the identity as stored.** Clients signed in with `openid` alone were told the caller's spelling of the identity as `sub`, which differs in case from what their ID token carried when the request was not lowercase; it is now the sign-in record's identity, or the same normalisation the store applies. No operation is added.
- **Back-channel logout tokens are delivered concurrently, at most 8 at a time.** They went one after another with a 5 s timeout each, in the request path, so an unreachable client added 5 s per client and sign-in to a sign-out; it now adds 5 s once. All posts are still waited for before the call returns, each keeps its timeout and its log line, and a failure never fails the sign-out. Ordinary sign-out and "sign out everywhere" both benefit. No change in operations on the store.
- **Behaviour change: the console on the issuer's origin applies the issuer's sign-in checks on every request, and ends a sign-in that fails them.** The console used to accept any live browser sign-in (the issuer's SSO cookie) for the rest of the sign-in's own lifetime (up to 12 h by default), so a sign-in past the absolute session limit, or one whose person the directory had since suspended or removed, kept opening the console while every other client was already refusing it. It now asks the same function the issuer's silent sign-in asks, so the two cannot drift. A sign-in past the absolute limit (`auth_time` plus the installation's absolute limit, the one the console's own client is held to) is refused and ended with the same cascade as at `/authorize`: the clients signed in under it without a refresh token are sent a logout token, sessions still inside their own limit are left alone, nothing is audited, and the browser's cookie is cleared. A person the directory no longer admits (suspended, not found, or not vouched for with nothing held for them) is refused, the sign-in is ended and the cookie cleared, with the same warning in the log (`browser session is no longer admitted`) and no audit record, as at `/authorize`. A directory that cannot be reached is answered from the issuer's hold window (last-known standing, 4 h by default), exactly as for a silent sign-in: an admitted person keeps the console inside the window, and the sign-in ends past it or for someone nothing is held for. A recovery sign-in has no directory to ask and is held to the absolute limit only. Not being in a group that grants a console role still does not end a sign-in: the console refuses those calls by role, as before, and a silent sign-in to a client the person is not entitled to also keeps the sign-in. A console request now makes 4 State reads (was 3: the new one is the eventually consistent revision read of the last-known groups every refresh already pays), no writes, and one directory resolution and one snapshot read, as before: the issuer's check and the console's authorizer share one answer per request (`hub.OneAnswerPerRequest`), held to the grant-cost budget as `console request`. `issuerapp.Deps.UseSignedIn` and `server.ConsoleServerDeps.SignedIn` now take the `http.ResponseWriter` too, so an ended sign-in's cookie can be cleared.
- **Behaviour change: the issuer's session service holds a browser's sign-in cookie to the same check.** `ListSessions` and `RevokeSessions` (`accessissuer.v1.SessionService`, what the account page and the console call at the issuer's host) authenticated a cookie by asking only whether it named a live sign-in, so an operator's sign-in past the absolute limit, or a copy of its cookie, could go on listing every session and revoking anyone's until the record expired, and a person the directory had suspended or removed was still served as themselves, without their groups. The cookie is now judged by the decision the issuer's silent sign-in and the console make (one function): past `auth_time` plus the installation's absolute limit, or a person the directory does not admit (suspended, not found, or not vouched for with nothing held), the cookie proves nobody and the call is refused with `unauthenticated`; a bearer token, where one is sent, is still checked on its own. Inside the hold window an unreachable directory is answered from the last-known groups, as before. The service refuses such a cookie and does not end the sign-in; the issuer's pages and the console end it on the browser's next visit. A cookie call costs what it did: 7 State reads for a person listing their own sessions, no writes, one directory resolution (held to the grant-cost budget as `session service call`).
- **A recovery sign-in is recognised by how it was made, not by the shape of its subject.** The silent sign-in and the console skipped the directory for any sign-in whose subject had no `@`, and the console and the session service evaluated one whose subject read as a ServiceAccount (`system:serviceaccount:<ns>:<name>`, `k8s:<ns>:<name>`) as that ServiceAccount, which an installation that recovers through the console makes an operator. They now go by the method the sign-in recorded when it began (`recovery`); a sign-in made through an identity provider is a person whatever its subject looks like, is asked of the directory, and is never the recovery account. Recovery sign-ins themselves are unchanged.
- **Behaviour change: tokens and refreshes evaluate a subject as a ServiceAccount only for a recovery sign-in.** The code exchange, the ID token, the access token's claims, the client's `requires` at sign-in and at every refresh evaluated any subject that read as a ServiceAccount (`system:serviceaccount:<ns>:<name>`, `k8s:<ns>:<name>`, `<cluster>:k8s:<ns>:<name>`) by the policy's `service_account` matchers, without asking the directory, so a sign-in through an identity provider whose subject happened to read as one was given whatever the policy grants that ServiceAccount (for the recovery ServiceAccount, typically the operators' group) in every token and refresh. They now go by how the sign-in was made: the authorization request carries it, and a per-client session records it when it is opened (new optional field `method` in the `ses.<person>.<sid>` record, a provider's kind or `recovery`, written with the record a code exchange already writes and read with the record a refresh already reads, so a refresh still costs 4 writes, 4 reads and one directory lookup). A recovery sign-in is evaluated as its ServiceAccount, as before; anybody else is asked of the directory, whatever their subject looks like. Token exchange is unchanged: its grant is decided from the verified proof, and a session it opens keeps the subject this issuer minted from that proof, as a ServiceAccount only when it names one and has no `@` (a person trading their own sign-in is a person, even with an address such as `k8s:ns:x@corp.example`). A session opened before the upgrade has no `method` and is evaluated as a person: a recovery sign-in's per-client sessions from before the upgrade stop refreshing (the directory knows no such person) and sign in again; a person's are unaffected. During a rolling upgrade an older replica opens sessions without the field, with the same effect.
- **The console's own recovery sign-in form is accepted only from the browser it was served to.** The standalone console's `POST /login/recovery` took a form-posted proof with no state, so whoever held a valid recovery proof could post it from a page of theirs and sign a victim's browser into the console as the ServiceAccount (a login CSRF). The console's sign-in page (`GET /login`) now embeds a signed state, bound to recovery, in the recovery form and sets a recovery cookie of its own (`__Host-access_roster_recovery`, `access_roster_recovery` without Secure cookies; HttpOnly, SameSite=Lax, 10 minutes) to it; a form post is refused with 400 ("this sign-in did not start in this browser") unless the cookie equals the state, compared in constant time (`access.RecoveryStartedHere`, beside `access.LoginStartedHere` for the provider callbacks), and the state verifies as a recovery state. The console's provider callback now refuses a recovery state in turn. The post body is limited to 16 KiB before anything is read (413 beyond it); the standard library would otherwise read up to 10 MiB of form, or 32 MiB of multipart. The cookie is cleared once the proof is accepted. The programmatic channels are unchanged and need no page, since another site cannot send them without a CORS preflight this console never answers: a bearer (`Authorization: Bearer <proof>`, the one-line curl) and a JSON body. A JSON body is now read only under a `Content-Type` of `application/json`: read regardless, a cross-site `text/plain` post carrying JSON was the same forgery. Anything else is a form: `curl -d proof=…` without the page's state and cookie is refused (use the bearer), and the proof is no longer read from the query string. A console built without a state codec (no real deployment) offers no recovery form; the bearer still works.
- **A sign-out after a step-up now ends what was opened before the step-up.** When the same person signs in again in the same browser (a step-up, `prompt=login`, `max_age`), the sign-in the browser held is ended, but the per-client sessions opened under it stayed filed under its id, while a sign-out revokes the sessions filed under the sign-in the browser holds; so they survived the person's sign-out and ran on to their own end, and their clients were never told. The sessions are now carried over to the new sign-in: each of the person's live sessions filed under the old sign-in is re-filed under the new one (written over the revision read, so a refresh or revocation racing it is never undone, and keeping the store lifetime it had), and the clients the old sign-in was used at are added to the new one's, so a later sign-out revokes them and sends their Back-Channel Logout as it does for the rest. Nothing is revoked, announced or audited by the step-up itself, as before. The old sign-in's clients are read, then it is ended, then its sessions are moved, so a code redeemed under it either files its session before the move or finds the sign-in ended and ends its own. Carrying is best effort: a session that cannot be moved is logged and left where it was, and the others are still moved. Over a State that keeps no revisions nothing is moved (every adapter the issuer ships keeps them), since a plain write there could bring back a session revoked meanwhile. The same person's sessions in other browsers are not touched. A step-up costs `2 + N` State reads and `C + M` writes more, where N is the number of the person's sessions in the index, M the number moved and C the number of clients the old sign-in was used at; a refresh and a silent sign-in cost nothing more. An authorization code completed under the old sign-in and redeemed after the step-up is refused at redemption (see the next entry): its client starts again and completes silently under the new sign-in.
- **An authorization code completed under a browser sign-in that has since ended opens nothing, and a sign-out ends the sign-in before its sessions.** A silent sign-in reads the browser's sign-in and then completes the request, and the relying party redeems the code a moment later. A sign-out in another tab used to revoke the sessions filed under the sign-in, tell the clients (Back-Channel Logout, up to 5 seconds each), and only then end the sign-in, so the sign-in and its cookie stood through all of that; a code redeemed anywhere before the end opened a session filed under a sign-in that then no longer existed, which no later sign-out could reach, or, for a client that asked for `openid` alone, an ID token from a sign-in that would never announce its end. Both sides are now ordered so that this cannot happen. A sign-out (and every other end of a sign-in: another person signing in in the same browser, the absolute session limit) ends the sign-in first, then lists and revokes its sessions and tells the clients; what is revoked, announced and audited is unchanged. A code redemption files its session first and then reads the sign-in; when the sign-in has ended it revokes the session it just filed and answers `invalid_grant`. Either that read comes after the end, or the session was filed before the sign-out listed them, and the sign-out revokes it. A code that opens no session (a client that asked for `openid` alone, or a request whose every scope the client may not ask for) is held to its sign-in before its access token is minted: the client is recorded among the sign-in's clients first and the sign-in read after, so the code is refused or the client is told at sign-out, except for a sign-out whose two adjacent store calls (reading the clients, ending the sign-in) straddle both. Ending a sign-in, and carrying a step-up's sessions over, is no longer cancelled with the request that asked for it: a browser that disconnects once the sign-in has ended no longer leaves its sessions unrevoked, or unmoved, under it. That work runs on the request's values without its cancellation, bounded at 30 seconds, and the audit record is now written before the clients are told, which runs last, each Back-Channel Logout bounded by its own 5-second timeout. The relying party starts again and the browser, signed out, signs in interactively. This covers every completion, silent or interactive (a code completed under a sign-in that another person's or a step-up replaced before it is redeemed is refused the same way). An `authorization_code` grant under a browser sign-in that opens a session costs one State read more (5 reads, 9 writes), and a refused one also revokes the session it filed; one that opens no session costs one read, as an `openid`-only grant already did, now also for a grant with no scope. A refresh is unchanged (4 writes, 4 reads, 1 resolution), and so is a grant with no browser sign-in.
- **The issuer's recovery sign-in is accepted only from the browser the form was served to.** `POST /login/recovery` checked the signed `state` field and nothing else, and a valid state is anybody's to fetch: load the sign-in page for a request of your own and read it off the form. So whoever held a valid recovery proof could post it from a page of theirs and sign a victim's browser in as the ServiceAccount (a login CSRF). The sign-in page that renders the recovery form now sets a recovery cookie (`__Host-access_roster_recovery`, `access_roster_recovery` without Secure cookies; HttpOnly, SameSite=Lax, 10 minutes) to the form's state, and the POST is refused with 400 ("this sign-in did not start in this browser") unless the browser presents that cookie with the same value, compared in constant time, as the provider callback does with the login cookie. A cross-site POST never carries a SameSite=Lax cookie. The recovery cookie is not the login cookie, because the issuer and a console mounted on its host both write the login cookie: opening any sign-in page, or following a provider button, would otherwise break a recovery form already open. The recovery form's state is also marked as a recovery state (`access.RecoveryPurpose` in its binding), and the recovery POST refuses a provider round trip's state while the provider callback refuses a recovery state, so neither door accepts the other's. The POST body is limited to 16 KiB before it is parsed (413 beyond it). The cookie is cleared once a proof is accepted; a refused proof keeps it so the form can be submitted again. A person who opens the sign-in page in two tabs can recover only from the last one loaded. A recovery form loaded before the upgrade is refused once; reload the page.

## v1.67.0

### Changed

- **Behaviour change: a client that presents a spent refresh token more than 30 seconds after it was spent now ends its session.** Refresh token reuse detection, per OAuth 2.0 Security BCP (RFC 9700 section 4.14.2): a refresh token used by both a thief and its rightful holder is presented twice, and the issuer cannot tell which presentation is the thief's, so the session the token was spent in is ended. The session is ended only once the library has authenticated the client and matched it to the session's: a spent token presented under another client is refused with nothing ended. The grant is refused with `invalid_grant`; the successor and every later refresh token of the session stop working, the session is gone from every listing, and userinfo refuses the access tokens that name it, as `RevokeID` ends a session; a client that declares a `backchannel_logout_uri` is sent a logout token for it. What is not ended: the browser sign-in (SSO) the session was opened under, so a false positive costs one client's session and not every one, and access tokens already issued as JWTs, and credentials already obtained by token exchange (RFC 8693), which stay valid until they expire. The revocation is audited as `roster.session.revoked` by the system with the scope `refresh_token_reuse`; the record names no browser sign-in, since a sign-in's id is its cookie's value. Ending the session is conditional on the session record being the revision read when the reuse was detected, so of several concurrent presentations of one spent token exactly one ends the session, writes the audit record and sends the logout tokens, and the others are refused with nothing written; the audit record follows that conditional delete, so it is never written for an end that did not happen. A reuse is acted on only when the token was spent more than 32 seconds ago (the grace window plus a 2-second clock tolerance); between 30 and 32 seconds it is refused and nothing is ended, so a replica whose clock runs a little ahead cannot turn a client's own burst of refreshes into a revocation. The replicas are expected to keep their clocks with NTP. As with any refresh token reuse detection (RFC 9700), a spent refresh token that leaks while its mark is kept (up to 24 h) can be presented once to end that session. Inside the 30-second grace window a replay is still answered with the same successor, exactly as before. To make this detectable, a spent token's pointer is kept until the absolute deadline of its refresh family, `auth_time` plus the absolute limit (24 h by default), so a reuse is detected for as long as the family it would end can live and never longer (a workload's session, which has no `auth_time`, or a deployment with no absolute limit, keeps it until the session's end as the rotation set it, at most the refresh lifetime, 12 h by default), instead of 30 seconds, and holds when it was spent, the session id and the successor sealed under the spent token (AES-256-GCM, key derived from the spent token, so the keyspace never holds a live refresh token in plain): `spent:<ms>:<sealed>:<session>`. A mark that does not open with the presented token ends nothing. That is the TTL of the write a rotation already makes, so a refresh still costs 4 writes and 4 reads; a reuse costs 2 reads and 4 writes (the record and its three index sets), a reuse that lost to a concurrent one 2 reads and 1 failed conditional delete, a repeated reuse 3 reads. Storage holds one small spent mark per refresh for that window rather than for 30 seconds: for a client refreshing every 5 minutes, up to about 288 marks per session. A mark dated more than 2 seconds ahead of the reading replica's clock is logged and counted (`access_issuer.spent_mark_ahead`), since replica clock skew moves the grace window. A token spent by v1.66.0 (the `issuer:session-rotated:` record, read for one release) cannot be detected after its 30 seconds, since that version kept nothing longer; during a rollout, a replay inside the grace window that lands on a v1.66.0 replica is refused, as that version does not read the new mark.
- **Audit catalogue 1.9.0**: the summary of `roster.session.revoked` names the scopes the issuer uses on its own: `refresh_token_reuse`, when it ends a session on a refresh token reuse; `pre_upgrade_cookie`, when a browser signs out with a sign-in cookie set before the cookie had a secret of its own (by an `anonymous` actor); and `sign_in_replaced`, when another person signs in in the same browser (by the person signing in). What a record carries is unchanged. An audit installation on Kubernetes registers the new catalogue automatically when the service starts; a Lambda audit writer needs the new catalogue release asset (`roster-1.9.0.yaml`) before it can keep records of this version.
- **A refresh_token grant makes 4 State writes and 4 reads (was 14 and 8), and asks the directory once (was 4); an authorization_code grant makes 9 writes and 4 reads (was 13 and 5).** One resolution per request, the snapshot decoded once per blob version, the last-known groups rewritten only when they change, when the stored time is an eighth of the hold window old (a hold can now end up to that much early, never late), or when the stored record is no longer the one this process wrote (checked with an eventually consistent read of its revision only, the new optional `port.RevisionPeeker`), the access-token record written once, the refresh token and its session read once and the spent token marked in its own pointer (`spent:`, written only over the revision read, so a concurrent refresh becomes a replay and a revocation during a refresh is not undone), and the index and sign-in-client sets added to only when a membership would lapse. Behaviour is otherwise unchanged; `issuer:session-rotated:` is still read for one release.
- **The issuer's sign-in cookie no longer equals the sign-in id; every browser signs in once more after the upgrade.** The browser session cookie (`__Host-access_issuer_sso`, `access_issuer_sso` without Secure cookies) used to carry the sign-in record's id, which the operators' listing shows for every signed-in person and every session carries, so anyone who saw an id could set the cookie and be signed in as that person until the sign-in ended. The cookie now carries a fresh 256-bit random secret (base64url). The store keeps only its labelled SHA-256 hash, as a pointer `issuer:sso-cookie:<hash>` (layout kind `issuer-sso-cookie`) to the sign-in id and in the sign-in record, with the record's lifetime; the pointer is removed when the sign-in ends (sign-out, revocation, the absolute session limit). The id stays the sign-in's name everywhere it was shown and authenticates nothing. A sign-in costs one State write more, a silent sign-in one read more, and ending a sign-in one delete more (and, for "sign out everywhere", one read per sign-in). A cookie set before the upgrade signs nobody in: those browsers sign in once more. For one release it still signs its own browser out: `/logout` and `/end_session` with a pre-upgrade cookie end that sign-in and the sessions it opened, and tell the clients, as before. This works only for a sign-in written before the upgrade, and never signs anybody in. A sign-out whose sign-in cannot be read from the State now answers 503 and keeps the cookie, so the person can retry, instead of clearing it and reporting success. An interactive sign-in (step-up, `prompt=login`, `max_age`, another account) now ends the sign-in the browser held before, once the new one is handed to the browser (a refused recovery leaves the earlier one in place). When the earlier sign-in was another person's (a shared browser), their sessions under it are revoked and the clients told, as at sign-out; a sign-in whose record cannot be written clears the browser's previous cookie. Revoking one browser in the console now also ends the sessions filed under a sign-in that has already ended. A sign-out by a pre-upgrade cookie is audited as `roster.session.revoked` by an `anonymous` actor with scope `pre_upgrade_cookie`, since an id proves nothing about who presented it; the shared-browser case is audited as `roster.session.revoked` by the person signing in with scope `sign_in_replaced`. During a rolling upgrade, or after a rollback, an older replica still accepts a sign-in id as the cookie, so the protection holds only once no older replica serves traffic.
- **Upgrade note: an IAM policy that limits `dynamodb:LeadingKeys` to the issuer's kinds must allow `issuer-sso-cookie` before the upgrade.** Without it the cookie pointer cannot be written or read, and single sign-on stops without an error page: every console asks for a login again. The policy the Pulumi library writes does not restrict leading keys and needs no change.

## v1.66.0

### Added

- **Pulumi library: opt-in API Gateway access logs (path and status only).** `LambdaArgs.AccessLogs` (off by default) declares a log group `/aws/apigateway/<FunctionName>` (`RetentionDays`, default 7) and the stage's access log settings, for an installation where MCP clients refresh every few minutes and the request pattern is worth seeing. The line holds the request time, id, method, path, status and the two latencies; the query string (OAuth codes and state), headers, source address and identity are never logged. An HTTP API needs no account-level CloudWatch role, so no IAM resource is added. New output `AccessLogGroupName`.
- **Confidential clients can let the issuer generate their secret.** A policy client's `secret` is now one of two shapes: a string, the name of an input (`clients/<id>/secret`, unchanged), or the object `{generate: true}`. With the object form the issuer makes a 32-byte random secret (base64url, no padding) the first time it runs and keeps it with its credentials, at `credentials/oidc-client/<id>/secret`, as one record that also has room for a previous secret and the time until which it is accepted. The write is create-only, so replicas and Lambda invocations that start together agree on one value, and an existing record is never overwritten. When the input `clients/<id>/secret` exists at that moment it is adopted, so a client that already uses it keeps working. The token endpoint reads the record of a generated client first and, only while the store says there is none, the input; a corrupt or unreadable record authenticates nobody (a record read earlier is served for at most 5 minutes when the store fails), and a client with a named input secret is served by the input alone, so a first deploy never refuses a valid client before the secret exists. Start-up is refused for a generated client when the State is not shared between replicas (the memory adapter excepted); the record is cached for 30 seconds. The check compares both the current and the previous secret on every request, in constant time. Generation is refused (at start-up) when the secrets adapter cannot create a secret only if absent, which is the `legacy` adapter's, and for a public or exchange client; `generate: false` is refused. A failure for one client is logged and counted (`sluis.client_secret.reconcile`, `sluis.client_secret.auth`) and does not stop the issuer; it is retried every five minutes on a server and on the directory refresh schedule on Lambda. An older binary refuses the object form, so roll every replica before writing it.
- **A generated client secret can be exported.** An `exports[]` entry with `source: oidc-client` and `client: <id>` copies a confidential client's id and its current generated secret (never the previous one) as `client-id` and `client-secret` to the export store, written with `replace`, so the relying party reads it through its own secret operator. The client must be confidential with `secret: {generate: true}`, or the policy is refused at start. A copy is made at start and every `interval` (default 1h, minimum 1m); there is no watch on the Secrets port, so a change by anything other than `sluisctl clients rotate` reaches the copy at the next interval; a rotation by that command copies the new secret out at once.
- **Rotate, look at and purge a generated client secret: `sluisctl clients rotate|show|purge <id>`.** `rotate [--overlap 24h]` makes a new secret and keeps the old one accepted for the overlap (default 24h, at most 7d, `0` cuts it at once), so a relying party is not cut off between the new value being made and being deployed. A rotation during an open overlap drops the older previous secret, and says so. It is refused for a client that is not generated or has no record yet. `show` prints metadata only (created, rotated, whether a previous exists, until when it is valid, whether the client is orphaned): never a secret. `purge` deletes the stored record of a client that is no longer a generated client of the policy (an orphan); the copy an export made is not touched, since the export is declared with the client and goes with it, and neither is the input secret `clients/<id>/secret`: the command tells the operator to delete both by hand. The commands call the issuer's `POST /.access/client-secrets/{rotate,show,purge}`, which admits a bearer token from this issuer that was issued to `accessctl` (sluisctl's default client) or `console` and whose `groups` hold the operators group, as the session service's operator calls do. A verified caller refused for it (not an operator, wrong audience, busy, not generated, still declared, no record, a bad overlap) is audited as `roster.client.secret.denied`; a request with no accepted token is only logged and counted (`sluis.client_secret.admin_refused`). Writers of one record are serialised by a per-client lease on the State (ssm's conditional write is a read and then a write). The resolver on the replica that served the request forgets the client at once; another replica's 30-second cache lapses on its own. Metrics: `sluis.client_secret.rotations`, `sluis.client_secret.purges`, `sluis.client_secret.orphans` (no client ids in labels).
- **Orphaned generated secrets are reported once.** At start, on the five-minute tick and on the Lambda refresh, stored `credentials/oidc-client/<id>/secret` records whose client is no longer a generated client in the policy are logged and audited once (an `orphaned` time is written into the record); they are never deleted by the issuer. A client that comes back clears the mark and keeps the stored secret unchanged.
- **Audit catalogue 1.8.0**: `roster.client.secret.created`, `.adopted`, `.rotated`, `.orphaned`, `.deleted` and `.denied` on a `client` target, with the client, the times and the overlap, never a value (actor `system` for reconcile and the orphan report, the person for a rotation and a purge). An audit installation on Kubernetes registers the new catalogue automatically when the service starts; a Lambda audit writer needs the new catalogue release asset (`roster-1.8.0.yaml`) before it can keep these records.

### Changed

- **Every cookie sluis sets carries the `__Host-` prefix when cookies are Secure.** The console session (`access_roster_session`), the three flow cookies (`access_roster_connect`, `access_roster_login`, `access_roster_link`) and the issuer's browser session (`access_issuer_sso`) are now written, read and cleared as `__Host-<name>`. A browser honours that prefix only for a cookie that is Secure, has `Path=/` and no `Domain`, which these already were, so a sibling host in the same zone can no longer plant ("toss") a cookie that the issuer or console would then read. One helper, `access.CookieNameFor`, decides the name for every write, read and clear. With `issuer.secureCookies` false (plain-HTTP local development) the old unprefixed names are kept, because a browser rejects a `__Host-` cookie that is not Secure. The old names are not read as a fallback: sessions and in-flight sign-ins started before the upgrade are dropped once, and every user signs in again a single time.

## v1.65.1

### Fixed

- **Chart: documents mode projects the input secrets by `secrets.source`, not by the credentials adapter.** With `adapters.secrets: openbao` (or `ssm`) the chart skipped every input file, so an installation that reads its inputs (client secrets, the Google OAuth client, the signing state secret) as files from Kubernetes Secrets, and keeps credentials and exports in OpenBao, had none of them mounted. Now `secrets.source: file` projects the Secrets declared in `secrets` at `<root>/<name>` (mode 0440, read-only, pod `fsGroup` set), whatever the adapter; `ssm` or `openbao` as the input source projects nothing. In documents mode a policy client's `secret` is the secret's name, so each `clients/<id>/secret` is declared in `secrets` (`{name, secretName, key}`) and the chart refuses a confidential client whose name has no entry. The refusal covers every input name the documents use (the Google OAuth client, `...Secret` fields such as `stateSecret` and `passwordSecret`), a name declared twice is refused, the projected `secretName`, `key` and `name` are quoted, and the pod runs as 65532:65532 with `fsGroup` 65532. The #323 guards are unchanged. Values mode keeps its value shapes; its projected volume is also mode 0440 now.

## v1.65.0

### Changed

- **`github.com/truvity/policy` v1.44.0 to v1.45.0** (root module and `deploy/pulumi`). v1.45.0 removes `config.Secret` and replaces the `...Env` spellings in its shared fragments with `...Secret` names; sluis used neither (its schemas do not `$ref` those fragments and nothing called `config.Secret`), so no document, schema, chart value or behaviour changes. The unused `config.Secret` wrapper in `internal/config` is removed. sluis keeps its own `internal/secrets` resolver (the `SLUIS_SECRET_<NAME>` env mapping, the legacy v1 locations, the bulk SSM read with a five-minute refresh); it does not yet sit on policy's `config.NewSecrets`.

## v1.64.0-rc.1

The release candidate of v1.64.0: its notes are v1.64.0's, below. Cut to exercise the release workflow's library-tag job before the final tag; its npm package was not published (npm refuses a pre-release without `--tag`).

## v1.64.0

Documents mode, `sluisctl render` and the installation document, the OpenBao Secrets adapter, `k8s-aws`, verify-only signing keys. Presets `server`, `k8s-minimal` and `k8s-openbao` are refused, and the chart's values mode is deprecated: see [upgrade to v1.64](docs/how-to/upgrade/v1.64.md).

### Added

- **Chart: `documents.service` and `documents.policy`**, the rendered documents of one installation
  (`sluisctl render`). The chart puts them into their ConfigMaps unchanged, holds them to what it mounts (release name,
  `policy.file`, public URLs, audit token, verify-only keys, the controllers' console URL) without rewriting them, and
  keeps only the deployment-level values. A secret an `openbao` or `ssm` secrets adapter delivers is not projected from a
  Kubernetes Secret in this mode. Helm cannot run the loader, so the chart also refuses an `http://` OpenBao address and a
  credential-looking key in adapter settings; the chart README says documents must come from `sluisctl render` (and
  `--check` in CI). A Truvity-shaped case (k8s-aws, OpenBao, KMS-wrapped, 20 clients, both controllers) is
  rendered from the installation's documents and held byte for byte.
- **The installation document and `sluisctl render`: estates render their documents through sluis**
  ([0038](docs/decisions/0038-estates-render-through-sluis.md)). An installation
  (`apiVersion: sluis.truvity.github.io/installation/v1`, `schemas/config/installation.schema.json`) is what an
  estate knows about one installation: its shape (`lambda`, `kubernetes`, `server`), the AWS resources and the OpenBao it is built
  on, whom it trusts, its clients and its exports. `sluisctl render --installation <file> --out <dir>` writes the service
  document (`sluis.yaml`, v3) and the policy document (`policy.yaml`, v2) from it, deterministically, held to the loader
  the service runs at start; `--check` compares with the files already there and exits 1 on a difference. Documented in
  [sluisctl](docs/reference/sluisctl.md#render-an-installation-in-the-two-documents-out) and
  [configuration](docs/reference/installation-document.md).
- **A public configuration package, `github.com/truvity/sluis/config`**: the service (v3) and policy (v2) document
  types, `Load` and `Validate` against the authored schemas, `LoadInstallation`, `Render` and `InstallationSchema`.
  Estates and the Pulumi library use it instead of `internal/config`.
- **Pulumi library: `LambdaArgs.Installation`**, rendered by the public package. The library completes it with the
  instance, region, account and function name from its arguments when the installation leaves them out, and refuses
  one that says another.
- **`secrets/openbao`: the Secrets port in an OpenBao KV version 2 mount**
  (`adapters.secrets: {adapter: openbao}`), laid out like SSM layout v3: `<root>/private/config/<name>`,
  `<root>/private/credentials/<kind>/<id>/<ref>` and `<root>/export/<path>`. It shares the Export
  adapter's client (the `jwt` or `kubernetes` login, the token file read again at every login, a token per
  namespace, TLS always verified against `caFile` or the system CAs). Compare-and-swap is KV's
  `cas`, atomic on the server. The root is configurable: `sluis` inside an installation's own OpenBao namespace
  (recommended), or `sluis/<instance>`; `private` and `export` are reserved. An export in the default destination
  (`ports.export` unset) honours an entry's `namespace`: the adapter writes the same `export/<path>` in that
  namespace, so a preview runner App can land in `devel`. An export of properties is stored as the properties
  themselves, one KV field each, readable by a consumer's External Secrets as it reads a copy made by
  `ports.export: openbao`. The policy it needs is in
  [configuration](docs/reference/openbao-secrets-adapter.md). Settings: `address`, `caFile`,
  `mount`, `namespace`, `root`, `auth{method, mount, role, tokenFile}`. The OpenBao client accepts https only, never
  follows a redirect, puts a 403 down to the token only when it is older than 30s, and a `Put` under a
  `cas_required` mount is a refusal that names it, not a conflict.
- **Preset `k8s-aws`**: DynamoDB state, SSM secrets (OpenBao with `adapters.secrets`), S3 blobs, kms-wrapped
  signing, the DynamoDB trigger, the ticker and the `connect` audit sink. Every adapter it names is built, so it
  starts; naming `adapters.secrets: openbao` needs no `platform.openbao`.
- **`signingKey.verifyOnly`: public keys published and never signed with**, each with an optional `kid` (default:
  the RFC 7638 thumbprint, the file signer's derivation), `alg` and `until`, so tokens issued by the old file keys
  keep verifying across the cutover to `kmsWrapped`. A private key stops the start. The chart mounts them from a
  ConfigMap (`signingKey.verifyOnly[].pem`). `until` is required; the chart refuses private material at render
  (PEM blocks other than PUBLIC KEY and CERTIFICATE, anything containing "private", a JWK with a private member).
- **Release assets `sluis-config-schemas_<version>.tar.gz` and `sluis-audit-catalogue_<version>.tar.gz`**, in
  `checksums.txt`: the JSON Schemas of the configuration documents, and the audit catalogue `roster.yaml` with
  every schema it references (the audit writer refuses to start without one). A test holds each bundle to its source.
- **Chart: `serviceAccount.awsIdentity`** (`pod-identity`, the default and renders nothing, or `irsa`, which annotates
  the account with `eks.amazonaws.com/role-arn: <serviceAccount.awsRoleArn>`).

### Deprecated

- **Chart: the values-mode** (`config`, `policy`, `exchange.clusters`, `exchange.aws`, `githubApps.catalogue` and
  `slackApps` rendering the two documents) keeps working for one minor and is then removed; NOTES.txt says so while it is
  used. Render the documents with `sluisctl render` and pass them as `documents.service` and `documents.policy`.
- **`LambdaArgs.Config`, `Policy` and `PolicyPath`** (Pulumi library): write the estate's facts as `LambdaArgs.Installation`.
  They keep working for one minor, and `NewLambda` logs a warning while one is used.

### Changed

- **Presets `server`, `k8s-minimal` and `k8s-openbao` are marked unavailable**: each names adapters that are planned
  and not built, and loading one now fails with a message naming the preset and the missing adapters, unless
  `adapters` replaces every one of them. They are not removed; `docs/reference/adapters.md` lists them.
- **The `$id` of `sluis.schema.json` says v3**, the version of the document (it said v2). A schema's `$id`
  version now follows its document's `apiVersion`.
- **`docs/reference/adapters.md` shows the `invoke` trigger as implemented**, as is the `aws-serverless` and
  `aws-hybrid` presets' trigger: the generator now links every adapter any binary registers.
- **The Pulumi library imports no `internal/` package, and its `require` of the root module is pinned by the
  release.** `deploy/pulumi/vX.Y.Z` is now tagged at a child of the release commit whose `go.mod` requires `vX.Y.Z`
  (`hack/pin-pulumi-require.sh`), so the require is never bumped by hand before a tag and a library tagged against a stale
  one no longer exists. A test holds the library to no `internal/` import.
- **Durations are written without their zero units** (`1h`, not `1h0m0s`) by the service document's types and the
  renderer; both read back to the same span.
- **Chart: KMS signing renders cleanly.** With `config.signingKey.kmsWrapped` (or `kms`, or `adapters.signing`
  naming one) the chart renders no signing Certificate and mounts no signing Secret, and refuses
  `signingKey.additional` and `existingSecret`; the chart's default `config.signingKey.file` is dropped with
  `file: null`.
- **Chart: `adapters.secrets: openbao` composes with `config.secrets.source: file`.** The chart checks
  `adapters.secrets.settings.caFile` and `.auth.tokenFile` against the `exports.openbao.caBundle` and
  `exports.openbao.token.audience` it mounts, one bundle and one audience for the export and the secrets login.
- **Chart: `signingKeyRotationStalled` follows the rotation.** Unset, `maxAgeSeconds` is `rotateEvery` plus two hours
  with kms-wrapped signing (26h for 24h), and 350 days otherwise, as before. An alerts-only release needs the same
  `config.signingKey`, or sets the value.
- **Chart: the replica check understands presets**: `k8s-aws` and the other AWS presets, and `adapters.state`,
  keep state in DynamoDB.
- An explicit adapter that needs OpenBao is no longer refused for a preset's `platform.openbao: false`.

### Deprecated

- **Preset `aws-eks`** is the deprecated name of `k8s-aws` (it never started: it named the unbuilt trigger `watch`).
  It resolves to `k8s-aws` and start logs a warning.

## v1.63.0

One process everywhere (ADR 0037): on AWS Lambda one function and one role, on Kubernetes one Deployment and one Pod Identity role, configured by one service document `sluis.yaml` (`apiVersion` v3, with `controllers.github` and `controllers.slack`) plus the canonical `policy.yaml`. No data migration from 1.62; binary 1.63 and the Pulumi library 1.63 deploy together. **Upgrading:** [upgrade to v1.63](docs/how-to/upgrade/v1.63.md).

### Changed

- **Breaking: one process everywhere.** `sluis serve` is the whole of sluis:
  the issuer, the console, the directory hub and, beside them, the GitHub and
  Slack controllers, each in a loop of its own in the same process. There is no
  separate controller process to deploy: on Kubernetes it is ONE Deployment
  (the chart no longer renders the two controller Deployments), and on AWS
  Lambda it is ONE function (below, in the Pulumi library's entry). The cost is
  accepted: a controller's code runs with the service's permissions and in its
  pod, and a controller that cannot start stops the process.
- **The one service document, `apiVersion: sluis.truvity.github.io/sluis/v3`**
  (`schemas/config/sluis.schema.json`, `sluis serve --config` / `SLUIS_CONFIG`).
  Every key of the v2 `serve` document stays where it was, at the top level,
  and `controllers` is what it adds: `controllers.github` and
  `controllers.slack`, each optional, an absent one being a controller that is
  off. A controller's section holds only what is its own: `consoleURL` (unset is
  `publicURL`), `tokenFile`, `console.auth.aws.audience`, `recordsDir`,
  `appsDir` (GitHub) or `credentialsDir` (Slack) and `interval`. The release,
  the policy, `ports`, `platform`, `preset`, `adapters`, `audit`, `log` and
  `probes` are the process's own, and a controller shares them. `policy.file`
  is required when a controller is named.
  ```yaml
  apiVersion: sluis.truvity.github.io/sluis/v3
  issuerURL: https://access.example
  publicURL: https://access.example/console
  policy: {file: /etc/sluis/policy.yaml}
  secrets: {source: ssm, root: /sluis/example}
  ports: {adapter: dynamodb, dynamodb: {table: sluis}}
  audit: {tokenFile: /var/run/audit/token}
  controllers:
    github: {consoleURL: "http://sluis.access.svc:8080/console", interval: 15m}
    slack: {consoleURL: "http://sluis.access.svc:8080/console"}
  ```
- **N-1.** The binary still loads the v2 `serve` document (and v1, with no
  apiVersion) as this document with no controllers, so `sluis serve` on an
  unchanged document behaves as before. For one release `sluis controller
  github|slack` still run a controller as a process of its own (deprecated, and
  said so in the log), reading either their own v2 documents or the one v3
  document, whose `controllers.<kind>` section they then take; `sluis tick
  github|slack` and `sluis migrate` read either. They are removed in the next
  release.
- **Telemetry.** The one process reports as `access-issuer`: the controllers'
  series no longer carry `github-roster` or `slack-roster` as their service
  name. A dashboard or an alert that selects on those names must stop.
- **Readiness is the whole process.** `/readyz` of the one process is ready
  only once each controller it runs has begun, and a controller that fails to
  start (a refused audit catalogue, an enabled organisation the policy does not
  bind) stops the process, as it stopped the controller before. A controller
  waits for the console, which is its own process, to answer before its first
  pass.

### AWS Lambda and the Pulumi library

- **Breaking: ONE function.** `sluis-http`, `sluis-github` and `sluis-slack`
  are one function from the release zip. It takes API Gateway events (the issuer
  and the console), `{"kind":"tick"|"run","target":"<id>"}` (one pass of one
  target, run by the controller the policy says the target belongs to: the
  GitHub controller's organisations and `github:links`, the Slack controller's
  workspaces; a run-now of an undeclared target ends cleanly, a schedule's tick
  of one fails), and `{"kind":"refresh"}` / `{"kind":"exports"}`. The controllers
  do not loop on Lambda: each pass is assembled for its invocation, as before.
  `SLUIS_ROLE` is retired (a function that still sets it is refused, saying why);
  `SLUIS_CONFIG` names `/opt/sluis/sluis.yaml`. The function's timeout is 300 s
  by default (API Gateway still cuts a request at 30 s) and its memory one
  setting (512 MB). The `invoke` trigger's `github` and `slack` settings name
  this one function, and the library writes them.
- **The library (`deploy/pulumi`, MinPackageVersion 1.63).** `LambdaArgs`:
  `Config` is the one v3 service document (`controllers` included);
  `GitHubConfig`, `SlackConfig`, `HTTP`, `GitHub`, `Slack` and
  `Exports.Function` are gone; `Function` (`MemoryMB`, `TimeoutSeconds`) and
  `FunctionName` (default `FunctionNamePrefix`, so `sluis`) are new. Outputs:
  `FunctionArn`, `FunctionName`, `RoleArn`, `RoleName` replace the
  `HTTP|GitHub|Slack` ones. One role, `<FunctionName>`, with the http role's
  policy and the controllers' needs: credentials and exports read/write, config
  read, the signing keys, `lambda:InvokeFunction` on the function itself,
  `sts:GetWebIdentityToken` (audience-restricted by `WebIdentityAudience`); no
  denials, so the keyring is writable by the one role. The controllers' code runs
  with these permissions: the per-role isolation is gone by decision (S1a). The
  scheduler role may invoke only the function. Kept: reserved instance names, no
  endpoints without `AllowEndpoints`, the `Telemetry.Env` allowlist, the
  release-zip SHA-256 verification, documents held to sluis's loader before
  anything is published, the layer mounted last, every IAM grant under
  `/sluis/<instance>/` (a test holds it, with no `*` but a trailing `/*`).
- **Migrating:** merge the three documents into one v3 document, admit the one role in the policy and the audit
  installation's workload map, and keep `FunctionName: "<prefix>-http"` to replace nothing: [upgrade to v1.63](docs/how-to/upgrade/v1.63.md).

- **Breaking: `NewKubernetesIdentity` makes ONE role for the one pod.** The
  `Serve`, `GitHub` and `Slack` `ProcessArgs` and `ProcessArgs` itself are gone;
  `ServiceAccount` (required) and `Description` are on the args. The role is
  `<prefix>-sluis` (was `<prefix>-sluis-serve`; `-github` and `-slack` are
  gone), with one Pod Identity association for the one ServiceAccount, and the
  outputs are `RoleArn` and `RoleName` (were `ServeRoleArn`, `GitHubRoleArn`,
  `SlackRoleArn` and the names). Its policy is the old serve role's (storage,
  state, signing keys or the wrapped key, with no key-ring denial now that the
  one role signs) and, with the new `Instance` (and `Region`,
  `ParameterKeyArn`), the Lambda role's SSM grants under `/sluis/<instance>/`:
  credentials read/write, config read, exports, the parameter key through SSM
  only. Moving: Pulumi replaces the serve role under its new name and destroys
  the github and slack roles and associations ([upgrade to v1.63](docs/how-to/upgrade/v1.63.md#4-eks-identity-one-role-for-the-one-pod)).

### Chart

- **Breaking: one Deployment.** `controllerGithub` and `controllerSlack` are
  gone, with the Deployments, ServiceAccounts, ConfigMaps
  (`<release>-github-roster-config`, `<release>-slack-roster-config`) and
  PodDisruptionBudgets they rendered. The controllers are
  `config.controllers.github` and `config.controllers.slack` of the one
  service document, and run in the Deployment `<release>`, as its
  ServiceAccount: that account gets the controllers' Role (update, by name, the
  status ConfigMaps and the GitHub links Secret), the token, Apps, credentials
  and records volumes of the controllers named, and an ingress rule that lets the
  pods reach the console through the release's own Service. Policy: `exchange`
  must admit that ServiceAccount (the controllers read the console as it),
  where it admitted `<release>-github-roster` and `<release>-slack-roster`, and
  the audit installation's `workloadIdentity` need map one account.
- **Refused at render:** a controller in `replicaCount` above 1 unless
  `config.ports.adapter` is `dynamodb` (the controllers' tick leases must be in
  a State every replica shares, or every replica acts on every target); a
  controller without `exchange.clusters`, `console.mount` or the console's own
  Service as `consoleURL`; `tokenFile`, `appsDir`, `credentialsDir` or
  `recordsDir` that is not where the chart mounts it.
- The service document the chart renders is `sluis/v3`; the checksum
  annotations are two (`checksum/config`, `checksum/policy`) and a change to a
  controller's section rolls the pod.

#### Removed (chart values)

| Value | Replacement |
|---|---|
| `controllerGithub.enabled`, `controllerGithub.config` | `config.controllers.github` (present is on): `consoleURL`, `interval`, `tokenFile`, `appsDir`, `recordsDir` |
| `controllerSlack.enabled`, `controllerSlack.config` | `config.controllers.slack` (present is on): `consoleURL`, `interval`, `tokenFile`, `credentialsDir`, `recordsDir` |
| `controllerGithub.config.{policy,release,log,ports,platform,preset,adapters,audit,probes}` and the same for Slack | the service document's own keys, which the controllers share |
| `controller{Github,Slack}.{replicas,strategy,minReadySeconds,podDisruptionBudget,resources}` | `replicaCount`, `resources` (the one Deployment's) |

## v1.62.0

Configuration is immutable per instance (ADR 0036): four versioned documents (`serve`, `controller-github`, `controller-slack`, and one canonical `policy`) with `apiVersion` v2, secrets by name from a declared source, SSM layout v3 under `/sluis/<instance>`, and on Lambda the release zip deployed unchanged with the configuration as a layer. Binary 1.62 and the Pulumi library 1.62 deploy together. **Upgrading:** [upgrade to v1.62](docs/how-to/upgrade/v1.62.md).

### Changed

- **Breaking: configuration is four documents, each with an `apiVersion`; the
  policy is one rendered document.** The three service documents (`serve`,
  `controller-github`, `controller-slack`) say how a process runs; the new
  policy document (`schemas/config/policy.schema.json`) says what the
  installation decides, and every service document names it with
  `policy.file`. Each document carries `apiVersion:
  sluis.truvity.github.io/<document>/v2`; this build reads v2 and v1 (no
  apiVersion), converting a v1 document as it loads it through truvity/policy's
  versioned loader, so a deployment can roll the binary first and its
  configuration second. Configuration and policy are read once, at start: a
  change is a new instance. Moved out of the service documents into the policy
  document, as values rather than files:
  - `exchange.clustersFile` → `exchange.clusters`; `exchange.awsFile` →
    `exchange.aws`; `github.owners` → `exchange.github.owners`;
  - `github.runnerTiers` → `apps.github.runnerTiers`; `github.catalogueFile`
    (and the GitHub controller's `catalogueFile`) → `apps.github.catalogue`;
    `slack.catalogueFile` → `apps.slack.catalogue`;
  - the controllers' `enabledOrgs` / `enabledWorkspaces` →
    `controllers.github.enabledOrgs` / `controllers.slack.enabledWorkspaces`;
  - `exports` → `exports`;
  - `policyDir` → `policy.file`, naming the one rendered document.
  `overlayFile` becomes the serve document's `directory.workspaces`. `api`
  (the directory API listener's guard and `consumersFile`) is removed: `sluis
  serve` serves no such listener. A v2 document naming a retired key is
  refused with where it went. The checks the service made at start across
  these (a catalogue grant naming an undeclared group, a Slack App for an
  undeclared workspace, an enabled organisation or workspace the policy does
  not bind, an export of an undeclared App) are the policy document's, run
  wherever it is loaded. A v1 document keeps working until moved
  ([upgrade to v1.62](docs/how-to/upgrade/v1.62.md)).

- **Breaking: a service document names its secrets; `secrets.source` delivers
  them.** A key ending in `Secret` holds a secret's NAME, from the layout of
  docs/decisions/0036: `valkey.passwordSecret` (`valkey/password`),
  `recovery.passwordSecret` (`recovery/password`),
  `signingKey.kms.stateSecret` and `signingKey.kmsWrapped.stateSecret`
  (`issuer/state-secret`), a declared workspace's `keySecret`
  (`directory/<id>/key`), and `oauthClient.provider`, whose two halves are
  `providers/google/<provider>/client-id` and `client-secret`; a confidential
  client's secret is `clients/<client-id>/secret`. `secrets: {source: env |
  file | ssm, root}` says how a name is delivered: `env` reads
  `SLUIS_SECRET_<NAME>`, `file` reads `<root>/<name>` on every use, `ssm` reads
  every parameter under `<root>/private/config/` at once (decrypted, paged)
  and again after `refresh` (5m). Retired, each refused in a v2 document with
  where it went: `valkey.passwordEnv`, `oauthClient.idFile`, `.secretFile`,
  `.secretEnv`, `adminPasswordEnv`, `recovery.passwordFile`,
  `clientSecretsDir`, `signingKey.kms.stateSecretFile`,
  `signingKey.kmsWrapped.stateSecretFile` (the `kms` and `kms-wrapped`
  adapters' setting is `stateSecret` too) and `directory.workspaces[].keyFile`.
  A v1 document keeps reading each secret where it named it.
- **Breaking: SSM layout v3; an installation has one root, `secrets.root`
  (`/sluis/<instance>`).** The `ssm` Secrets adapter takes it from the serve
  document's `secrets.root` (`source: ssm`); naming another root under
  `adapters.secrets.settings` is refused, and with neither the start is
  refused. Two installations share an account by their roots:
  `<root>/private/config/...` (what an operator seeds),
  `<root>/private/credentials/...`, `<root>/export/...`. A v1 document that
  names no root keeps `/sluis` (layout v2) until it moves. The secrets are copied with `sluis migrate
  ssm-layout` first ([upgrade to v1.62](docs/how-to/upgrade/v1.62.md#3-copy-the-configuration-secrets-to-the-instance-root)).
- **Breaking: on Lambda, `SLUIS_CONFIG` names the document; `SLUIS_CONFIG_FILE`,
  `SLUIS_SECRET_FILES` and the `ssm:<path>` variables are retired** and refused
  at start, naming the v1.62 Pulumi library to deploy with: a function the
  v1.61 library deployed does not start on this binary, so the binary and the
  library move together. The secrets are read by the document's `secrets`
  source.

- **Breaking: Pulumi library (`deploy/pulumi`): the release zip unchanged, and
  the configuration as an immutable layer.**
  - `LambdaArgs.PackageSHA256` is required and checked; the functions' code is
    the release zip byte for byte (nothing is added to it). A package older
    than the library (`sluis-lambda_<version>_…zip`, or `PackageVersion`) is
    refused: v1.61 and older cannot read the layer.
  - The three service documents (`Config`, `GitHubConfig`, `SlackConfig`, v2)
    and the policy (`Policy`, a document, or `PolicyPath`, a file or directory
    rendered by sluis's own renderer) are held to sluis's own loader and
    published as one `aws.lambda.LayerVersion` (`<prefix>-config`) mounted
    LAST at `/opt/sluis`; a change publishes a new version and updates the
    functions, and old versions are kept (a rollback is re-pointing). The
    library writes the apiVersion, `policy.file`, `secrets` (`ssm`,
    `/sluis/<instance>`, the region), `recovery.passwordSecret`,
    `recovery.enabled` and the state secret's name; a document that disagrees
    is refused.
  - A function's environment is `SLUIS_ROLE`, `SLUIS_CONFIG=/opt/sluis/<role>.yaml`
    and the telemetry layer's `OTEL_*`; nothing else.
  - New required `Instance`: the SSM root `/sluis/<instance>`. The state secret
    and the recovery password are generated at
    `/sluis/<instance>/private/config/{issuer/state-secret,recovery/password}`
    (same values: the random resources are unchanged). IAM: every function
    reads and writes `<root>/private/credentials/*` and `<root>/export/*` and
    nothing above them; only the http function reads `<root>/private/config/*`
    (by path, for its `secrets` source), which the controllers are denied;
    `ExportReadPolicy` takes the instance and reads `<root>/export/*`. (Before,
    a controller's allow on `/sluis/private` let `GetParametersByPath` reach
    `config/*` past its Deny.)
  - Removed: `Catalogues`, `CataloguePaths`, `Env`, `FunctionArgs.Env`,
    `FunctionArgs.SecretFiles`, `SecretFile`, `ConfigFilePath`,
    `StateSecretPath`, `RecoveryPasswordPath` and
    `WrappedSigningArgs.KeepRemoteSigningKeys`: with `WrappedSigning` the two
    asymmetric keys are no longer declared, and a `Config` naming
    `signingKey.kms` beside it is refused.
  - Refused in the documents the library publishes: any `endpoint`
    (`secrets.endpoint`, `ports.dynamodb.endpoint`, ...), unless
    `AllowEndpoints` (a LocalStack test); an `Instance` named `private` or
    `export`. `Telemetry.Env` holds `OTEL_*`, the telemetry layer's own
    (`ACCESS_ROSTER_*`, `OPENTELEMETRY_*`) and `AWS_LAMBDA_EXEC_WRAPPER`, and
    nothing else. Only the function that runs the exports reads `export/*`;
    with `ParameterKeyArn`, each role may use the key only for the parameters
    under its own prefixes (`kms:EncryptionContext:PARAMETER_ARN`).
  - Moving from `kms` to `WrappedSigning` drops the old key ids from the JWKS at once: it is a step of its own
    ([upgrade to v1.62](docs/how-to/upgrade/v1.62.md#6-retire-the-asymmetric-signing-keys-only-when-moving-from-kms-to-wrappedsigning)).
  - The directory-refresh and exports schedules are unchanged.

### Added

- **`sluisctl policy render <file or directory> [-o <file>]`.** The one place
  layering happens: merges a directory of v1 policy files, access documents
  and policy document fragments (`apiVersion: sluis.truvity.github.io/policy/v2`)
  into the canonical policy document, held to every check the service runs at
  start. A process loads exactly one policy document.
- **`SLUIS_CONFIG`** names the service document when no `--config` is given.

### Chart

- **Breaking: the chart renders the four documents.** `<release>-config`,
  `<release>-github-roster-config`, `<release>-slack-roster-config` and
  `<release>-policy`, at the paths they had (each service document by
  `subPath`, read once at start; the policy as the directory each process
  had for it); each pod carries one checksum per document it reads, so a change to one
  controller's document restarts that controller only, and a policy change
  restarts all three. The clusters, AWS, overlay and catalogue ConfigMaps are
  gone: `exchange.clusters`, `exchange.aws`, `githubApps.catalogue` and
  `slackApps` are rendered into the policy document. `policy` is the policy
  document without its apiVersion (the chart writes it, and the values schema
  holds it to the policy schema).
  The secrets the config names are projected as files under
  `/var/run/sluis/secrets` from the new `secrets` value (`{name, secretName,
  key}`) and each confidential client's Secret (`clients/<id>/secret`), and
  `config.secrets` must be `{source: file, root: /var/run/sluis/secrets}`
  (the default); the `client-secrets` volume and `config.clientSecretsDir`
  are gone. `secretEnv` stays, for `secrets.source: env`.
  A confidential client's Secret key is still `policy.clients.<id>.secretKey`
  (default `client-secret`), projected as `clients/<id>/secret` and left out
  of the rendered policy document.
  `config.policy.file` must be `/var/run/access-issuer/policy/policy.yaml`
  (the controllers': `/var/run/github-roster/policy/policy.yaml`,
  `/var/run/slack-roster/policy/policy.yaml`).
### Removed (chart values)

| Value | Replacement |
|---|---|
| `access`, `overlay` | render the access document first, `sluisctl policy render <dir>`, and pass the result as `policy` |
| `directory.workspaces` (`backend`, `admin`, `secretName`, `secretKey`, `id`, `serve`, `syncGroups`) | `config.directory.workspaces` (`backend`, `admin`, `id`, `serve`, `syncGroups`, `keySecret: directory/<id>/key`), the key projected with a `secrets` entry `{name: directory/<id>/key, secretName, key}` |
| `config.clientSecretsDir` | none: `config.secrets` (`{source: file, root: /var/run/sluis/secrets}`, the default) |
| `config.exchange.clustersFile`, `.awsFile` | `exchange.clusters`, `exchange.aws` (rendered into the policy document), or `policy.exchange` |
| `config.github.*`, `config.slack.catalogueFile`, `config.exports` | `policy.exchange.github.owners`, `policy.apps.github.runnerTiers`, `githubApps.catalogue` / `slackApps` (or `policy.apps`), `policy.exports` |
| `controllerGithub.config.enabledOrgs`, `.catalogueFile`; `controllerSlack.config.enabledWorkspaces` | `policy.controllers.github.enabledOrgs`; the catalogue is the policy's; `policy.controllers.slack.enabledWorkspaces` |
| `config.policyDir`, each controller's `config.policyDir` | `config.policy.file` (`/var/run/access-issuer/policy/policy.yaml`; the controllers' under `/var/run/github-roster/policy`, `/var/run/slack-roster/policy`) |

## v1.61.2

Released automatically as a patch: last-known groups of identities are now kept in the shared State, fixing identity refusals after a cold start on Lambda or during a rollout when the directory cannot be vouched for.

### Fixed

- **Last-known groups live in the shared State, not in process memory, so a new instance finds what the previous one learned.**
  On Lambda or after a rollout that replaces a replica, a fresh instance had no last-known groups and refused every identity while the directory could not be vouched for. They now live under `issuer:held:<identity>` in the State with a lifetime of the hold window (groups and time only); memory is used only when there is no State or it fails.

## v1.61.1

Released automatically as a patch: the Lambda directory snapshot was refreshed on demand and every 15 minutes, fixing sign-in failures after 30 minutes.

### Fixed

- **On Lambda the directory snapshot was never refreshed after the first one, so
  sign-in failed about 30 minutes later ("the directory cannot be vouched
  for…"); it is now refreshed on demand and every 15 minutes by a schedule.**
  A request that finds the snapshot older than the refresh interval refreshes it
  (older than the freshness window: before answering, bounded to 20 seconds, and
  under the refresh lease so instances read a directory once between them), and
  `<prefix>-directory-refresh` invokes the http function with
  `{"kind":"refresh"}` every 15 minutes (`DirectoryRefresh.Rate`, `.Disabled`).

## v1.61.0

kms-wrapped signing (one symmetric KMS key, wrapped data key pairs, free rotation) becomes the AWS presets' default; recovery on Lambda is a Pulumi-generated password in SSM with an off switch; controllers lose access to config/*; GET / redirects to the console.

### Changed

- **A bare `adapters.signing: {adapter: kms}` no longer hides `signingKey.kms`.**
  Naming the adapter with no `settings` used to build it from nothing and stop the
  start with "the kms adapter needs keys and stateSecretFile", though
  `signingKey.kms` was written beside it. An override with no settings of its own
  now takes the settings of `signingKey.<adapter>` (`kms`, `kmsWrapped`) when that
  block is for the same adapter; settings given in the override still win whole.

### Added

- **`kms-wrapped` signing adapter, the default for the AWS presets.** Token
  signing moves from one remote asymmetric KMS key per algorithm to ONE symmetric
  key per estate and data key pairs: the issuer calls
  `kms:GenerateDataKeyPairWithoutPlaintext` (ES384 on `ECC_NIST_P384`, RS256 on
  `RSA_3072`), records the public key and the private key encrypted under the
  symmetric key in its key ring, and signs locally with the key it decrypts
  (`kms:Decrypt`, same encryption context
  `{purpose: sluis-signing, alg, kid}`) into process memory. Rotation is
  automatic and free: a new pair per algorithm every `rotateEvery` (24h), under
  the State lease `lease.signing-keygen:<alg>`, published for `prepublish` (15m,
  longer than a verifier caches the JWKS) before it signs and kept for `retain`
  (`lifetimes.token` + 5m) after it is replaced. Configure it with
  `signingKey.kmsWrapped: {keyId, stateSecretFile, algorithms, rotateEvery,
  prepublish, retain}` or `adapters.signing`. `aws-serverless` and `aws-hybrid`
  now default to it; `kms` (remote signing, non-extractable keys) stays
  selectable and unchanged, and so does `aws-eks`. **Trade-off:** wrapped keys
  are decrypted into memory, so a leaked signing role can forge offline for as
  long as the keys are published, and write access to the State (the key ring)
  is part of the trust boundary; the key policy must reserve the signing context
  to the signing roles (the library does it for the key it creates; a shared
  `KeyArn` needs the denial merged in). EdDSA is not supported yet. See
  [Signing on AWS](docs/explanation/signing-on-aws.md). A deployment that
  names `signingKey.kms` or `signingKey.file` keeps what it names.
- **Pulumi library: `LambdaArgs.WrappedSigning`.** One symmetric key (created
  with rotation enabled, protected, and a key policy that denies every principal
  but the signing roles any use of the signing context and holds those roles to
  `kms:EncryptionContext:purpose = sluis-signing` and no context keys beside
  `purpose`, `alg`, `kid`; an existing, possibly shared, `KeyArn` must carry the same denial,
  `WrappedKeyPolicyStatements`), a grant of only
  `kms:GenerateDataKeyPairWithoutPlaintext` and `kms:Decrypt` on it with those
  conditions to the http function, and no asymmetric keys unless
  `KeepRemoteSigningKeys`. The github and slack roles may not write the key ring
  items (with any signing). `KubernetesIdentityArgs.WrappedSigningKeyArn` grants the serve role the
  same.

- **Recovery on Lambda: a generated password, kept in SSM, with an off switch.**
  `sluispulumi.NewLambda` now generates the recovery password (`random.RandomPassword`,
  40 characters, no look-alikes), stores it as the SecureString
  `/sluis/private/config/recovery/password`, writes it to `/tmp/sluis/recovery-password`
  for the http function at cold start (the library names it as the new
  `recovery.passwordFile`) and exports its NAME as `RecoveryPasswordParameter`, never the
  value. `LambdaArgs.Recovery{Enabled *bool}` writes `recovery.enabled`; the default is
  **on**, because a first installation has no other way in; turning it off keeps the
  parameter and the file, refuses the sign-in with a message, and turning it on again
  rotates nothing. Before this a Lambda generated a different password at every cold
  start and printed it to the function's log. Every recovery attempt on the console,
  refused ones too, is now the audit event `roster.recovery.signed_in` (outcome
  `denied` or `failure` with the reason, actor `anonymous`); the catalogue is unchanged.
  The setup checklist's step "Turn off the recovery password" names the setting and the
  parameter. See [Recovery on Lambda](docs/how-to/recover-on-lambda.md).

- **`GET /` on the issuer's host redirects to `/console/`** (302, HEAD too) when the
  console is mounted, instead of the issuer's bare 404. Only the exact path: every
  other route keeps its handler.

## v1.60.0

Storage layout v2 moves DynamoDB items to a per-kind table partition, SSM configs to a /config prefix, and exports to Secrets; re-migrate from legacy.

### Changed

- **Layout change for the non-legacy adapters (storage layout v2): re-migrate from legacy.**
  What the `dynamodb`, `ssm` and Secrets-port adapters write is now clear and
  consistent ([storage layout](docs/reference/storage-layout.md)). A record has a
  kind and an id: DynamoDB `pk` is the kind (`directory`, `github-org`, `slack-workspace`,
  `issuer-token`, `lease`, `keyring`, ...) and `sk` its id (`google/C01ipl6j0`,
  `stable/acme`), where there were three conventions in one table; a credential
  is `/sluis/private/credentials/<kind>/<id>/<ref>`, where it was
  `/sluis/private/private/<key>/<ref>`; and the operator's secrets are
  `/sluis/private/config/...` (`config/oauth/client-id`, `client-secret`,
  `config/clients/<id>`, and the Pulumi-generated `config/issuer/state-secret`, now
  `StateSecretParameterName` under `ConfigParameterPrefix`). The mapping is one table
  (`internal/port/keys.go`); the legacy adapter and its key names are unchanged, and
  `sluis migrate` writes layout v2. **Consequence:** data written by an earlier
  non-legacy build is not found at its old place. There is no in-place relayout:
  empty the table and the `/sluis/private/credentials` prefix and migrate again from
  legacy. A deployment moves its `<NAME>=ssm:/sluis/private/oauth/...` environment
  mappings and `SLUIS_SECRET_FILES` entries to the `config/` names. IAM is unchanged.
  A directory workspace's State key gains its provider, `ws.dir.<provider>.<id>`
  (the record's backend), so that another identity directory is a provider and not a
  new kind.

### Added

- **Exports are written through the Secrets port.** With a Secrets adapter configured
  and `ports.export` unset, an export is the secret `export/<path>` (SSM
  `/sluis/export/<path>`), one JSON object of the export's properties, so ESO's SSM
  provider reads a property with `remoteRef.property`; the `exports` event on Lambda
  writes there. `ports.export: openbao` keeps working. The property names of each source
  are in the storage layout reference.

## v1.59.1

Released automatically as a patch: the declared OAuth client is read off-cluster, so the issuer on Lambda can open the Google Workspace directory.

### Fixed

- The OAuth client declared by `oauthClient.id` / `idFile` and `secretFile` / `secretEnv` is now read.
  Off-cluster (Lambda included) the issuer had no Google OAuth client, so the Google directory
  could not be opened and every sign-in was refused. Declaring it both ways (with `secretName`)
  is refused at start, as is a half-declared client or an unreadable file.

## v1.59.0

On Lambda, the GitHub and Slack controllers authenticate to the console with their role's AWS web identity token (a console audience of its own), and exports run on a schedule.

## v1.58.0

sluis runs on AWS Lambda (three functions from one zip) with DynamoDB state, SSM secrets, KMS token signing (ES384 and RS256), S3 blobs and SQS audit; adapters are chosen by name from presets; the sealer and the NATS adapter are removed.

- **The Lambda controllers authenticate to the console, and the exports keep running.**
  A controller's console client can present the function role's AWS outbound web
  identity token (`sts:GetWebIdentityToken`, one audience, cached until two minutes
  before expiry) instead of a projected ServiceAccount token: set
  `console.auth.aws.audience` in the controller's file, which must equal the issuer's new
  `console.awsAudience` (default `<issuerURL>/console`): the console and token exchange
  are two doors with two audiences, so a token minted for one is no proof at the other.
  The token is re-minted every four minutes, because the issuer refuses one whose `iat`
  is older than its `maxAge`. The console's workload door now accepts an AWS
  role as it does a cluster's ServiceAccount, and the policy's `aws` matchers decide
  what it may do (an `aws` matcher with no role admits every role of the account, and
  the issuer now warns at start). The `http` function also answers `{"kind":"exports"}` from a
  schedule: one pass of every export, each under its lease, failing the invocation if
  a copy could not be made. Kubernetes behaviour is unchanged. See
  [aws-lambda](docs/reference/lambda.md).

- **The Pulumi library creates a second signing key, RS256.** `NewLambda` makes an
  `RSA_3072` `SIGN_VERIFY` key beside the ES384 one (alias `SigningKeyRS256Alias`,
  default `alias/sluis-signing-rs256`; `DisableSigningKeyRS256` leaves it out) and
  outputs `SigningKeyRS256Arn`, `SigningKeyRS256ID` and `SigningKeyRS256Alias`. Only
  the `sluis-http` role may `kms:Sign` and `kms:GetPublicKey` with it.
  **Breaking:** `KubernetesIdentityArgs.SigningKeyArn` is now `SigningKeyArns`, a list.
  A caller's next apply creates the key.
  Also: an exports schedule (`Exports`: `<prefix>-exports`, default every 15 minutes,
  invoking `sluis-http` with `{"kind":"exports"}`, the function configurable);
  `sts:GetWebIdentityToken` on the github and slack roles (`WebIdentityAudience`
  restricts the audience); and `SLUIS_SECRET_FILES` per function (`SecretFiles`; the
  http function always lists the state secret at `/tmp/sluis/state-secret`).

- **KMS-backed RS256.** `signingKey.kms.additional: [{alg: RS256, keys: [...]}]` signs RS256
  with `RSA_2048`, `RSA_3072` or `RSA_4096` KMS keys beside the ES384 ones, for relying
  parties that need it (Kargo, EKS's OIDC provider); a client or resource pinned to
  `signing_alg: RS256` is signed by that ring. `RSASSA_PKCS1_V1_5_SHA_256` over a SHA-256
  digest, every signature verified before it is returned, kid the RFC 7638 thumbprint, each
  algorithm rotating on its own track by the same rules. The role needs `kms:Sign` and
  `kms:GetPublicKey` on both keys.

- **The Audit page's query URL no longer needs `audit.writer`.** `audit.queryURL` is its
  own setting, so the console's Audit page works with the `sqs` and `log` audit sinks, which
  have no receiver (the Lambda target state). The console already forwards the page's calls
  server-side, so the query host needs no CORS; a base URL with a path prefix
  (`https://audit.example.org/sluis`) is kept and the procedure path appended.

- **`sluis migrate` moves a whole installation to AWS.** By default it copies the State
  records, the secrets (to the Secrets port), the controllers' reports (to S3) and the
  issuer's key ring schedule, and leaves the issuer's sessions, refresh tokens, codes in
  flight and Index sets behind (`--with-sessions` copies them); `issuer:kms:*` is never
  copied. `--dry-run` validates sizes against the State and Secrets limits and key
  validity, prints a summary per concern and exits non-zero if anything would be
  refused (so does a real run, before it writes). `--kubeconfig`, `--kube-context` and
  `--namespace` read the legacy namespace from a workstation. The runbook has a "Cutover:
  migrating an installation" section.

- **Deployment guides and a generated adapter matrix.** `docs/guides/choosing-a-deployment.md`
  opens with the decision tree and modifiers, then the presets and support levels
  (`aws-hybrid` is the implemented, maintained path); `docs/guides/diy-adapter.md` is the
  fixed checklist for adding an adapter in a fork. `docs/reference/adapters.md` is
  generated from the adapter registry and the preset table by `just adapters-doc`;
  `just docs-check` fails when it is stale.

- **Audit records can go to an SQS queue.** The `audit` concern gains the `sqs` adapter
  (needs AWS; Kubernetes and Lambda runtimes): `adapters.audit: {adapter: sqs, settings:
  {queueURL, region, endpoint, timeout}}`, which the `aws-*` presets also name. The sink
  is now chosen from the resolved table: `connect` is the legacy `audit.writer` mapping
  and does not change, `log` keeps the log line only. On `sqs` there is no receiver, so
  the catalogue registration is skipped (no error, no retry; logged once) because the
  catalogue is delivered in the audit writer's package. A `block` action returns once
  SQS has the message; async actions never block a sign-in. On Lambda the trail is
  synchronous, waiting at most 3 seconds for the send and not waiting at all after a
  failed one, and `Trail.Flush` is there for a handler to call before it returns. The
  process needs `sqs:SendMessage` on the queue. See `docs/design/ports.md`.

- **sluis runs as AWS Lambda functions.** One arm64 `bootstrap` in one zip, deployed
  as three functions chosen by `SLUIS_ROLE`: `http` (the issuer and the console behind
  an API Gateway HTTP API, payload format 2.0, into the same `net/http` mux the server
  serves), and `github` and `slack` (one pass of one target per invocation, under the
  target's lease in DynamoDB). Events: `{"kind":"tick","target":"<id>"}` from an
  EventBridge Scheduler schedule and `{"kind":"run","target":"<id>"}` from the `http`
  function; a second invocation for a held target returns `contended` and succeeds.
  "Run now" is the new `invoke` adapter of the `trigger` concern (an asynchronous
  Lambda invoke; `Subscribe` is unused), and `eventbridge` is described for the
  `schedule` concern. The configuration is a file in the zip (`SLUIS_CONFIG_FILE`,
  default `/var/task/config/sluis.yaml`); an environment variable whose value is
  `ssm:/sluis/private/...` (or `/sluis/export/...`) is read from SSM Parameter Store at
  cold start, so a `*Env` secret is never in the file; `SLUIS_SECRET_FILES` (a JSON array of `{"parameter","path"}`) writes parameters to files under `/tmp` (0600) at cold start, so every `*File` setting works unchanged on Lambda. The release attaches
  `sluis-lambda_<version>_linux_arm64.zip` (`bootstrap` at its root, about 50 MB, 14 MB
  zipped; the Kubernetes binary is about 80 MB). The binary is built with
  `-tags lambda,lambda.norpc`, which leaves out client-go, NATS and Valkey, and
  `cmd/sluis-lambda/imports_test.go` fails on any of them. The controllers' configuration files gain the serve file's `platform`, `preset` and
  `adapters` keys (the Lambda controllers select `sqs` audit through them). **No change
  to the Kubernetes build.** See [aws-lambda](docs/reference/lambda.md).

- **Removed: sluis's own OTLP Lambda extension.** `cmd/sluis-lambda` used to be the
  extension installed as `extensions/access-roster-otlp`, deprecated in v1.57.0 for
  `truvity/observability`'s `otlp-lambda` layer. Nothing in an estate's GitOps repository or
  deploy repository deploys it, so the extension and the `sluis-lambda-layer_*` assets are
  gone and `cmd/sluis-lambda` is the function's composition root. The layer is the only
  telemetry layer; sluis ships none.

- **The `ssm` secrets adapter.** `internal/port/ssm` keeps `port.Secrets` in AWS SSM
  Parameter Store as SecureString parameters (the AWS-managed key, or `kmsKeyId`).
  A port path `p` is `/sluis/private/<p>`, and a path under `export/` is
  `/sluis/export/<rest>` (the root is the `root` setting), so a consumer's ESO can be
  granted `/sluis/export/*` alone. Versions are SSM's parameter versions; the tier
  is Intelligent-Tiering (advanced only for a value over 4 KiB). Creating ("only if
  absent") is atomic; `PutIfVersion` with a version is a read and a write, **not
  atomic** (last writer wins), safe because the target lease serialises the
  writers. The adapter is registered (AWS, kubernetes and lambda) and the secrets
  concern is now wired from the plan: a chosen adapter is `Set.Secrets`. With
  nothing chosen (every deployment today) nothing changes. IAM and the layout:
  `docs/design/ports.md`.

- **Removed: the NATS adapter and the KMS sealer.** Configs that name `ports.adapter: nats`,
  `ports.nats` or `ports.sealer` are refused. Sealing is retired entirely: the `Sealer`
  port, `internal/port/kmsseal`, the envelope code (`port.Seal`, `port.Open`) and the
  `sluis:binding` encryption context are gone, and so is `internal/port/nats` with its
  registration, chart test cases and documentation. A dynamic secret (a workspace
  credential, an App key, a link's token pair, the console's session key) is now
  written to the Secrets port under `private/<key>/<ref>`, and the record in State only
  names it; State never holds one. **Behaviour change for a non-`legacy` adapter:** it
  needs a Secrets adapter (`memory` has one; the `ssm` adapter follows), and start is
  refused naming it without one. A credential is written under a fresh ref and the
  one it replaces is removed, so a writer that loses a compare-and-swap never
  replaces the winner's secret. The chart accepts `replicas` above 1 only with
  `ports.adapter: dynamodb` now. `legacy` (which never sealed), Valkey, `Index`,
  `dynamodb`, `s3`, `memory`, file signing keys and the Export port are unchanged.
  The `nats` ADR records and the sealing ADR carry a superseded note.

- **Adapters are chosen by name, per concern.** `internal/port` gains an adapter registry:
  each adapter registers a descriptor (name, concern, what it needs of AWS, Kubernetes
  and OpenBao, the runtimes it works on, implemented or on request, and a factory from
  its settings), and a static catalogue lists the planned adapters, so the
  compatibility matrix is generated from the registry. The serve configuration takes
  an optional `platform` block (`aws`, `kubernetes`, `openbao`, `runtime`, `replicas`),
  an optional `preset` (`server`, `k8s-minimal`, `k8s-openbao`, `aws-serverless`,
  `aws-hybrid`, `aws-eks`) and per-concern `adapters`; an explicit adapter beats the
  preset, which beats the preset the answers derive. **No behaviour change without the
  new keys:** `ports.adapter` (`legacy` by default) and `ports.blob` map onto the same
  table. Start now refuses an adapter that needs a platform answer that is false, cannot
  run on the runtime (`legacy` on Lambda), is `memory` with more than one
  replica, or is planned and not built, then logs the resolved table once and exposes
  `sluis_adapter_info{concern,adapter}`. Also a `Secrets` port (`Get`, `Put`,
  `PutIfVersion`, `Delete`, `List`; exports under `export/`) with a `memory` adapter and
  a `porttest.RunSecrets` conformance suite. See `docs/design/ports.md`.

- **The Pulumi library deploys sluis on AWS Lambda.** Both estates (one on Kubernetes and
  one on AWS Lambda installations) move sluis to Lambda, and `deploy/pulumi` now expresses it
  ([guide](docs/reference/pulumi-library.md#lambda)):

  - `NewLambda` creates three functions from one released zip (`sluis-http`,
    `sluis-github`, `sluis-slack`; arm64, `provided.al2023`, handler `bootstrap`,
    no VPC), told apart by `SLUIS_ROLE`. The library adds the estate's
    configuration at `config/sluis.yaml` (`SLUIS_CONFIG_FILE`) and the catalogue
    files at `config/<name>` to the zip, so a change to either changes the package
    and redeploys.
  - **One IAM role per function.** All three get DynamoDB, S3, SSM under
    `/sluis/private/*` (read, write, delete), writes under `/sluis/export/*`,
    `sqs:SendMessage` on the audit ingest queue and logging; only `sluis-http`
    gets `kms:Sign`, `kms:GetPublicKey` and `lambda:InvokeFunction` on the
    controllers. `ExportReadPolicyJSON` is the policy a consumer's External
    Secrets Operator role attaches: read on `/sluis/export/*` and nothing else.
  - An HTTP API (payload 2.0) behind a custom domain with mutual TLS and a
    truststore the library uploads to a bucket of its own, with the default
    `execute-api` endpoint disabled unless `API.KeepDefaultEndpoint` is set (for
    the cutover's acceptance run).
  - One EventBridge schedule per GitHub organisation and Slack workspace,
    invoking the controller with `{"kind":"tick","target":"<id>"}` through a
    scheduler role that may invoke only those two functions.
  - The three functions' configuration files go in the package as
    `config/sluis.yaml`, `config/github.yaml` and `config/slack.yaml`
    (`Config`, `GitHubConfig`, `SlackConfig`), each function's `SLUIS_CONFIG_FILE`
    naming its own, as the Lambda app reads them. The issuer's OAuth-state secret is
    generated (32 random bytes, an SSM SecureString at
    `/sluis/private/issuer/state-secret`, output `StateSecretParameter`). The roles
    also get `dynamodb:UpdateItem` (the leases).
  - A new token-signing KMS key (`ECC_NIST_P384`, `SIGN_VERIFY`, alias default
    `alias/sluis-signing`), and an optional observability `otlp-lambda` layer.
  - **Breaking: the Sealer's KMS key is removed.** `NewStorage` no longer creates
    the key and its alias, and `StorageArgs.KeyAlias`, `KeyDescription`,
    `DefaultKeyAlias`, `Storage.KeyArn`, `KeyID`, `KeyAlias`, `StorageGrant.KeyArn`
    and `BindingContextKey` are gone; the roles no longer grant `kms:Encrypt` and
    `kms:Decrypt` on it. A caller's next apply therefore schedules the key's
    deletion (30-day window). The key and its alias are protected, so run
    `pulumi state unprotect` on both URNs first or the apply refuses.
    `PortsArgs.KeyID` is optional and an empty one renders no `sealer:` block.
  - `NewKubernetesIdentity` takes an optional `SigningKeyArn`, which only the
    serve role may sign with. EKS Pod Identity is otherwise as it was.

- **Sign with AWS KMS.** `signingKey.kms` (`keys`, `region`, `stateSecretFile`) makes the
  issuer sign ES384 tokens with `ECC_NIST_P384` KMS keys: the private key never leaves
  KMS. Exclusive with `signingKey.file`, which stays the default. Keys are listed oldest
  first and the last signs; the `kid` is the RFC 7638 thumbprint, rotation is appending a
  key and rides the existing `pollInterval`, `activationDelay` and `overlap`. The role
  needs `kms:Sign` and `kms:GetPublicKey`; signatures are counted by key in
  `access_issuer.kms_signatures`. See
  [Signing with AWS KMS](docs/how-to/sign-with-aws-kms.md). The chart
  does not render this yet.

- **The GitHub and Slack controllers roll safely.** The chart fixed each controller at
  one replica with `strategy: Recreate`, so a release whose pods crashed at start
  (sluis 1.57.0, 2026-10-04) deleted the running controller first and left it down for
  about 15 minutes, while the `serve` Deployment kept its old pods. Now:

  - `controllerGithub` and `controllerSlack` take `replicas` (default 1), `strategy`
    (default `RollingUpdate`, `maxUnavailable: 0`, `maxSurge: 1`), `minReadySeconds`
    (default 10) and `podDisruptionBudget` (rendered when `replicas` is above 1).
    A new pod must be Ready before an old one is removed. **Behaviour change:** a
    deployment that relied on `Recreate` sets `strategy.type: Recreate`; with the
    `legacy` adapter the new pod's first pass can overlap the old pod's last for a few
    seconds.
  - The controllers serve `/healthz` and `/readyz` on `probes.address` (default
    `:7070`), and the chart probes them. Ready means the process finished starting:
    the policy loaded, the stores open, the audit catalogue accepted.
  - More than one replica is refused at render unless the controller's
    `ports.adapter` is `nats` or `dynamodb`. With `legacy` or `memory` the tick leases
    are in each pod's memory and every replica would act on every target.
  - See [the runbook](docs/how-to/check-health.md#2-a-rollout-that-does-not-complete)
    and [high availability](docs/explanation/high-availability.md#the-controllers-in-the-one-process).

## v1.57.1

Released automatically as a patch: the roster audit catalogue bumped to 1.7.0 after its document changed in 1.57.0.

- **Fixed: the audit catalogue is now version 1.7.0.** v1.57.0 renamed the
  product through every file, including `internal/audit/catalogue/roster.yaml`
  and its released fixtures, so the document changed (three descriptions say
  `sluis` where they said `access-roster`) while its version stayed `1.6.0`.
  An audit installation refuses a changed document under a registered version,
  and every sluis process stopped at start with `roster version 1.6.0 is
  already registered with a different document`. The source stays `roster`;
  renaming it is catalogue 2.0.0. The released fixtures 1.0.0 to 1.6.0 are
  restored to the bytes of v1.54.0, and `TestAReleasedFixtureIsNeverRewritten`
  pins each fixture's SHA-256 in `testdata/released/SHA256SUMS`, so a sweeping
  edit can no longer rewrite a released document and its fixture together.

## v1.57.0

- **Deprecated: the Lambda extension layer (`sluis-lambda-layer`).** The
  extension moved to `truvity/observability`
  (`github.com/truvity/observability/lambdaext`, v0.47.0), which releases it as
  `otlp-lambda-layer_<version>_linux_<arch>.zip`. This release still builds
  `sluis-lambda-layer_<version>_linux_<arch>.zip`, from that package, so a
  consumer can switch; it is dropped in the release after this.
  `internal/lambdaext` is removed from this module. The protocol is unchanged,
  and `SLUIS_*` settings work alongside the `ACCESS_ROSTER_*` ones.

- **Breaking: renamed to sluis.** The product is now **sluis** and the
  repository `truvity/sluis` ([ADR 0035](docs/decisions/0035-renamed-to-sluis.md)).
  This ships as a minor release (v1.57.0, no `/v2`) by
  [ADR 0007](docs/decisions/0007-breaking-changes-inside-1x.md). It lands only
  after the GitHub rename, because the new module path resolves only then.
  What a consumer changes:

  - **Go module path:** `github.com/truvity/access-roster` becomes
    `github.com/truvity/sluis` (packages `policy`, `identity`, `tokens`; the
    `deploy/pulumi` library already was `github.com/truvity/sluis/deploy/pulumi`).
    Change every import and `go get github.com/truvity/sluis@v1.57.0`.
  - **Images:** `ghcr.io/truvity/access-roster/access-roster` becomes
    `ghcr.io/truvity/sluis/sluis`, and `ghcr.io/truvity/access-roster/resource-proxy`
    becomes `ghcr.io/truvity/sluis/resource-proxy`. The server binary is `sluis`
    (it was `access-roster`), and the chart mounts its files at `/etc/sluis/`.
  - **Chart:** `oci://ghcr.io/truvity/charts/access-roster` becomes
    `oci://ghcr.io/truvity/charts/sluis`; the chart is named `sluis` and its
    helpers `sluis.*`. `nameOverride` and `fullnameOverride` are honoured as
    before: an installation that sets them (the estate sets both to
    `access-issuer`) keeps every object name and every Deployment selector.
    An installation that relied on the defaults now has to say what it relied
    on (`fullnameOverride`, `nameOverride`, and `config.release: access-roster`
    for the store prefix), because the default `release` is now `sluis`.
  - **CLI:** `accessctl` is now **`sluisctl`**; release assets are
    `sluisctl_<version>_<os>_<arch>` and the Nix flake `sluisctl`. `accessctl`
    is kept as an alias for one or two releases: `accessctl_*` archives and an
    `accessctl` flake carry the same program, which prints a deprecation notice
    on stderr. Its settings are `SLUISCTL_*` (the `ACCESSCTL_*` names still
    work). The OIDC client id `accessctl`, the config and cache directory, the
    kubeconfig user names and the managed known-hosts file are unchanged.
  - **Release assets:** `sluis_<version>_checksums.txt`,
    `sluis_<version>_<os>_<arch>` for the service, and the deprecated Lambda
    layer `sluis-lambda-layer_<version>_linux_<arch>.zip`.
  - **KMS sealing context:** the encryption-context key is `sluis:binding`
    (it was `access-roster:binding`). Nothing is sealed anywhere yet, so
    nothing needs re-wrapping; a KMS grant that names the key must say
    `sluis:binding`.
  - **OpenBao names:** `access-roster-backup/<bundle>` becomes `sluis-backup/<bundle>` and the role `access-roster-writer` becomes `sluis-writer` in examples and defaults; the estate sets its paths explicitly. Neutral paths (`slack-apps/*`, `arc/*`, ...) are unchanged.
  - **Environment:** the Lambda extension reads `SLUIS_*` first and falls back
    to `ACCESS_ROSTER_*` (both work, `SLUIS_*` wins); `sluisctl` does the same
    for `SLUISCTL_*` and `ACCESSCTL_*`
    ([aws-lambda](docs/reference/lambda.md),
    [sluisctl](docs/reference/sluisctl.md)).
  - **npm:** `@truvity/access-roster` becomes `@truvity/sluis`. The release also
    publishes the same build as `@truvity/access-roster` for one or two
    releases, described as deprecated.
  - **Schemas:** the `$id` of the configuration schemas is
    `https://truvity.github.io/sluis/schemas/v1/config/...`.

  What did **not** change, on purpose: the issuer URL and every OIDC client id;
  the group names `all:access-roster:*` and the `groups: [access-roster]` thing;
  the Kubernetes label and annotation keys `access-roster.truvity.github.io/*`;
  `token-source: access-roster`, the `ACCESS_ROSTER_ISSUER` variable and the
  grants preset `access-roster`; the audit source `roster`; metric, alert and
  dashboard names and OTEL `service.name` (they change with the dashboards, at
  B5). The full list is in the ADR.

## v1.56.1

Released automatically as a patch by a workflow_dispatch of Auto Release; it carries a feature (the Pulumi library) that would normally be a minor.

- **A Pulumi library for the AWS part of an installation:
  `github.com/truvity/sluis/deploy/pulumi`.** A Go module of its own (Pulumi is
  not in the root's dependency graph; the release tags it
  `deploy/pulumi/vX.Y.Z`) with three components: `Storage` (the blob bucket, the
  Sealer's KMS key and alias), `State` (the DynamoDB table of the DynamoDB
  adapter) and `KubernetesIdentity` (one EKS Pod Identity role per process:
  serve, the GitHub controller, the Slack controller, each with the storage and
  table grants and nothing else), and `RenderPorts` for the `ports:` block,
  validated in its tests against the binaries' schemas. The KMS grant admits the
  encryption-context key `sluis:binding` only, so it needs a service release
  whose Sealer sends that key. Tested with Pulumi's mocks (`just pulumi-test`).
  [docs/deployment/aws.md](docs/reference/pulumi-library.md).

## v1.56.0

- **A longer absolute session for a read-only resource: `absolute_cap` and
  `read_only` on a policy resource.** The 24-hour absolute limit of
  [ADR 0001](docs/decisions/0001-sessions-and-an-absolute-limit.md) made every
  connector sign in again daily. A resource that declares `read_only: true` may
  now carry `absolute_cap` up to `168h`
  ([ADR 0033](docs/decisions/0033-a-longer-absolute-limit-for-read-only-resources.md)).
  A refresh chain's limit is the shortest among the resources it was used for,
  the client's own audience and an uncapped resource counting as
  `lifetimes.absolute`; it is enforced at refresh, at a silent `/authorize` and
  in the access token's `exp`. Refused: a cap above 168h, zero or negative, and
  (at start) one above `lifetimes.absolute` without `read_only`. Sign-out,
  removal and refresh-token reuse end the chain as before, and a cap withdrawn
  from the policy shortens it at its next refresh. `lifetimes.refresh` (default
  `12h`) still bounds a chain's idle time, so the seven days need it raised to
  match. Nothing changes for a resource that sets neither field.

- **Exports: the service copies the secrets it keeps into OpenBao itself.** With
  `ports.adapter` other than `legacy` the service writes no Kubernetes Secret, so the
  External Secrets `PushSecret`s that copied them into OpenBao (a Slack App's bot
  token for Alertmanager, the runner Apps for ARC, seven recovery bundles) had
  nothing to read. A new port, `port.Export` (`Put` in `replace` or `patch` mode,
  and `Delete`), with a memory adapter and an OpenBao KV version 2 adapter
  (`internal/port/openbao`: Kubernetes or JWT login inside each namespace, a JSON
  merge patch for per-property copies, nothing written when the key already holds
  the data), and `internal/exports`, which runs the copies. `ports.export` names
  the OpenBao and how to log in; `exports:` lists what is copied (`slack-app`,
  `github-app`, `runner-app`, or one of the seven `bundle`s) and where, under the
  property names the PushSecrets wrote, and the seven bundles are the Secrets'
  entries byte for byte. Validated at start: an unknown source, an App or tier the
  deployment does not declare, and two exports that would write one key stop the
  service. Each export is made once at start, again on a change to its source and
  every hour, under a lease so only one replica writes, retried with backoff, and
  never on the path of a sign-in, a tick or a console action: an OpenBao that is
  down changes nothing live. Metrics `access_roster.export.attempts`, `.duration`,
  `.last_success_timestamp` and `.contended`; alerts `AccessRosterExportFailing` and
  `AccessRosterExportStale`; a dashboard row. The chart mounts the CA
  (`exports.openbao.caBundle`) and projects the token the login presents
  (`exports.openbao.token.audience`), refusing a `caFile` or `tokenFile` anywhere
  else. The `push` values are kept for the `legacy` storage and are deprecated. The
  OpenBao role needs `read`, `create`, `update` and `patch` on
  `kv/data/<prefix>/*`. Marked 🧪 in the capabilities. Installations without
  `exports` see no change. See
  [docs/decisions/0034](docs/decisions/0034-exports-go-to-openbao-directly.md) and
  [docs/reference/configuration.md](docs/reference/exports.md).

## v1.55.0

This release adds a DynamoDB adapter for State, the session index and the Trigger. Installations on `ports.adapter: legacy` see no change.

- **A DynamoDB adapter for State, the session index and the Trigger:
  `ports.adapter: dynamodb`.** `internal/port/dynamodb` keeps them in one table
  (`pk` the key's first segment, `sk` the whole key, TTL on `expires`) with the
  platform's credentials, so a Kubernetes deployment on AWS, or a Lambda one, shares
  State across replicas as the NATS adapter does. Writes are conditional on a
  random 64-bit revision (an identical rewrite changes it and a delete-and-recreate
  cannot be mistaken for no change), reads are consistent and filter expiry
  themselves, and a `Create` takes an expired record at once. `Watch` and the
  Trigger poll (one second); the lambda-invoke Trigger of ADR 0029 stays a separate
  adapter. `ports.dynamodb.{table,region,endpoint,create}`: `create` is off by
  default so production binds to the table the infrastructure code made. It passes
  the whole conformance suite, with the other ports' assertions skipped and named,
  over a fake in `go test ./...` and over LocalStack in the `s3` CI job, which fails
  on any other skip, and `access-roster migrate` copies memory into it and back.
  Marked 🧪 in the capabilities: not yet run against AWS. Installations on any other
  adapter see no change. See
  [docs/design/ports.md](docs/reference/port-adapters.md#the-dynamodb-adapter).

## v1.54.0

This release adds the NATS JetStream KV, S3 Blob and KMS Sealer port adapters; the domain stores on the ports, with secrets sealed per key; GitHub link refresh as one compare-and-swap; the Slack Connect hand-off and the `users.info` cache; the `access-roster migrate` tool, with sessions copied and lifetimes kept; and the chart's `telemetry.otlp` value. Installations on `ports.adapter: legacy` see no change.

- **The chart sets the OpenTelemetry environment: `telemetry.otlp`.** With
  `telemetry.otlp.endpoint` set, every pod of `renders: app` gets
  `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL` (`protocol`,
  `http/protobuf` by default), an `OTEL_SERVICE_NAME` of its own (`access-issuer`
  for `serve`, `github-roster`, `slack-roster`, the names the binary already
  uses) and each `extraEnv` entry. Without an endpoint nothing is rendered, so the
  installed objects are unchanged (policy ADR 0006). The chart refuses an endpoint
  that is not an http(s) URL, an `extraEnv` name not starting with `OTEL_`, and
  `OTEL_EXPORTER_OTLP_ENDPOINT` in `extraEnv`. Without it the alerts and the
  dashboard had no data. See
  [docs/reference/telemetry.md](docs/reference/telemetry.md#wiring-it-with-the-chart).

- **`access-roster migrate --from <config> --to <config>`** copies the State from
  one storage to another (ADR 0031): the first step of the move, ConfigMaps,
  Secrets and Valkey to NATS, and the rollback the other way round. Each end is a
  `serve` configuration file. Every domain store (workspaces and credentials,
  GitHub organisations, Apps and links, Slack workspaces, Apps and channel
  records, confirmations and requests for a pass, the console's session key) is
  copied through its business interface, so a secret is sealed under the
  destination's Sealer; the issuer's sessions, refresh tokens, SSO and keyring
  schedule are copied with the lifetime each has left, so nobody signs in again.
  A plan runs first and writes nothing: a destination value that differs fails
  the run naming the key unless `--overwrite`; `--dry-run` stops there. A run that
  writes needs `--i-have-stopped-writers`. It then reads both sides again and
  compares every item, and prints a JSON report of counts and keys (never a
  value), also to a Blob with `--report-blob`. Re-running completes a partial
  copy. See [docs/operations/migrate.md](docs/how-to/migrate-state.md). Adds the
  optional `port.StateExporter` and `port.IndexExporter`, a `Restore` on the two
  GitHub link stores and `PutSessionKey`, used only by the migration. `--backup`
  to a file is a follow-up.

- **The domain stores on the ports.** With `ports.adapter` set to `nats` or
  `memory`, the directory workspaces and their credentials, the GitHub
  organisations, the link App, runner and catalogue Apps, people's GitHub links,
  the Slack workspaces, Slack Connect and channel records, the operators'
  confirmations and requests for a pass and the console's session key are kept in
  State (`ws.`, `gh.org.`, `gh.link.`, `app.`, `rec.`, `gate.`), a record and its
  secret in one item, every secret sealed with the item's own key as the binding.
  `ports.adapter: legacy`, the default, keeps the ConfigMap and Secret stores
  unchanged. A Sealer is required: the start is refused, naming `ports.sealer`,
  without one (so `nats` needs `ports.sealer`). The controllers read the same
  records from the State instead of the mounted files. See
  [docs/design/ports.md](docs/explanation/domain-stores.md).

- **A GitHub link is one item with one compare-and-swap refresh.**
  `gh.link.<account>` holds the link and its sealed token pair; the refresh
  marker is the claim on the single-use refresh token, so two replicas never
  spend it twice, and the replica that loses uses the winner's pair. A claim that
  spans accounts is steps with a marker that the next read finishes. Links are
  permanent keys (a link outlives its tokens).

- **The Slack Connect hand-off and a `users.info` cache on the State.** The
  host's tick writes `share.<host>.<channel>` (which asks the guest's runner to
  tick), the guest's tick marks its side accepted; 14 days while pending, 7 once
  accepted. Who a channel's member is, is cached 24 hours in
  `cache.slack.user.<workspace>.<id>` and counted by
  `slack_roster.user_cache{result=hit|miss}`. The shared inputs cache stays in
  memory. Additive: nothing changes with the default adapter.

- **The NATS JetStream adapter of the State port.** `ports.adapter: nats`
  (with `ports.nats`: `url`, `bucket`, `replicas`, `tokenFile` or `credsFile`,
  `caFile`, `create`) keeps State, the transitional session Index and the
  Trigger in one JetStream KV bucket, so a lease is exclusive across replicas
  and a notification crosses processes. Revisions are the stream sequence, so a
  rewrite of identical bytes changes it and a record that went A, B, A is
  detected. Expiry is judged on read; with nats-server 2.11 or later the bucket
  also reaps each record by its own TTL. Blob, Sealer and Identity are the legacy
  adapter's unless `ports.blob` and `ports.sealer` name the S3 and KMS adapters. The conformance suite passes against an
  embedded nats-server, a single node and a three-node cluster. Additive: the
  default adapter is unchanged. See
  [docs/design/ports.md](docs/explanation/ports.md#the-nats-adapter).

- **S3 Blob and KMS Sealer adapters.** `ports.blob: {adapter: s3, s3: {bucket,
  prefix, region, kmsKey, endpoint, pathStyle}}` keeps the status reports and
  directory snapshots in S3 (`WriteIfVersion` is an `If-Match` on the ETag), and
  `ports.sealer: {adapter: kms, kms: {keyId, region, endpoint}}` wraps the data
  keys of sealed secrets with AWS KMS (the binding is the EncryptionContext; no
  unwrapped key is cached). Each replaces one port of whatever `ports.adapter`
  brings, so the legacy State with an S3 Blob is valid. Credentials are the
  platform's; no key is configured. Additive: an installation that sets neither
  runs what it did. Both are experimental: they pass the conformance suite on
  LocalStack (`just test-s3`, and the `s3` CI job, which fails on a skipped
  test). See [docs/design/ports.md](docs/explanation/ports.md#the-s3-blob-and-the-kms-sealer).

- **The access document.** A policy layer may be written as the lists an
  installation derives from its access matrix (`access`) plus the rows that are
  its own (`overlay`); `policy.ParseAccess` reshapes it into the layer, and
  `LoadDeclared` reads such a file beside ordinary layers. The chart takes the
  same two values (`access`, `overlay`), renders them to `access.yaml` in the
  policy ConfigMap, checks `enabledOrgs` and `enabledWorkspaces` against them
  and projects the secrets of the clients they declare. Additive: `policy` is
  unchanged, and an installation that sets neither value renders what it did.
  See [docs/reference/policy.md](docs/reference/policy-bindings.md#the-access-document).

## v1.53.0

This release adds traces and metrics through an exporter allowlist, the chart's `alerts` and `dashboards` render modes, per-target ticks with the `tick` command (which builds on v1.52.4's single-binary, single-chart, and configuration-file consolidation), and preparation for the audit SDK module bump that will land in a later commit before the tag.

- **Traces, issuer and controller metrics, and the chart's `alerts` and
  `dashboards` modes.** Telemetry is still only `OTEL_*`, exported only when a
  collector is named; the contract and every signal are in
  [docs/reference/telemetry.md](docs/reference/telemetry.md).

  - **Traces.** A server span per request on the issuer's listener, named for a
    fixed route and never the path; Connect spans on the console's and the
    session service's handlers and on the controllers' clients of the console
    (the trace continues across); a span per tick (target kind and target) and
    per storage port call, the latter only inside a trace already recorded.
    Every span leaves through an allowlist exporter: attributes outside the list
    (the client's address, the raw path, the user agent), every event, link
    attribute and status text are dropped, and a test plants personal data in
    each place and asserts none leaves. **The sampler default, when
    `OTEL_TRACES_SAMPLER` is unset, is parent based `always_on`**; truvity/audit
    keeps a tenth. Which is right is not decided: it is one function, and the
    environment overrides it.
  - **Metrics.** `access_issuer.http.requests` and `.request.duration` by route
    and status class, `access_issuer.tokens.issued` by declared client and grant,
    `access_issuer.login.failures` by reason and `.login.successes` by method,
    `access_issuer.reuse_detected` by kind,
    `access_issuer.signing_key.active_since_timestamp`, and
    `access_issuer.signing_keys_published` now **by algorithm** (it was one
    unlabelled gauge written by every ring). For the controllers,
    `access_roster.ticks`, `.tick.duration` and `.tick.last_success_timestamp`
    by kind and target, `access_roster.leases.{acquired,contended,lost,held}`,
    and `access_roster.port.operation.duration` by port, operation and outcome
    (its `conflict` count is the compare-and-swap conflicts). The rate-limit
    metrics are kept. Nothing is labelled by person or group; a client id is
    bounded by the policy (an undeclared one is `other`) and a target by the
    policy's own declaration.
  - **Chart: `renders: app|alerts|dashboards`.** `app`, the default, renders
    byte for byte what it did. `alerts` renders only a VMRule (or, with
    `alerts.format: prometheusrule`, a PrometheusRule) of ten rules, each with
    its threshold's reason in a comment and a runbook entry, with
    `alerts.ruleLabels` on every rule; `dashboards` renders only the sidecar
    ConfigMaps of an "access-roster overview - $cluster" dashboard. Both are
    installed as a release of their own and validate nothing of the service.
  - **Held by CI.** `just telemetry` (a new job, and part of `check`) regenerates
    the dashboard and runs truvity/observability's `dashboardlint` over it, and
    unit-tests every rule with `vmalert-tool`, each with a case that fires it
    and one that must not. Goldens cover both modes.

- **A flaky Slack controller test.** `TestAChangedCredentialRunsAPassWithoutWaitingForTheInterval`
  read the second workspace's report before its own tick had published it; it
  now waits for it.

- **Audit SDK module upgrade.** Migrate from `github.com/truvity/audit v0.3.1`
  to `github.com/truvity/audit/sdk v0.6.1`, updating all imports to the new
  module path. The audit catalogue and API remain compatible with the new
  version.

## v1.52.4

- **Breaking (released automatically as a patch):** this release replaces the three
  binaries (`access-issuer`, `github-roster`, `slack-roster`) and the
  `access-issuer` chart with one `access-roster` binary, image and chart, and
  moves configuration from environment variables and chart values to one
  validated file per binary. It is a breaking change shipped as a patch release;
  the migration steps are in
  [docs/reference/configuration.md](docs/how-to/migrate-from-environment-variables.md)
  and
  [the chart migration](docs/how-to/migrate-from-the-access-issuer-chart.md).

- **Breaking: each binary is configured by one validated file, in place of
  environment variables, and the chart passes it through.**
  [0032](docs/decisions/0032-one-configuration-file-one-binary-one-chart.md)
  (its configuration-file half; the single binary and the single chart are the
  next entry). `access-issuer`, `github-roster` and `slack-roster` take
  `--config <file>` and nothing else but `--version` and `--help`. The file is
  validated against a committed JSON Schema before anything starts
  (`schemas/config/access-issuer.schema.json`, `github-roster.schema.json`,
  `slack-roster.schema.json`, generated by `just config-schemas` and embedded in
  the binaries), so an unknown key, a missing required key or a value of the
  wrong type is a start-up error that names the path to it, and a misspelt
  value in a chart is a failed `helm template` rather than a container that
  ignores it. The key for each old variable is in
  [docs/reference/configuration.md](docs/how-to/migrate-from-environment-variables.md).

  - **Secrets are the one thing the environment adds, and only the ones the
    file names.** A key ending in `Env` (`valkey.passwordEnv`,
    `oauthClient.secretEnv`, `adminPasswordEnv`) holds the name of the
    variable; the process reads exactly those, and an unset one refuses to
    start. A secret in the file is refused: there is no key for it, and a URL
    or address with a password in it does not match the schema. A signing key,
    the OAuth client's files, the policy, the cluster and AWS federation files
    and the catalogues are keys holding paths.
  - **Telemetry is only `OTEL_*`.** `telemetry.otlpEndpoint` is removed from the
    chart; the endpoint is set on the pods by the platform.
  - **The old environment is refused, not ignored.** A variable the binaries
    used to read (`ISSUER_URL`, `VALKEY_PASSWORD`, `ENABLED_ORGS`, and the rest)
    that is still set stops the process at start and names the key that replaces
    it. `NAMESPACE` is now read from the pod's mounted service-account
    namespace, and the audit instance name is the pod's hostname.
  - **The chart renders each component's `config:` as it stands** into a
    ConfigMap (`<release>-config`, `<release>-github-roster-config`,
    `<release>-slack-roster-config`), mounts it, and `values.schema.json` embeds
    the schema its binary uses. Platform values stay values: images, replicas,
    resources, the route, the signing key's Certificate, `policy`, `exchange`,
    `directory.workspaces`, `githubApps`, `slackApps`, `audit.token` and the
    new `secretEnv` and `secretMounts`. The old env-shaped values (`issuerURL`,
    `listeners`, `logLevel`, `lifetimes`, `valkey`, `recovery`, `oauthClient`,
    `github`, `githubRunnerApps`, `telemetry`, `directory.store`,
    `directory.freshness`, `signingKey.rotation`, `githubRoster.actsIn`, ...)
    are removed, not aliased, and refused at render. A Go test holds every
    rendered ConfigMap to the values' `config` and to its binary's schema.
  - **The chart checks the config against what it renders.** Because the config
    is not computed, the chart refuses one that disagrees with the chart itself,
    and prints the value to write: `release` must be the release's full name; a
    path (`policyDir`, `signingKey.file`, `exchange.clustersFile`,
    `overlayFile`, a catalogue file, `clientSecretsDir`, `audit.tokenFile`, a
    controller's directories) must be where the chart mounts it, and must be
    unset where it mounts nothing; a controller's `consoleURL` must be this
    release's Service; with `route.host`, `publicURL` and `publicRootURL` must
    be the route's and `secureCookies` may not be `false`; `recovery.enabled`
    needs `inCluster`, a service account and an audience. An installation whose
    Helm release is not called `access-issuer` sets `config.release` to
    `<release>-access-issuer`; the chart says so.
  - **Every safety refusal is kept**, in the chart, the schema or the binary:
    the table is in the pull request. `tests/invalid/access-issuer/` holds a
    fixture for each chart refusal, and the removed values have one each that
    shows they are refused.

  Migrating, for an installation with a Valkey, a sign-in client and a
  controller:

  ```yaml
  # before
  issuerURL: https://access.example.com
  logLevel: debug
  lifetimes: { token: 30m }
  valkey:
    address: valkey.access-issuer.svc:6379
    passwordSecret: { name: valkey-password, key: secret }
  oauthClient:
    secret: { name: google-client }
  recovery: { enabled: true, serviceAccountName: access-recovery, audience: access-recovery }
  route: { host: access.example.com }
  githubRoster: { enabled: true, interval: 5m, actsIn: [acme] }
  telemetry: { otlpEndpoint: http://otel-collector.observability.svc:4318 }
  ```

  ```yaml
  # after
  config:
    issuerURL: https://access.example.com
    publicRootURL: https://access.example.com        # with route.host
    publicURL: https://access.example.com/console    # route.host plus console.mount
    log: { level: debug }
    lifetimes: { token: 30m }
    valkey:
      address: valkey.access-issuer.svc:6379
      passwordEnv: VALKEY_PASSWORD
    oauthClient:
      secretName: google-client
      idFile: /var/run/access-issuer/oauth-client/client-id
      secretFile: /var/run/access-issuer/oauth-client/client-secret
    recovery: { enabled: true, serviceAccount: access-recovery, audience: access-recovery }
  secretEnv:
    - { name: VALKEY_PASSWORD, secretName: valkey-password, key: secret }
  secretMounts:
    - { secretName: google-client, mountPath: /var/run/access-issuer/oauth-client }
  route: { host: access.example.com }
  githubRoster:
    enabled: true
    config:
      consoleURL: http://access-issuer.access.svc:8080/console   # this release's Service
      interval: 5m
      enabledOrgs: [acme]
  # telemetry: set OTEL_EXPORTER_OTLP_ENDPOINT on the pods, outside the chart
  ```

- **Breaking: one binary, `access-roster`, one image and one chart, in place of
  three of each.**
  [0032](docs/decisions/0032-one-configuration-file-one-binary-one-chart.md)
  (its second half; `tick` is a later change, with
  [0029](docs/decisions/0029-ticks-per-target-under-a-lease.md)'s leases).
  `access-issuer`, `github-roster` and `slack-roster` are `access-roster serve`,
  `access-roster controller github` and `access-roster controller slack`, each
  still `--config <file>` and nothing else but `--version` and `--help`;
  `access-roster migrate` is reserved by
  [0031](docs/decisions/0031-a-generic-migration-tool.md) and prints that it is
  not yet available. Each command's file has its own schema:
  `schemas/config/serve.schema.json`, `controller-github.schema.json` and
  `controller-slack.schema.json` replace the three above. **Removed, not kept as
  an alias:** the three `cmd/` mains, the images
  `ghcr.io/truvity/access-roster/access-issuer`, `/github-roster` and
  `/slack-roster`, and the chart `oci://ghcr.io/truvity/charts/access-issuer`;
  none is published again. **Published instead:**
  `ghcr.io/truvity/access-roster/access-roster` and
  `oci://ghcr.io/truvity/charts/access-roster`. Migrating an installation
  (the full walk-through, with every name that changes, is in
  [docs/reference/configuration.md](docs/how-to/migrate-from-the-access-issuer-chart.md)):

  1. Be on the config-file form of the values (the entry above).
  2. Rename the values `githubRoster` to `controllerGithub` and `slackRoster` to
     `controllerSlack`, and delete `githubRoster.image` and `slackRoster.image`:
     a controller runs the chart's one `image`. If `image.repository` is set,
     point it at `ghcr.io/truvity/access-roster/access-roster`.
  3. Add `nameOverride: access-issuer` and `fullnameOverride: <the release's old
     full name>`. The chart's name is part of every object's name, so without
     them every object is renamed, the signing key's Secret is a new key and the
     service starts on an empty store, because `config.release` (which must be
     the full name) names the objects it writes. With them nothing is renamed and
     no Deployment selector changes. Write `config.release` and each
     controller's `config.release` out if they were unset: the default is now
     `access-roster`, not `access-issuer`.
  4. Point the chart reference (`helm`, Argo CD, Flux) at
     `oci://ghcr.io/truvity/charts/access-roster` at this version, run
     `helm template`, and roll out.
  5. Change anything outside the chart that ran a binary or an image by name.

  The container names, the `app.kubernetes.io/component` labels and where the
  config file is mounted change (`serve`, `controller-github`,
  `controller-slack`; `/etc/access-roster/...`). Unchanged: the controllers'
  object names (`<full name>-github-roster`, `-slack-roster`), every path the
  chart mounts under `/var/run/...`, and the telemetry service names.

- **The storage ports of `docs/design/ports.md` exist in the code, behind
  today's storage, and the apps depend on them.** No data moves, the same
  objects and keys are written, and nothing behaves differently. A new package,
  `internal/port`, defines `State`, `Blob`, `Trigger`, `Sealer` and `Identity`
  (the audit sink stays `audit.Recorder`); `internal/port/memory` implements
  all of them for tests and the demonstration; and `internal/port/legacy` is a
  temporary adapter that implements them by reaching the ConfigMaps, Secrets and
  Valkey keys the service writes today, byte for byte (`gh.org.<org>` is the
  `<release>-github-orgs` entry, `snapshots/<workspace>` and `lease.<kind>:<workspace>`
  are the Valkey keys the hub has always used, `reports/github/` and
  `reports/slack/` are the two status ConfigMaps). A key of the layout that has
  no object of its own today (`ses.`, `sid.`, `ws.`, `gh.link.`, ...) is refused
  as unsupported rather than written somewhere else. The gaps are listed in
  [design/ports.md](docs/reference/capabilities.md): a revision is
  a digest of the stored bytes, `Watch` polls, and the legacy `Sealer` refuses.
  The issuer's logins in progress, the hub's snapshots and refresh lease, the
  controllers' reports and the cluster `TokenReview` now go through the ports;
  the ConfigMap and Secret domain stores of a connected workspace, an
  organisation's credential and a person's link stay behind their own
  interfaces until the data moves. A new `ports.adapter` key (`legacy`, the
  default, or `memory`) in the `serve` and controller files chooses the adapter;
  `memory` keeps all state in the process and is refused with `store: kubernetes`
  or `valkey.address`. `internal/port/porttest` is the conformance suite (CAS
  races, TTL visibility, lease takeover, prefix paging, revisions, watch, limits,
  blobs, sealing, identity) that both adapters pass, with each assertion the
  legacy adapter cannot meet skipped by name and reason. A test fails the build
  if a business package imports `internal/kube`, `internal/valkey` or the legacy
  adapter. `valkey.Snapshots` is gone: its encoding moved to the hub, unchanged.

- **Each reconciler works in ticks of one target under a lease, and there is an
  `access-roster tick` command.**
  [0029](docs/decisions/0029-ticks-per-target-under-a-lease.md), on the ports
  above. A GitHub organisation, a Slack workspace and (for GitHub) the people's
  link check, `github:links`, are targets; `controller.Tick(ctx, target)` is
  one target's pass and publishes **its own report only**, so the report
  ConfigMap entry of a target nobody ticked is not read, written or rewritten.
  `access-roster controller github|slack` is still the Kubernetes runner: it
  sweeps every target on the interval (links first), each under a lease taken
  from the State port (a create with a lifetime, renewed by compare-and-swap,
  released only if still the holder's, and the tick's context ends if the lease
  is lost), and the sweep prunes the reports of targets the policy no longer
  has. **New:** `access-roster tick <github|slack> <target> --config <file>`
  runs one target's tick once under its lease and exits (an organisation's
  login, `github:links`, or a workspace's key), which is the shape of a
  function that lives for one invocation and is useful to an operator now.
  **It refuses to run while the leases are in-process only** (no shared State,
  which is every installation until B3): a running controller would not be kept
  off the same target. Scale the controller to 0 and pass
  `--unsafe-local-lease` to run it anyway.

  - **Nothing moves and nothing new is stored.** The leases are the
    `lease.<kind>:<target>` keys the legacy adapter already maps to the hub's
    Valkey lease slot (`{target}:lease:github-tick`, `github-links`,
    `slack-tick`); reports stay the two status ConfigMaps. Keys the legacy
    adapter cannot hold (`share.`, `cache.`, `gate.`, `gh.link.`) are not used:
    the shared inputs are cached in memory per policy digest (five minutes,
    and read again when anything mounted changes, such as an operator's
    request), and the Slack Connect hand-off keeps working without a
    pending-share record, as below. All of that is B3.
  - **A request ticks its target.** An operator's Refresh now ticks only its
    organisation or workspace (the controller's mounted-records watch notifies
    its in-process trigger with that target) instead of the whole pass, and the
    console calls `Trigger.Notify(target)` on a Refresh, a confirmation and a
    save of a channel or Slack Connect record. With the legacy adapter the
    console and the controllers are different processes, so the controllers
    still learn of a console write from the records they mount, polled every 30
    seconds, and a change of a credential or record runs a sweep; B3's
    key-value watch replaces the poll.
  - **Slack Connect.** The host's tick, having invited a guest (or seeing an
    invitation still waiting), notifies the guest's tick, which accepts; the
    sweep is the fallback when the notification cannot reach the guest's runner.
    `probeGuestSides` is now the host's: it reads the guests' last reports from
    the blob, asks only for the sides a guest's own report does not list, and
    publishes what it found in the host's report as the new additive
    `guest_sides` (the console merges them into the guest's side as before; an
    older console shows no probed side and nothing else changes).
  - **Replicas stay at one, strategy `Recreate`.** The leases are only
    exclusive across pods when the State is shared, and a controller has no
    Valkey configured and the chart gives it no way to name one, so with the
    legacy adapter its leases are in its own process (it logs that); a second
    replica or a rolling update would not be kept off, and the previous
    version, which takes no lease, would run beside the new one. Two replicas
    wait for B3's key-value State, which both pods share. Nothing in the chart
    changes.

## v1.52.3

- **The GitHub controller waits out a rate limit instead of failing the
  pass.** A 429, or a 403 with `Retry-After`, an empty `X-RateLimit-Remaining`
  or a secondary-limit message, is waited for (`Retry-After`, else the
  `X-RateLimit-Reset` time, at most 60 seconds) and retried up to three times;
  the wait ends with the context. Only such an explicit rejection is retried,
  never a call GitHub may have processed, so the single-use OAuth token
  refresh is not repeated after any other answer. The members listing reads
  its GraphQL point budget and waits for the reset before the next page when
  less than two pages' worth is left; a GraphQL rate limit answered inside a
  200 is waited out and retried the same way. Each wait is one log line and
  counts in the new `github_roster.rate_limited` metric;
  `github_roster.rate_limit_remaining` reports the budget GitHub last stated.

## v1.52.2

- **The documented IAM policy for `sts:GetWebIdentityToken` now works.** The
  examples in the AWS workloads and Lambda guides pinned the audience with
  `StringEquals`, but `sts:IdentityTokenAudience` is multi-valued, so STS
  answered `AccessDenied` for a correctly configured role. The audience
  condition is now `ForAllValues:StringEquals`; `sts:SigningAlgorithm` keeps
  `StringEquals`.

## v1.52.1

- **A group address is no longer read as a user.** The directory answers a
  user read of a group (or a group alias) with a 400 "Type not supported:
  userKey", which the hub logged as a `live account read failed` warning on
  every lookup. A group the snapshot knows is answered from it without a
  directory read, and that one answer from Google is an absence rather than
  an error. Authentication failures, rate limits and 5xx still warn.

## v1.52.0

- **The Lambda extension forwards Lambda platform logs as OTLP logs.** It
  subscribes to the Lambda Telemetry API and sends `platform.*` events
  (timeouts, out-of-memory kills, init and restore errors, and the `REPORT`
  metrics: duration, billed duration, max memory, init duration) to
  `<endpoint>/v1/logs` with the same bearer token as the proxy, so they reach
  the log store without CloudWatch. Failures (`status` other than `success`,
  any `errorType`) are ERROR records; X-Ray trace context is carried when the
  event has it; the resource is `service.name` (`OTEL_SERVICE_NAME`, else the
  function name), `faas.*` and `cloud.*`. On by default
  (`ACCESS_ROSTER_PLATFORM_LOGS=false` turns it off). `function` and
  `extension` logs are opt-in (`ACCESS_ROSTER_FUNCTION_LOGS`,
  `ACCESS_ROSTER_EXTENSION_LOGS`) because a function that exports its own logs
  through OpenTelemetry would send each line twice. The queue is bounded
  (`ACCESS_ROSTER_TELEMETRY_BUFFER_*`; oldest dropped and counted), exports are
  fail-open, and what is queued is exported before the environment can freeze
  and on `SHUTDOWN`. The binary grows by about 65 KB. See
  [integrations/aws-lambda.md](docs/reference/lambda.md#platform-logs).

## v1.51.0

- **AWS workloads exchange their IAM role's token.** A Lambda function, ECS
  task or EC2 instance can call `sts:GetWebIdentityToken` (AWS outbound
  identity federation) and exchange the JWT it gets for a token of this
  issuer's, with no stored secret. New `exchange.aws` chart value
  (`audience`, `maxAge`, `accounts[]` of `{account, name, issuer, jwksUri,
  orgId, algs}`), mounted as a file named by `AWS_FEDERATION_FILE`; **an empty
  list verifies no AWS token**, because any AWS account can mint one for a role
  of its own. A token is accepted only from a configured account's issuer, with
  its signature checked against that account's key set (ES384 or RS256, pinned
  per row), the configured audience, `iat` no older than `maxAge` (default
  5 minutes) and the account claim, the role's ARN and the row all agreeing.
  The identity is the IAM **role**: the minted subject is
  `aws:<account>:role/<path><name>`, never a session or a function. `sub` is
  accepted only as a role ARN (AWS documents no other form), so an
  `assumed-role` session ARN is refused. New policy matcher
  `aws: {account, role, path, function, org_id}` (account exact and required,
  the rest `path.Match` globs; roll the issuer before the policy that uses it,
  since an older issuer refuses the key). Audited as `roster.token.exchanged`
  with proof `workload`; the catalogue is unchanged. See
  [connect/aws-workloads.md](docs/how-to/connect/aws-workloads.md).

- **A Lambda extension layer sends a function's OpenTelemetry data with the
  function role's identity.** The release now carries
  `access-roster-lambda-layer_<version>_linux_{amd64,arm64}.zip`, whose only
  file is `extensions/access-roster-otlp` (about 10 MB, 4 MB zipped). Set
  `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318` in the function: the
  extension gets an identity token from regional STS
  (`sts:GetWebIdentityToken`), trades it at the issuer for a short-lived access
  token, refreshing on demand because a frozen Lambda runs no timers, and
  forwards each export with it as the bearer. It is fail-open: with no token the
  exporter gets a retryable 503 and the extension logs one line per failure
  window; the function is never blocked. `ACCESS_ROSTER_TOKEN_FILE` also writes
  the token for a function's own collector. `docs/integrations/aws-lambda.md`
  has the settings and the IAM policy. It needs an issuer that accepts AWS
  identity tokens. The release publishes the zip only; a layer version is yours
  to publish.

## v1.50.3

- **When a rollout replaces every Valkey pod at once, the client finds the new
  ones through the Service.** The topology reload asked the pod addresses it
  already held first, each costing the whole dial budget, and fell back to the
  configured Service name only after the last had failed, so both issuer
  replicas dialled the old pods for minutes while sign-in and token calls
  failed. The reload now reads `CLUSTER SLOTS` through the configured address on
  a fresh connection every time; the Service follows the pods, so one dial finds
  a live node. A test with six replaced pods goes from about 29s to about 2s.

## v1.50.2

- **A write that meets a lost Valkey node is retried once, after the topology
  reloads.** The failover fix below still left one failure: the command that
  discovered the dead primary was refused (`dial tcp <old primary>: i/o
  timeout`) although a replica was already serving its key. Every call the
  shared login state makes (`Get`, `Set`, `Delete`, `Add`, `Remove`, `Members`)
  that fails with no answer from the server (a dial error, a timeout or reset
  connection, `EOF`) or with `CLUSTERDOWN`/`TRYAGAIN` now waits (at most two
  dial timeouts) until the client sees a different primary for the key, then runs
  once more and logs one warning with the key's prefix. All of them are safe to
  repeat; `SetIfAbsent` is the exception, since a lost reply would turn "you
  took it" into "taken", so it is retried only when it certainly never ran (a
  dial error or `CLUSTERDOWN`). A server's own error, or the caller's context
  ending, is never retried. New `TestNoWriteFailsOnceTheReplicaHasTakenOver`
  (same opt-in docker cluster) streams writes through a primary kill and fails
  on any write still in flight after the takeover; it failed on every run
  without the retry.

## v1.50.1

- **In cluster mode the Valkey client follows a failover by itself.** When a
  shard's primary died, its replica was promoted within seconds, but the client
  kept sending that shard's commands to the dead address until its once-a-minute
  topology refresh: measured against a three-shard cluster, a third of all
  writes were still failing 30 seconds after the kill. A command that fails
  without an answer from the server (a dial timeout, a reset connection) or with
  `CLUSTERDOWN` now asks for a topology reload (asynchronous, coalesced), the
  periodic reload runs every 5 seconds instead of 60, and a dial gives up after
  1 second and 2 attempts instead of 5 seconds and 5 attempts, so a reload that
  happens to ask the dead node first does not wait half a minute on it. The same
  cluster now takes every write again 3–6 seconds after a primary is killed,
  with nothing written before the kill lost. The dial limits apply in
  non-cluster mode too. New `TestTheClientSurvivesTheLossOfAPrimary`
  (docker, opt-in via `VALKEY_TEST_CLUSTER_ADDR` and
  `VALKEY_TEST_CLUSTER_CONTAINERS`) kills a primary and asserts both.

## v1.50.0

- **A test keeps `contracts.md` in step with the protos.** `just docs-check` now
  fails, naming each one, when a service or RPC in `proto/` is not mentioned in
  `docs/reference/contracts.md`. It found `accessissuer.v1.SessionService`
  (`ListSessions`, `RevokeSessions`) undocumented, and the page now describes it.

- **GitHub organisations get Refresh and a prompt pass, as Slack workspaces
  have.** The GitHub controller now looks every 30 seconds at its mounted App
  credentials and at the console's records, and runs a full pass at once when an
  organisation's credential or record changed (a new installation, a reconnect)
  or an operator asked for one, instead of waiting out `githubRoster.interval`.
  The organisation's page has a **Refresh** button for the organisation's
  operators (the installation-wide operator, or the operator of the owning
  directory), backed by the new `RequestGitHubPass` RPC: it leaves a
  `_pass.<organisation>.json` marker in the `<release>-github-orgs` ConfigMap,
  is refused within a minute of the last request (`resource_exhausted`) and for
  an organisation whose App is not installed (`failed_precondition`), is
  forgotten when the organisation is disconnected, and is audited as the new
  `roster.github_org.pass_requested`. `GetGitHubStatus` carries the last request
  as `pass_requested_at`, and the page says *Pass requested* until a newer report
  exists. The chart mounts `<release>-github-orgs` read-only into the controller
  (new `RECORDS_DIR`, optional volume); no RBAC changes, because a mounted volume
  is read by the kubelet. The watch itself, the directory listing and the
  rate-limit gate are now shared with the Slack controller in `internal/rails`
  (`Watch`, `Digest`, `Entries`, `Gate`).

- **Audit catalogue 1.6.0 removes what was dead and states what is historical.**
  The `takeover` branch of the console-channel message (the feature was removed
  in v1.48.0) and the stale "taken over by id" summary of
  `roster.slack_channel.adopted` (adoption is by name) are gone; a Slack Connect
  channel's records now carry the `sources` count its console counterpart always
  had, and its created message says how many directory groups and individual
  addresses feed it. `reason` on console-channel records and `from` on
  Slack Connect records are no longer written; both stay declared in their
  schemas, described as historical, because records written under 1.3.0 to 1.5.0
  carry them and must still read. A console-channel record written by the
  removed take-over feature now reads without its "taken over from git" suffix.
  Adds `roster.github_org.pass_requested`. Existing installations take the new
  version at the next start.

## v1.49.3

- **Fix: a shared channel's record no longer tells an outsider that it exists.**
  Editing or deleting a Slack Connect record answers *no such channel* to a
  caller who may not see it (a viewer of neither its host nor a guest), asked
  before the role over its host; a caller who may see it but not operate it is
  still refused plainly. Creating a record whose name another host's record
  already holds no longer names that host to a caller who may not see it.

- **Fix: a channel defined in both git and the console reads *held* on the
  record's row too.** The console showed *invalid* for the record while the
  controller reported both as held; both now say *held*, with the same reason.

- **Fix: a directory-groups read that fails is said, not shown as an empty
  picker.** The channel forms show the failure in the group picker (new
  `source_directories_error` field on `ListSlackChannelsResponse` and
  `ListSlackSharedChannelsResponse`).

- **Fix: one definition of "this record is that channel" and of a channel id.**
  The probe of a Slack Connect channel's guest sides matches a record that has
  no channel id by name only for a proven host (the host reports it, or both host
  teams are known and equal), as the reconciler does. A record's channel id is
  checked with the policy's own rule (`C` or `G` and at least eight capitals or
  digits); a shorter id on a console or Slack Connect record is now refused. The
  chart's `slackApps[].workspace` takes the policy's workspace-key shape, so a
  key the service would refuse fails `helm template`. The controller and the
  console use one *no owning directory* hold text.

- **Docs and wording.** The `policy` package, the proto comments (individual
  members, the update fields, `can_change_owner`, the `GetSlackStatus` refusal),
  the state-push mirror comment and the console wording (*adopted*, not *taken
  over*, for an existing channel; the navigation as Overview and four clusters)
  say what the code does.

## v1.49.2

- **Fix: archiving from the console asks Slack first.** Deleting a console
  channel's record with *Also archive* ticked now refuses, with the record kept
  and nothing changed, unless the workspace's controller reports that it acts
  (a dry-run workspace, or none reported, is archived by hand), asks Slack
  (`conversations.info`) and refuses a Slack Connect channel whatever the
  controller's report says (a held console record over a channel that is shared
  in Slack used to be archived for every organisation in it), and refuses a
  channel the bot cannot see or a Slack that does not answer. The delete dialog
  shows the checkbox only for a workspace that acts (new `acting` field on
  `SlackChannelWorkspace`).

- **The chart refuses an `actsIn` entry the policy does not define.** With the
  policy given inline in values, `slackRoster.actsIn` must name keys of
  `policy.slack.workspaces` and `githubRoster.actsIn` must name organisations of
  `policy.github`; an unknown entry now fails `helm template` with the entry
  named, instead of crash-looping the controller (which would be down for every
  workspace) after the rollout. With no policy in the values the check is
  skipped; the controller's own refusal to start stays as the backstop.

- **Disconnecting a Slack workspace clears its old report, and the dialog says
  its records stay.** A workspace with no bot token (not connected, or created and
  not installed) is now reported as waiting with nothing carried over, so the
  Channels and People views no longer show the channels and members of a
  connection that is gone, or of another team. The channel and Slack Connect
  records are kept as before, and the Disconnect dialog now says they apply to
  whichever Slack team is connected under that key next.

- **Saving a channel or Slack Connect record wakes the Slack controller.** The
  controller's 30-second look at the mounted credentials and records now includes
  the console's `_channel.*` and `_shared.*` records (confirmations, pass
  requests and install states still do not count), so a save no longer waits up to
  an interval for its pass; saves made close together are answered by one pass.
  The console and the docs now say a change takes effect "within a couple of
  minutes" (the kubelet's projection of the mounted files dominates), not "at
  once" or "on the next pass".

- **Documentation brought up to date with the Slack reconciler, console channels,
  Slack Connect and the v1.41–v1.49 changes; nine new decision records
  (0017–0025).**

## v1.49.1

- **The guest-side probe asks only about managed Slack Connect channels.** A
  shared channel no console record manages is no longer probed with
  `conversations.info`, so the roster stops spending a call per other
  connected workspace per unmanaged channel every pass. For a managed channel
  the rules are unchanged (Slack-named guests when informative, workspaces
  that already list it skipped), except that when Slack names no guest the
  probe asks exactly the workspaces the record names as sides (host and
  `with`), not every connected workspace.

## v1.49.0

- **Individual addresses as Slack channel members.** A console channel record
  (`_channel.<workspace>.<name>.json`) and a Slack Connect record
  (`_shared.<name>.json`) gain `members: [<email>...]` beside `sources`
  (directory groups). Either may be set, at least one must be. The Manage form
  has an *Individual addresses* field next to *Directory groups*. An address
  must be an active user of a directory the channel draws from: an ordinary
  channel, the directory that owns its workspace; a Slack Connect channel, any
  connected directory. The console refuses a group typed as a person, a person
  typed as a group, a repeat, and an address outside those directories. The
  desired membership is the union of the groups' members and the individuals,
  mapped to people exactly as group members are; an individual who is
  suspended or gone is a leaver like a group member (never added, removed by a
  strict channel). The channel page, the Channels and Slack Connect rows show
  both ("2 groups, 3 people"), and a person's page marks the channels that list
  them *individually*. `ResolveDirectoryGroups` also answers for `users`.
  Audit catalogue **1.5.0**: the individuals are targets of the new
  `directory_user` type and the record's data counts them (`members`).

## v1.48.0

- **Archive a channel in Slack when forgetting its console record.** The
  delete dialog of a console channel has an opt-in, off by default: *Also
  archive #name in Slack*. Ticked, the bot calls `conversations.archive` after
  the record is forgotten (`channels:manage` and `groups:write`, already in the
  App's scopes), under the same operator role as the delete, and the action is
  audited as `roster.slack_channel.archived` (the audit catalogue is
  **1.4.0**). When the bot cannot (it is not in the channel, or Slack refuses)
  the record is still deleted and the note says to archive it by hand. A Slack
  Connect channel is never archived from the console, whoever hosts it: the
  dialog says so and the server refuses the request with nothing changed,
  because archiving closes the channel for every organisation in it. Channels
  the policy defines cannot be deleted here at all.

- **The Channels tab uses the shared filter bar.** It narrows by workspace
  (any side of a channel), kind (policy, console, Slack Connect), state (ok,
  pending, waiting, held, invalid, not reported; pending is a channel the
  controller is about to create, adopt or accept) and a name or Slack id
  search, with the
  same components as Discovered and Slack Connect, and its summary reads "N of
  M shown". Every selection is in the address query
  (`#/slack/channels?workspace=&kind=&state=&q=`).
- **Removed: Take over from git.** It was a migration aid. The channel page has
  no **Take over from git** action and no *taken over* state; the Manage form
  refuses a channel the policy defines in that workspace (*this channel is
  defined in git; remove it there to manage it here*), and the server refuses
  a create or update the same way. To move a channel from git to the console:
  remove it from the policy, then Manage it from Discovered. **No mixing:** a
  channel defined both in the policy and as a console record is held on both
  sides (*defined in both git and the console*) and nothing on it changes
  until one definition is removed. A stored record that still carries
  `supersedes_policy: true` keeps loading with the field ignored, is never
  written with it again, and is a plain console channel once git no longer
  defines the channel. The field is reserved in
  `SlackChannelDefinition`, and `superseded` and `ignore` are gone from the
  Slack status. The removal itself leaves the audit catalogue alone: records
  already written keep their `reason: takeover`, and nothing emits it any more.

## v1.47.2

- **Fix: the guest-side probe skipped every channel when Slack named no
  guests.** Slack returns a bot only its own team for a Slack Connect channel,
  so the team filter added for the probe skipped every workspace and a guest
  side only the probe could find disappeared from the report. The probe now
  asks only the workspaces Slack names as guests when it names any connected
  one besides the host and the workspaces that listed the channel; when it
  names none, it asks every other connected workspace again. Expected
  `channel_not_found` and `not_in_channel` stay at debug, real errors warn, and
  each pass still logs one `guest-side probe` summary.

## v1.47.1

- **Filters on the Slack Discovered and Slack Connect tabs.** Discovered
  narrows by workspace, kind (ordinary or Slack Connect), visibility (public,
  private, unknown) and a name search, sorts by workspace then name (or most
  members), and its summary reads "N of M shown". Slack Connect narrows by host,
  by a workspace on either side, by state (active, waiting, pending, held,
  invalid, not reported) and a name search. Every selection is in the address
  query (`#/slack/discovered?workspace=&kind=&visibility=&q=&sort=`,
  `#/slack/connect?host=&side=&state=&q=`), like the Channels tab's.
- **Fix: Manage on a discovered Slack Connect channel left a side out and
  defaulted an unseen side to public.** A workspace whose own report lists the
  channel (found, or probed from the guest side) is now a prefilled side with
  the privacy it reported, even when its report carries no team id; a side
  nobody could see has no default and must be chosen ("private there and the
  bot is not in it, or not shared"). A managed Slack Connect channel the
  controller holds for a visibility that differs from its record now says so,
  with an Edit hint, on the Slack Connect row and the channel page.

- **The guest-side probe no longer logs a warning per channel per pass.** The
  controller now asks a connected workspace about a Slack Connect channel
  only when Slack lists that workspace's team in it (the host, shared,
  connected, pending or internal team ids a report carries); every other
  workspace is skipped without a call. A `channel_not_found` or
  `not_in_channel` from a probe that was expected is an invisible side, logged
  at debug (the old check matched the error the wrong way round, so it always
  warned). Real errors still warn, and each pass logs one summary line:
  `guest-side probe` with `probed`, `visible` and `invisible` counts.

## v1.47.0

- **Take over a policy channel from git on the console, with no unmanaged
  gap.** A policy channel (`slack.workspaces[k].channels`) shows **Take over
  from git** on its channel page (operator over the workspace's owning
  directory, or installation-wide operator): the console-channel form opens
  prefilled with its workspace, name, channel id (from the latest report),
  visibility, mode and `ignore` list, and asks for directory-group sources.
  Saving writes the usual `_channel.<workspace>.<name>.json` record with the
  new field `supersedes_policy: true` (also `SlackChannelDefinition.supersedes_policy`)
  and audits it as `roster.slack_console_channel.created` with a new optional
  data field `reason: takeover`; the audit catalogue is **1.3.0**. A console
  channel that duplicates a policy channel is still refused as *defined in git*
  unless it sets `supersedes_policy` and covers exactly that channel (same
  workspace and name, or channel id; same visibility). The controller then
  reconciles the record and never the policy entry, reports the entry as
  `superseded` (`taken over on the console by X at T; remove it from git`,
  also on the Channels tab and the channel page), and removing the entry from
  git later changes nothing in Slack. A takeover removes nobody by itself;
  strict removals still need the directory to vouch and the breakers apply.
  Deleting the console record restores the policy entry's management on the next
  pass. The old way (remove it from git first) still works.

## v1.46.1

- **A Slack Connect side the guest bot does not list is now
  probed by id.** A bot lists a channel that is public on its side only
  sometimes, and one it has not joined often not at all, so discovery showed
  that side as "not listed" and the operator had to add it by hand. For every
  Slack Connect channel any connected workspace discovered, the controller now
  asks each other connected workspace that did not list it, with
  `conversations.info` by channel id (bot token, `include_num_members`), at
  most once per channel and workspace per pass. An answer is published in that
  workspace's `discovered_shared` (privacy, name and member count as that side
  sees them, bot not joined), so the Manage form prefills the side; a public
  side the bot has not joined is joined on Manage as before. A private side
  answers `channel_not_found` and stays unknown. A probe that fails is logged
  and never fails the pass. Nothing else changes; the status document only
  gains entries.

## v1.46.0

- **The console is organised as four clusters, and Slack is one place.** The
  rail reads Overview · IDENTITY (Directories, Directory groups, People, Rules)
  · ACCESS (Internal groups, Clients, Sessions) · SYSTEMS (GitHub, Slack) ·
  ADMIN (Audit, Settings). Slack is a single entry with tabs — Workspaces,
  Channels, Slack Connect, Discovered, Apps — and every managed channel has a
  page of its own: what feeds it (internal groups for a channel defined in
  git, directory groups for a console channel), its mode, each person's state
  and why, the Slack Connect sides, the removal breaker, and its audit
  history. GitHub gains a Runners tab. Old links (`#/slack-apps`,
  `#/slack-connect`) still work.
- **Reverse links.** A directory group's page lists the Slack channels it
  feeds and how its people stand there, and the GitHub teams it reaches; an
  internal group's page lists the channels defined in git that name it; a
  person's page has a Slack section with each channel's state and reason
  (e.g. "waiting for them: no Slack account yet").
- `SlackChannelStatus.sources` now also carries the internal groups a channel
  defined in git is fed by, so the channel page can link them. No other API
  change; nothing new calls Slack.

## v1.45.0

- **Console channels: ordinary Slack channels managed on the console, fed by
  directory groups.** Slack channels now come in two kinds that are never mixed
  on one channel. **Policy channels** (`slack.workspaces[k].channels`, in git)
  are fed by internal groups and are unchanged, for channels the
  infrastructure owns. **Console channels** are records on the console, audited
  and backed up with the other Slack records, fed by **directory (IdP) groups**
  by address and never by internal groups: an **ordinary** channel in one
  workspace takes the groups of the directory that owns the workspace, and a
  **Slack Connect** channel takes groups of any connected directory, each person
  joining on the side whose owning directory serves their address. Members are
  resolved through nested groups (new `AccessService.ResolveDirectoryGroups`,
  gated like the other reads; cycles end and the depth and size are bounded,
  and a read cut short refuses the channel for the pass). The controller
  reconciles a console channel with the same rules as a policy channel (create
  or take over by name or id, `extend` or `strict`, the directory vouches
  before a removal and now asks about the directory groups and the groups they
  nest, breakers, holds, never a visibility change), refuses a record whose
  source is not a group of an allowed directory, and fails only the workspaces
  that depend on a directory it cannot read. A console channel is refused as
  *this channel is defined in git* when the policy binds the same name or
  adopts the same channel id, and one channel is managed one way. New record
  `_channel.<workspace>.<name>.json` (workspace, name, channel_id, private,
  mode, ignore, sources, created/updated by and at) in
  `<release>-slack-workspaces`, included in the `slack-records` mirror, and
  `SlackChannelService` (list, create, update, delete: operator over the
  workspace's owning directory, or installation-wide). The Slack page lists the
  console channels with a form, and **Discovered channels**: every channel the
  bots see that no policy binding or record manages (reports gain
  `discovered`, additive, capped at 500 per workspace with a count of the
  rest), with **Manage** prefilled. **Breaking for a record, not for a
  release:** a Slack Connect record's `from` is now `sources`, directory
  groups. A record written with internal groups is listed `invalid` with a
  message, its host's operators can edit it, and it is acted on by nobody until
  then. The Slack Connect form's picker lists directory groups of every
  connected directory under the directory (id and domains), searchable; it shows
  which chosen groups land on each side and warns about a group whose directory
  owns no side. Audit catalogue **1.2.0** adds
  `roster.slack_console_channel.created`, `.updated` and `.deleted` and the
  `directory_group` target type: a group's address is carried as a target,
  never as data, so the Slack Connect records no longer write `from`. Moving a
  policy channel to the console: remove it from the policy, find it under
  Discovered, Manage it with the directory group as the source and the same
  mode ([docs](docs/how-to/connect/slack-console-channels.md)).

- **Fix: a Slack Connect side no report mentions is shown as unknown, not
  dropped.** Slack names only the host among a channel's teams when the host's
  channel list is read, and a guest bot lists a private channel only once it is
  in it, so a guest side could leave no trace in any report and the Manage form
  omitted it. Discovery now offers every connected workspace the caller may view
  as a side: one nothing places the channel in is unknown (`listed: false`) and
  is not prefilled; Slack's pending-guest and internal team lists now place a
  channel in those workspaces too.

## v1.44.0

- **A recovery copy of the Slack state: `slackState.push`.** In the shape of
  `directory.push` and `githubApps.push` (`secretStore`, `remoteKey`,
  `refreshInterval`, `deletionPolicy` fixed at `None`) with one more required
  key, `recordsRemoteKey`. It renders two External Secrets `PushSecret`s: the
  whole of `Secret <release>-slack-credentials` at `remoteKey`, and the whole
  of the new `Secret <release>-slack-records` at `recordsRemoteKey`. The
  records live in a ConfigMap and a `PushSecret` reads Secrets only, so the
  service now keeps that Secret as a mirror of exactly the ConfigMap's
  `<workspace>.json` and `_shared.*` entries (never confirmations or pass
  markers), written in the same code path as the ConfigMap and reconciled at
  start. If the ConfigMap holds no record and the mirror does, start
  repopulates the ConfigMap from it. Off unless `slackState.push` is written,
  and refused at render without `directory.store: kubernetes`, without a
  store or either key, or for two pushes sharing one path. The mirror Secret
  is written regardless; it uses the permissions the service already has.
  Restore procedure: docs/how-to/back-up-and-restore.md, Slack state.

- **Slack Connect: find channels that already exist and take them under
  management.** The Slack controller now lists, per connected workspace, the
  Slack Connect channels its bot can see and publishes them in the report as
  `discovered_shared` (additive; status version unchanged). `#/slack-connect`
  has a Discovered section: one row per channel with its name, privacy and
  members per side, its host workspace and whether it is managed; Manage opens
  the create form prefilled, and the record keeps the channel's id
  (`channel_id`) so the reconciler takes over exactly that channel. A host side
  takes over by id and never invites a side that is already connected; a
  connected guest side joins a public channel instead of waiting for an
  invitation, and a private side without the bot is held until the bot is
  invited. Nobody is ever removed, and a team that is not a connected workspace
  is never touched. The audit catalogue is unchanged. See
  [docs/connect/slack-connect-channels.md](docs/how-to/connect/slack-connect-channels.md).

## v1.43.1

- **Fix: a refused Slack install now says why.** Every refused or failed
  install callback, for a workspace connection and for the catalogue App, is
  logged (values through `logsafe`; never the code, a token or a secret) and
  audited with its reason, using the existing `roster.slack_workspace.connect_refused`
  and `roster.slack_app.install_refused` actions. There is no audit catalogue
  change. The callbacks still require the flow cookie and the signed state
  exactly as before.
- **Fix: a person with no Slack account yet is waiting, not blocking.** The
  hold reason on that person's row reads "waiting for them" instead of "needs
  you".

## v1.43.0

- **A pass runs right after a Slack workspace's credential changes, and the
  console has a Refresh button.** The Slack controller now checks the mounted
  credentials and records every 30 seconds and runs a full pass at once when a
  workspace's credential changes, such as a new bot token after an install; the
  15-minute interval is unchanged. Until a report newer than the connection
  exists, the console shows "Installed - waiting for the first pass" instead of
  the previous pass's banner. A new `RequestSlackPass` RPC and a per-workspace
  **Refresh** button (offered only to operators, only for an installed
  workspace) ask for a pass: the request is a marker in the
  `<release>-slack-workspaces` records ConfigMap, a second request within 60
  seconds is refused, and the card shows "Pass requested" until a newer report
  exists. The requester is logged; there is no audit action.

## v1.42.4

- **Fix: an owning directory is labelled by its id and every authoritative
  domain.** In every owner dropdown and "owned by" line (Slack, GitHub, Change
  owner) a directory reads `<workspace id> - <authoritative domains, sorted>`;
  served domains that are not authoritative are left out. `DirectoryRef` gains
  `domains`, and a row's `owner_domain` now carries the joined list.
- **Fix: a Slack workspace that is not connected or not installed yet is
  waiting, not failed.** Such a workspace reports a `waiting` pass with no
  error, the console shows a neutral note, and metrics count it as a waiting
  pass. Real failures stay red.

## v1.42.3

- **Fix: Slack handlers no longer log raw request values.** Every request- or
  Slack-derived value in the Slack console handlers, the Slack controller and
  its records is routed through `logsafe`, as the GitHub handlers already did,
  so a crafted value cannot forge a log record. This closes the open
  `go/log-injection` findings; no token is logged.
- **Guard: a changed audit catalogue under an unchanged version is refused by a
  test.** The document of each released catalogue version is frozen as a test
  fixture, and a catalogue carrying one of those versions must equal it. A
  change to the catalogue now needs a new version and its fixture.

## v1.42.2

- **Fix: a slow client-document origin no longer fails a sign-in.** The fetch
  of a client document (`client_documents`) now allows 10 seconds per attempt
  (was 5) and retries once, after a short pause, on a timeout, a reset
  connection or a 5xx, within the sign-in's own deadline. When refreshing an
  already-validated document still fails that way, the last good copy is served
  for at most one hour past its ten-minute expiry, with a warning logged each
  time. Never on a validation failure, a redirect or a 4xx: those stay refused.
  Allow-listed origins, no redirects, the size limit and document validation
  are unchanged.

## v1.42.1

- **Fix: the audit catalogue's version is now 1.1.0.** v1.41.0 and v1.42.0 added
  actions under 1.0.0, so an audit installation that had already registered
  1.0.0 refused the changed document and the service stopped at start.

## v1.42.0

- **BREAKING: a Slack workspace's team, owner and domains, and a GitHub
  organisation's owner, are no longer policy keys.** v1.41.0 briefly let the
  policy carry `slack.workspaces.<key>.team_id`, `.domains` and `.owner` and
  `github.<org>.owner`. access-roster already knows each at run time (each
  connected directory has its workspace id and the domains it serves, a Slack
  install tells us the team, and the person connecting acts within a
  directory), so holding them twice was drift waiting to happen. A policy that
  still carries any of them is **refused at load**, with a message saying where
  the value now comes from. **Migration:** delete the keys, then connect (or
  reconnect) from the console, where the owning directory is chosen. A Slack
  workspace stays held (*no owning directory: set the owner on the console*)
  until it has an owner; a GitHub organisation keeps working as an
  installation-wide one until it is connected with an owner, or the
  installation-wide operator sets one. The policy keeps what is policy: the
  workspace key and `channels` (with `mode` and `ignore`), `people`, and GitHub
  team bindings. Removed from `policy`: `SlackWorkspace.TeamID`, `.Domains`,
  `.Owner`, `GitHubOrg.Owner`, `Set.SlackWorkspaceTeam`, `Set.SlackOwner`,
  `Set.GitHubOwner` and `SlackWorkspace.NormalisedDomains`; added
  `Set.SlackWorkspaceDeclared`.
- **Upgrade order: roll the console before the slack-roster controller.** The
  controller now needs the new `ListServedDomains` RPC and fails every pass
  against an older console. Its identity must also hold the installation-wide
  viewer role (or the viewer role over each owning directory) to read the
  served domains.
- **New: the owner is recorded when a connection is made.** One rule for Slack
  workspaces and GitHub organisations. The installation-wide operator chooses
  the owning directory from the connected ones, or none; an operator of
  exactly one connected directory owns what they connect; an operator of
  several chooses among theirs; a directory the caller does not operate, or one
  that is not connected, is refused. The owner is the connection record's
  `owner` (optional in version 1, so a record written before it reads as "no
  owner" and stays installation-wide) and is carried in the signed connect state
  to the callback that creates the record. Every owner check (`requireOwner`, the
  Slack catalogue, the workspace page, the Slack Connect editor, viewer
  filtering, `github_owner`) now reads the **connection record**, not the policy.
  Only the installation-wide operator changes it afterwards: new RPCs
  `ChangeSlackWorkspaceOwner` and `ChangeGitHubOrganisationOwner`, audited as
  `roster.slack_workspace.owner_changed` and `roster.github_org.owner_changed`.
  `roster.slack_workspace.connected` and `roster.github_org.connected` now carry
  `owner`. The connect forms show the owner choice where there is one, the rows
  show the owner by its primary domain, and the installation-wide operator gets
  *Change owner*. New request fields `BeginSlackWorkspaceConnectRequest.owner`
  and `BeginGitHubConnectRequest`/`BeginGitHubAppConnectRequest.owner_directory`;
  new response fields `owner_choices` and `may_connect_without_owner`, and
  `owner_domain`, `can_change_owner` on the rows.
- **New: the Slack team is recorded at the first install.** The workspace
  connect callback records the team from `oauth.v2.access`; any later install
  or reconnect must match it (a mismatch is revoked and refused, as before),
  and so is a first install into a team already connected under another key.
  The Slack App catalogue now requires its entry's workspace to be connected
  first (*connect the workspace first*) and holds its install to the recorded
  team. A record is valid with no team until the first install
  (`connection.Record.TeamID` is optional).
- **New: a person is looked up by the owning directory's served domains, every
  pass.** The Slack controller reads the domains each connected directory serves
  from the console (new `AccessService.ListServedDomains`, a viewer's read,
  scoped like the others) and finds a person in a workspace by their address in
  its **owner's** domains. A workspace with no owner holds its people (*no owning
  directory: set the owner on the console*); a directory that cannot be read or
  is no longer connected fails the workspace's pass and changes nothing.
- **Changed: a bound Slack channel is an idempotent upsert, taken over by
  name.** Create if missing, otherwise adopt the existing channel by its
  declared name: a public one is joined and then managed, a private one the bot
  is in is managed, and each adoption is recorded once as
  `roster.slack_channel.adopted`. Before, a same-name channel the bot did not
  create was held (*adopt it by id*); `adopt: <id>` is now only an optional
  disambiguation (a renamed channel, two candidates), still validated as before.
  Kept safe, each held with its reason and tested: a channel whose visibility
  differs from the declared `private` is never converted; an archived channel of
  that name is never unarchived (the controller now lists archived channels,
  `slackapp.Client.AllChannels`, to tell an archived name from a free one); a
  private channel the bot cannot see makes `conversations.create` answer
  `name_taken`, which is a hold (*a private channel named X exists that the bot
  cannot see; invite the bot to it*), recorded once as
  `roster.slack_action.held`, never a duplicate under another name and no longer
  a failed `roster.slack_channel.created` every pass; and a `strict` adopted
  channel removes only after the directory vouches, with the first pass after
  adoption subject to the breaker like any other. A channel that was held for
  this reason before is adopted on the first pass after the upgrade, in a
  workspace listed in `slackRoster.actsIn`: review a dry run first.
- **State:** `access.Binding` carries the owner a flow will record; a state
  issued before it (four parts) still verifies, as one with no owner.

## v1.41.0

- **New: `OUTBOUND_CA_FILE` for resource-proxy.** A PEM bundle appended to the
  system root pool, used only by the outbound forwarder's connection to
  `OUTBOUND_TARGET`, so a target served by a private CA is reachable while
  everything else (the issuer, the token exchange) keeps trusting public roots
  only. Flag `--outbound-ca-file`. Read once at start; a file that is unreadable
  or holds no certificate refuses to start; it needs `OUTBOUND_LISTEN` like the
  other `OUTBOUND_*` values. Restart to rotate.
- **New: Slack Connect channels, created and edited on the console.** A
  shared channel between the installation's own Slack workspaces is a record,
  not policy: `name`, `host` (the workspace that creates and owns it,
  **immutable**), `with`, `from` (policy groups: members come only from
  groups) and `private` (one bool, or one per side). The new Slack Connect page
  lists the records with what the host's controller reported (not reported,
  pending, waiting for acceptance, active, held, invalid), creates, edits (`with`,
  `from`, `private`; a change of host or name is refused with "create a new
  channel") and deletes (the record only: the channel stays in Slack and the
  reconciler stops managing it). Records are validated against the policy and
  written as `_shared.<name>.json` in `<release>-slack-workspaces` under the
  ConfigMap's version, retried on a conflict. The operator of the host
  workspace's owner, or the installation-wide operator, may change a record;
  viewers of any workspace it touches see it. New RPCs `SlackSharedChannelService`
  (`ListSlackSharedChannels`, `CreateSlackSharedChannel`,
  `UpdateSlackSharedChannel`, `DeleteSlackSharedChannel`), new audit actions
  `roster.slack_shared_channel.created`, `.updated` and `.deleted`. See
  [docs/connect/slack-connect-channels.md](docs/how-to/connect/slack-connect-channels.md).
- **New: the Slack page.** Connect, read and operate a Slack workspace from
  the console. **Connect** pastes a throwaway app configuration token
  (api.slack.com/apps, *Your App Configuration Tokens*; 12 hours, used once,
  never stored or logged): the service creates the roster's own Slack App from
  a manifest carrying `connection.BotScopes` (nine bot scopes, including `conversations.connect:manage` for listing Slack Connect invitations, each for a
  method the controller calls), keeps its client id and secret as "created, not
  installed", and sends an owner of the workspace to Slack. On the way back the
  bot token is written into `<release>-slack-credentials` and the record into
  `<release>-slack-workspaces` only if Slack says it belongs to the policy's
  `team_id`; any other workspace is **revoked** (`auth.revoke`) and refused,
  recorded as `roster.slack_workspace.connect_refused`. **Reconnect** grants
  new scopes (with a configuration token that updates the manifest),
  **Disconnect** revokes the token and forgets the connection, and the page
  shows per workspace the connection, acting or dry run, the last pass, each
  channel's people (in step, will invite, will remove, held with the reason),
  leavers and any breaker, with **Confirm** for an operator
  (`ConfirmSlackRemovals`: the fingerprint must be the latest report's for that
  gate; lapses in 24 hours; audited). Gated by the workspace's owner
  (`slack.workspaces.<key>.owner`), each row carrying `can_operate`. New
  service `SlackService` (`GetSlackStatus`, `BeginSlackWorkspaceConnect`,
  `DisconnectSlackWorkspace`, `ConfirmSlackRemovals`), new audit action
  `roster.slack_workspace.connect_refused`, new `slackapp.Client.Revoke`. The
  catalogue's install into the wrong workspace now revokes the token too. See
  [docs/connect/slack-workspace.md](docs/how-to/connect/slack-workspace.md#connect-a-workspace-from-the-console).

- **New: a catalogue of Slack Apps, created and installed from the
  console.** `slackApps` declares each App (`id`, the policy's `workspace`
  key, `botScopes`, optional `name`, `description` and `push`). An operator
  creates it on the new Slack Apps page by pasting a throwaway app
  configuration token (api.slack.com/apps, *Your App Configuration Tokens*;
  it expires in 12 hours): the service builds the manifest, creates the App,
  and keeps its client id and secret as "created, not installed". Install
  sends an owner of the workspace to Slack and, on the way back, keeps the
  bot token as `<id>.slack_bot_token` in `<release>-slack-catalogue-apps`
  only if Slack says it belongs to the policy's `team_id` for that workspace;
  any other team is refused and recorded. An entry that later declares more
  scopes than Slack granted shows **scopes missing** and is reinstalled (with
  a configuration token, which updates the manifest). The configuration token
  is used for one call and never stored or logged. `push` copies only the bot
  token, through a PushSecret per entry. New RPCs `SlackAppService`
  (`ListSlackApps`, `CreateSlackApp`, `InstallSlackApp`), new audit actions
  `roster.slack_app.created`, `.installed` and `.install_refused`. See
  [docs/connect/slack-apps-catalogue.md](docs/how-to/connect/slack-apps-catalogue.md).
- **New: per-organisation operators for Slack workspaces.**
  `slack.workspaces.<key>.owner` names the directory workspace that owns a
  Slack workspace, with the same meaning and checks as
  `github.<org>.owner`: its scoped operator creates, installs and reinstalls
  its Apps, the Slack Apps page lists only what the caller may view, and each
  row says whether the caller may operate it (`can_operate`). See
  [docs/reference/policy.md](docs/reference/policy-ownership.md#who-owns-a-slack-workspace).

- **New: per-organisation operators for GitHub.** `github.<org>.owner`
  names the directory workspace that owns an organisation; its scoped
  operator (`<id>:access-roster:operator`) may then connect, reconnect and
  disconnect it, confirm its removals and manage its runner and catalogue
  Apps, beside the installation-wide operator. An organisation with no
  `owner` is operated by the installation-wide roles alone, as before, so
  nothing changes until one is named. The check is one helper
  (`requireOwner`) that Slack workspaces will use for theirs. The GitHub
  pages list only the organisations the caller may view, each row says
  whether the caller may operate it (`can_operate`), and the connect
  callbacks ask the role question again. See
  [docs/reference/policy.md](docs/reference/policy-ownership.md#who-owns-a-github-organisation).
- **New: the Slack controller, `slack-roster`, and its chart values
  `slackRoster.*`.** A second process from the `access-issuer` chart (and its
  own image and archive) that makes each Slack workspace's channels match the
  policy's `slack` table: per pass it reads Slack whole, asks the directory who
  holds each bound group and to vouch (one question per address per pass) for
  each removal and leaver, decides, and acts only in the workspaces listed in
  `slackRoster.actsIn`; every other workspace is a dry run that publishes its
  report and changes and records nothing. A removal set over half of a channel
  or of a workspace is held until an operator confirms that exact fingerprint;
  **one confirmation now satisfies every breaker gate that fingerprint covers**
  (before, a single-channel trip needed the same set confirmed twice). Leavers
  and new holds are reported and recorded once. A workspace that is not
  connected, not installed or whose read failed is reported `failed` alone. It
  never creates accounts, touches user groups or removes anybody from a public
  channel. It needs egress to `slack.com:443`, which the chart leaves to the
  fleet's egress policy. Off by default; see
  [docs/connect/slack-workspace.md](docs/how-to/connect/slack-workspace.md).
- **New: `people` and `slack` in the policy schema (schema only; no
  controller yet).** `people` links the addresses of one person across
  domains; `slack` declares workspaces (own key, Slack `team_id`, email
  `domains`) and the channels bound in them to internal groups, created or
  adopted by ID. A channel is `mode: extend` (the default: only add) or
  `mode: strict` (add and remove; private channels only, so `strict` on a
  public channel is refused at load because Slack lets only administrators
  remove people from one), and a strict channel may `ignore` addresses or
  Slack user ids it never removes. Both keys are validated at load, merged
  across files as `github` is, covered by the policy digest, and their groups
  count as consumed. Slack Connect shared channels are not in the policy:
  they are managed on the console. A controller that reads these keys is
  being built; until it ships, nothing does. (The controller ships in this same
  release, in the first bullet above.)
  See [docs/reference/policy.md](docs/reference/policy-bindings.md#slack-channels).
- **Internal: the Slack reconciler's core (`internal/slackroster`): the pure
  decision (who to invite, remove, hold and report in each workspace's
  channels, and in Slack Connect channels given as input), its status
  document, the per-workspace connection record and credential, and the step
  that applies a decision through the Slack client.** No controller runs it
  yet. (The controller ships in this same release, in the first bullet above.)
  `internal/slackapp` gains `UserInfo` (a member's address) and
  `SharedTeamIDs` on a channel. See
  [docs/explanation/slack-reconciler.md](docs/explanation/slack-reconciler.md).
- **Internal: `internal/rails` now holds what the GitHub controller and the
  reconcilers after it share: the pass loop with its policy-retry backoff
  (`Run`), the console's two questions gated by the policy digest
  (`Directory`) with the removal rule (`Removal`), the held-once ledger
  (`Ledger`) and the last-good-report journal (`Journal`).** The GitHub
  controller calls them and keeps everything GitHub-shaped to itself. No
  user-visible change: same decisions, same audit records, same metrics.
  See [docs/explanation/github-controller.md](docs/explanation/github-controller.md#reconciler-rails).

## v1.40.0

- **New: `resource-proxy`, a sidecar that gives a stock MCP server (or any
  HTTP service) an access-roster resource server's front door, and
  `identity/resource`, the library it is built on.** Inbound it verifies
  the caller's token (signature, issuer, `aud` = the resource's own URL),
  serves the RFC 9728 Protected Resource Metadata at its path-suffixed
  location, answers `401` with `WWW-Authenticate: Bearer
  resource_metadata="...", scope="..."`, writes one audit line per request
  (caller, client, JSON-RPC method and tool, status, duration; never a
  token or a body) and reverse-proxies with the caller's `Authorization`
  removed and SSE streaming intact. Optionally, on a loopback listener, it
  injects a bearer token the workload's own projected ServiceAccount token
  earned by an RFC 8693 exchange, so the stock server holds no credential.
  The image is `ghcr.io/truvity/access-roster/resource-proxy:<version>`,
  multi-arch. See [docs/connect/mcp.md](docs/how-to/connect/mcp.md#fronting-a-stock-mcp-server-with-resource-proxy).
  `identity.Verified` gains `ClientID` (the token's `azp`, or
  `client_id`). No existing package changes behaviour.

- **Removed: the one-time label move.** The start-up relabel of objects an
  earlier release wrote under the old label keys, and the once-a-minute
  sweep that repeated it, are gone: every installation has run a release
  that contained them. An installation still holding objects under the
  old keys must run a release that has the move (see the entry that added
  it) first; the credential-store migration is unaffected.

- **Fixed: reopening a stored, console-connected workspace at start no
  longer refuses every backend kind but `"google"`.** `openStored`
  hardcoded that check, so a second backend (Entra, say) would be
  adopted from a fresh consent but rejected the moment the process
  restarted -- even though `backend.Backend` is documented as
  multi-implementation and the wire contract already carries
  `BACKEND_ENTRA`. It now resolves the kind through the same connector
  list the console offers for connecting a workspace the first time, via
  the new `server.CredentialReopener` an implementation opts into next to
  `Exchange` and `FromKey`. A kind with no such connector is still
  refused, by name.
- **Internal: the GitHub controller's confirm-before-remove gate, policy-digest
  guard, removal circuit breaker and dry-run switch moved to a new
  `internal/rails` package**, generic over what a reconciler is confirming or
  breaking on instead of GitHub-shaped. `internal/githubroster` now calls
  `internal/rails` for these four pieces and keeps everything GitHub-shaped —
  teams, logins, invitations, deriving and deciding — to itself. No
  user-visible change: same decisions, same audit records, same metrics. See
  [docs/explanation/github-controller.md](docs/explanation/github-controller.md#reconciler-rails).

## v1.39.2

- **Fixed: since v1.29.0, `resources` and `client_documents` declared in
  a multi-file policy directory — the chart's layout — were silently
  ignored.** Loading a directory merges its files one by one, and the
  merge had no case for either block, so both were read and then
  dropped. A single-file policy was unaffected. In a deployment this
  meant the discovery document never advertised
  `client_id_metadata_document_supported`, no document client was
  admitted, and no declared resource could be asked for. The failure
  granted less rather than more, but a declared block that does nothing
  is a defect all the same. Both now merge like the rest:
  - `resources` merge by id, and one id declared in two files is
    refused, exactly as a client is.
  - `client_documents` is installation-wide, like `vocabulary`: a second
    file declaring it is refused. An empty `client_documents: {}` counts
    as not declared, since it turns nothing on.

  After upgrading, a directory that declares one resource id in two
  files, or `client_documents` in two files, refuses to load. Before,
  it loaded and the block did nothing.

  A test now lists every field of the policy, and of a GitHub
  organisation binding, with the rule for merging it. A field added
  without one fails the build.
- README gains `Consumers` and `Neighbours` (openbao as relying party, audit, `accessctl` versus workstation's `awsctl`); a test fixture's audience is neutral; ci-workflows pins moved to v3.13.1.

## v1.39.1

- **Every value the GitHub App, consent and workspace flows log now
  passes through `internal/logsafe`.** The organisation, tier, owner,
  workspace, backend, App slug and the errors built from them were
  written as they arrived. The JSON handler escaped them already, so
  none of it was forgeable in practice. Now it is true by construction,
  as it already was for addresses and paths.
- `github.com/truvity/audit` v0.3.1, the same version the console's
  `@truvity/audit` already pinned.

## v1.39.0

- **Added: `accessctl r2`, authenticating for an R2 credential broker's
  audience and then running the real `r2broker` CLI unchanged.**

  Follows the same shape `accessctl bao` already ships
  ([ADR 0013](docs/decisions/0013-openbao-access-through-the-bao-cli.md)):
  it signs in (or takes a job's own identity), exchanges for
  `--audience` (default `r2-broker`, or `$ACCESSCTL_R2_AUDIENCE`), and
  execs `r2broker` with the token in `R2BROKER_TOKEN` — never on argv,
  never in a temp file, since `runChild` replaces this process's image on
  every platform but Windows and a temp file written before an exec that
  never returns could not be cleaned up. The subcommand defaults to
  `credentials` when none is named, and `--service-url` (or
  `$ACCESSCTL_R2_SERVICE_URL`) is injected as `r2broker`'s own flag when
  configured, so a `credential_process` line can be as short as
  `accessctl r2 -- credentials --bucket <bucket> --prefix <prefix>/`. The
  token is cached one file per issuer, client and audience, the same
  shape and margin `kube-token`'s own cache uses.

  access-roster still holds no R2 logic: no bucket, no prefix, no
  permission is ever named here — everything after the sign-in is
  `r2broker`'s own syntax, per
  [ADR 0014](docs/decisions/0014-minting-third-party-credentials-only-where-membership-is-governed.md).
  See
  [docs/reference/sluisctl-wrappers.md#r2-authenticate-then-run-the-real-r2broker-cli-unchanged](docs/reference/sluisctl-wrappers.md#r2-authenticate-then-run-the-real-r2broker-cli-unchanged)
  and [docs/connect/r2-storage.md](docs/how-to/connect/r2-storage.md).

## v1.38.0

- **Added: `accessctl ssh known-hosts`, which writes one managed file
  trusting an installation's own configured SSH host certificate
  authorities, so a laptop stops being prompted on the first connection
  to a fleet host.**

  `~/.ssh/known_hosts.d/accessctl` (or `--file`) is fully rewritten each
  run with one `@cert-authority <patterns> <key>` line per configured
  entry — never `~/.ssh/known_hosts` or `~/.ssh/config` themselves. The
  list of CAs to trust is entirely config, never code: `config.yaml`'s
  own `sshKnownHosts:` section (or `$ACCESSCTL_SSH_KNOWN_HOSTS`, the
  same YAML, when the file names none), each entry pairing one or more
  SSH host patterns with a CA source — a full `url:`, or
  `openbao: {namespace, mount}` joined with the address `accessctl bao`
  already resolves (`--address`, then `$BAO_ADDR`/`$VAULT_ADDR`). Only
  `ssh-ed25519` CA keys are ever written, and a fetch failure keeps the
  previous run's line for that entry (with a warning) rather than
  dropping trust silently or failing the whole run over one
  environment's CA being briefly down.

  It never edits `~/.ssh/config`; it only checks whether a
  `UserKnownHostsFile` line already names the managed file and prints
  the one line to add when it does not. `accessctl login` refreshes the
  file automatically once something is configured — a fresh sign-in
  never fails, or prints anything, over a feature it was never opted
  into. See
  [docs/reference/sluisctl-wrappers.md#ssh-known-hosts-trust-configured-ssh-host-cas-before-the-first-connect](docs/reference/sluisctl-wrappers.md#ssh-known-hosts-trust-configured-ssh-host-cas-before-the-first-connect),
  [docs/connect/ssh.md](docs/how-to/connect/ssh.md) and
  [docs/decisions/0016](docs/decisions/0016-a-managed-known-hosts-file-for-ssh-host-cas.md).

## v1.37.0

- **Added: `groups_delimiter`, a per-audience policy option that rewrites
  a token's `groups` claim to work around opkssh's own colon-splitting
  bug.**

  A client row or a resource row may pin `groups_delimiter: "."` (or
  another delimiter this schema accepts), modelled on `signing_alg`
  ([docs/decisions/0009](docs/decisions/0009-a-default-signing-algorithm-and-per-audience-exceptions.md)):
  after
  [per-audience groups scoping](docs/reference/policy.md#groups-in-a-token-scoping)
  has decided which groups a token for that audience carries, every `:`
  in each one is rewritten to the configured string — `devel:ssh:user`
  becomes `devel.ssh.user`. This is for opkssh specifically: its
  server-side policy, `oidc:groups:<value>`, splits its argument on
  EVERY `:` and reads only the last segment, so it can never match a name
  shaped `<scope>:<thing>:<role>`, this schema's own separator, however
  it is quoted. Rows that name no `groups_delimiter` — every default
  installation, and every other audience of one that sets it — are
  unaffected.

  A delimiter is refused at load if it is empty, the separator itself, a
  quote, a comma, whitespace, or built from the alphabet a scope, thing
  or role is itself conventionally written in
  ([taxonomy.md](docs/reference/taxonomy.md)) — and, because no single character
  can be proven absent from every group name this schema could ever
  declare, also if it would collide two of the policy's own declared
  groups once rewritten. See
  [docs/decisions/0015](docs/decisions/0015-a-per-audience-groups-delimiter-for-opkssh.md)
  and
  [docs/reference/policy.md#groups-delimiter-per-audience-opkssh-interop](docs/reference/policy.md#groups-delimiter-per-audience-opkssh-interop)
  for the full mechanism. It is a temporary interop shim, meant to be
  removed once opkssh's own parser stops splitting on every `:`.

- **Added: a test pins that `/keys` and the discovery document carry no
  caching header a fronting proxy or CDN could turn into a stale-JWKS
  window.**

  A go-oidc-based verifier — a Kubernetes API server's OIDC authenticator
  (e.g. a managed EKS cluster), or Kargo — keeps its own JWKS cache and
  only refetches on an unknown `kid` once that cache has expired, deriving
  the expiry from `/keys`'s own `Cache-Control`/`Expires` headers. This
  issuer sets neither, so a rotated (or newly re-algorithm'd) `kid`
  verifies on the very next request — confirmed live.
  `TestJWKSAndDiscoveryAreNotCacheableByAProxy`
  (`internal/issuer/jwks_cache_test.go`) now fails the build the day that
  stops being true. See
  [docs/operations/high-availability.md#signing-keys-across-replicas](docs/explanation/high-availability.md#signing-keys-across-replicas)
  for the same rule restated as a deployment concern: nothing in front of
  this issuer may cache `/keys` either.

## v1.36.0

- **Added: `accessctl version` (and `--version`).**

  Prints this build's own version — `accessctl <version>`, `accessctl
  dev` for one built without the release workflow's ldflags — plus the
  commit and build date when the release stamps those too, from the same
  `internal/version` the services already report. `--version` and
  `-version`, as the first argument, do the same; `--json` prints
  `{"version", "commit", "date"}`, including only whichever of those
  this build carries. No network call, no config, no session, no `HOME`
  needed.

## v1.35.0

- **Added: `accessctl bao`, `pg` and `psql` gain `--login-ns` (and
  `$ACCESSCTL_BAO_LOGIN_NAMESPACE`), so an installation that keeps its
  logins at one parent namespace while data lives in per-project
  children can log in at the parent while still operating on the child.**

  A token minted by logging in to an OpenBAO namespace is valid there
  and in its children, never in a sibling — so `-ns=<env>/<project>`
  today has no way to log in anywhere but `<env>/<project>` itself, which
  fails wherever the login mount only exists at `<env>`. `--login-ns`
  (default: the target namespace, unchanged when neither it nor the
  environment variable is set) names where the login happens instead;
  the target must be `--login-ns` itself or a descendant of it (a
  path-segment prefix, not a string prefix: `dev` is not a parent of
  `devel`), refused as a usage error before any exchange otherwise. `bao`
  itself, and the PKI `sign` call `pg`/`psql` make, still run against
  their own target namespace unchanged. The login token cache is keyed
  by the login namespace rather than the target, so two targets sharing
  a parent login (`bao -ns=<env>/a`, `bao -ns=<env>/b`) reuse the same
  login; `--forget` clears the entry at that login namespace. See
  [docs/connect/openbao.md#logins-at-a-parent-namespace](docs/how-to/connect/openbao.md#logins-at-a-parent-namespace).

## v1.34.0

- **Fixed: the "internal groups are declared but nothing consumes them"
  warning no longer names a group a GitHub App catalogue's grant
  consumes, or one only a `groups` override reaches.**

  `policy.Policy.Unconsumed` read `requires` on every client, resource
  and `client_documents`, GitHub org/team bindings, and the hub's own
  roles — but not a GitHub App catalogue's grants, which this package
  cannot read for itself: the catalogue is a deployment's own file,
  loaded by whichever process keeps one (`internal/app`, wired through
  to `internal/issuerapp`; the standalone `internal/githubroster/app`
  controller, which now reads it too, read-only, for this reason alone).
  `Unconsumed` gains a variadic `catalogueGroups` parameter — existing
  callers that pass nothing keep today's behaviour exactly — and
  `internal/githubapp/catalogue.Catalogue` gains `GrantGroups`, its
  `UndeclaredGroups` mirror, for a caller to read its own catalogue's
  grants with. On a real installation this closed about twenty false
  positives: every GitHub-derived group (`<org>:<repo-or-team>…`) that
  existed only to appear in a catalogue grant.

  While in there: a client's, resource's or `client_documents`' `groups`
  override (`docs/decisions/0006-groups-claim-scoped-per-audience.md`)
  is now counted too, by the same rule `Policy.ScopeGroups` applies to a
  live token — `groups: all` consumes every declared group, and
  `groups: [thing, ...]` consumes every declared group of that thing, in
  any scope. This closed a KNOWN LIMITATION `Unconsumed`'s own doc
  comment had carried since that override shipped: a client widening
  what it reads this way, rather than through `requires`, was invisible
  to this warning until now.

- **Added: `accessctl bao <args…>` authenticates to OpenBAO and runs the
  real `bao` binary, unchanged.** `docs/decisions/0013-openbao-access-through-the-bao-cli.md`:
  accessctl stops reimplementing OpenBAO's own features one
  `accessctl credential` kind at a time — the sign-in (or a job's own
  identity) is exchanged for `--audience openbao` and logged in on the
  JWT mount, in the SAME namespace the caller's own `bao` command is
  about to operate in (read from its `-namespace`/`--namespace` flag,
  then `BAO_NAMESPACE`, then `VAULT_NAMESPACE`), and the resulting token
  is handed to `bao` as `BAO_TOKEN` in the child process's environment
  alone — never `~/.vault-token`, never bao's own token helper file. The
  login is cached, one file per OpenBAO address, namespace and subject,
  under accessctl's own config directory; `accessctl bao --forget`
  revokes it and removes the cache entry. accessctl's own flags
  (`--address`, `--ca-cert`, `--issuer`, `--client`, `--audience`,
  `--mount`, `--login-role`, `--forget`) go BEFORE the bao subcommand;
  bao's own flags, including `-namespace`, go after it, exactly where
  bao has always accepted them. See
  [docs/reference/sluisctl-wrappers.md#bao-authenticate-then-run-bao-unchanged](docs/reference/sluisctl-wrappers.md#bao-authenticate-then-run-bao-unchanged)
  and [docs/connect/openbao.md](docs/how-to/connect/openbao.md).

- **Added: `accessctl bao kv get ... -format=env` renders a KV secret as
  dotenv lines**, a stop-gap for the one thing `bao kv get` cannot do yet
  upstream (proposed there under the same rule). A string with none of
  `'`, CR or LF is written `KEY='value'`; any other string is
  `KEY="value"`, with backslash, `"`, `$`, CR and LF escaped; a number or
  boolean is written as its own JSON text; `null` is `KEY=''`; a nested
  object or array, or a key outside `[A-Za-z_][A-Za-z0-9_]*`, is refused
  by name rather than silently mangled or renamed. `-field` combined with
  `-format=env` is a usage error. Detected once, cheaply, against the
  installed `bao`'s own `-format` help: the day it lists `env` on its
  own, accessctl gets out of the way and the call is bao's own answer,
  unchanged.

- **Changed: the `accessctl secrets` refusal now points at `accessctl bao
  kv get`** instead of a bare `bao login` / `bao kv get` recipe, since
  accessctl now authenticates that path itself.
  `docs/decisions/0002-mission-boundary-tokens-and-memberships.md`.

- **Added: `accessctl psql` and `accessctl pg --` mint a Postgres client
  certificate and run a command with libpq's own environment variables
  pointed at it**, replacing `accessctl credential db`.
  `accessctl pg [flags] -- <command> [args…]` authenticates — sharing
  `accessctl bao`'s own login and its cache — mints (or reuses, five
  minutes' margin) a client certificate via `pki/sign/<role>` (`-role`,
  `db-client` by default; `-ns`, `-mount` mirroring `bao`'s own flag
  spelling), and runs `<command>` with `PGSSLCERT`, `PGSSLKEY`,
  `PGSSLROOTCERT` and `PGSSLMODE=verify-full` set, plus `PGUSER` (the
  certificate's own common name) **only when the caller has not already
  chosen one** — an explicit `-U`/`user=`, or a libpq service file's own
  `user=`, still wins. `accessctl psql [flags] [psql args…]` is the
  shorthand for `accessctl pg -- psql [psql args…]`; psql's own
  arguments, including a service file's `service=<name>`, pass through
  unchanged. accessctl no longer writes a `pg_service` entry: a
  repository keeps its own, committed and secret-free, and points
  `PGSERVICEFILE` at it. See
  [docs/reference/sluisctl-wrappers.md#pg--psql-a-postgres-client-certificate-then-a-command](docs/reference/sluisctl-wrappers.md#pg--psql-a-postgres-client-certificate-then-a-command)
  and [docs/connect/postgresql.md](docs/how-to/connect/postgresql.md).

- **Breaking: `accessctl credential ssh|db|client` is removed.**
  `docs/decisions/0013-openbao-access-through-the-bao-cli.md`. Each now
  refuses, naming its replacement: `ssh` → `accessctl bao ssh -mode=ca`
  for an interactive session, or `accessctl bao write -field=signed_key
  <mount>/sign/<role> public_key=@key.pub > key-cert.pub` for scp, git,
  CI and Ansible; `db` → `accessctl psql` / `accessctl pg --`; `client`
  → `accessctl bao write <pki mount>/sign/<role> csr=@your.csr` (an
  `openssl req -new` recipe for the CSR is in
  [docs/connect/openbao.md](docs/how-to/connect/openbao.md)). See
  [docs/connect/ssh.md](docs/how-to/connect/ssh.md) and
  [docs/connect/postgresql.md](docs/how-to/connect/postgresql.md) for the
  full replacements.

## v1.33.0

- **Added: a declared role may restrict itself to some of its thing's
  scopes.** `docs/decisions/0012-per-role-scopes-in-the-vocabulary.md`,
  extending 0010's vocabulary: a role's value in `things.<t>.roles` may
  now be an object (`user: { implies: [...], scopes: [...] }`) alongside
  the plain implies-list form it has always accepted, restricting that
  role to a non-empty subset of its thing's own declared scopes — `ssh`'s
  `user` role valid on `devel` alone even though `ssh` itself also
  declares `core`, `stage` and `prod`. `core:ssh:user` is refused,
  distinctly from a scope the thing itself does not have; a mapping
  wildcard skips a combination the role disallows the same way it already
  skips a thing without the role at all (`*:ssh:user` reaches
  `devel:ssh:user` alone); and an `implies` edge whose target role does
  not cover every scope its source does is refused at load rather than
  silently narrowed. No policy that declares no per-role `scopes` changes
  shape or behaviour. See
  [docs/reference/policy-vocabulary.md#per-role-scopes](docs/reference/policy-vocabulary.md#per-role-scopes)
  and [docs/taxonomy.md#per-role-scopes](docs/reference/taxonomy.md#per-role-scopes).

- **Added: `groupsScoping: enforce` actually narrows a token's `groups`
  claim, and `/userinfo`'s answer, to what report mode has been
  computing since 1.32.0.** Every path that writes `groups` is covered:
  the ID token, the access token (an authorization code, a refresh, a
  token exchange alike), `Storage.MintFor` (the console's own internal
  mint), and — closing the one gap report mode could not, on its own —
  `/userinfo`, scoped by the presented access token's own audience or
  resource, or a caller could recover the unscoped list with one extra
  call. `requires` still gates entry, and a `rung:` group's lifetime is
  still computed, off the FULL held set either way: scoping narrows what
  a token SAYS, never what the issuer computes from what a caller holds.
  Report's INFO line becomes a DEBUG line under enforce, at the same rate
  limit, for turning on when a role goes missing.

  Opt-in: the chart's default stays `report`. `docs/reference/policy.md#groups-in-a-token-scoping`
  and `docs/how-to/turn-enforce-on.md` for how to move
  from report's findings to `enforce`, and how to find a role that went
  missing once it is on.

- **Added: a `groups` override entry may name a two-segment FAMILY
  (`rung`, `emp`), not only a thing.** `rung:<name>` and `emp:<slug>` have
  no thing a pair could match or an outright thing entry could widen, so
  the only way to keep one under scoping used to be naming it outright,
  one exact name at a time (`groups: [rung:sre]`) — unworkable for a
  Kubernetes audience binding every person's own `emp:<slug>` to their
  namespace, where no single entry could name every person's slug up
  front. `groups: [emp]` now keeps every held `emp:` name at once, the
  same way a thing entry keeps every role of it; `groups: [rung]` does
  the same for `rung:`. A declared vocabulary constrains a bare-word entry
  to a declared thing or one of the two known families; an exact
  two-segment name validates either way, unchanged. See
  `docs/reference/policy.md#groups-in-a-token-scoping`.

## v1.32.1

- **Fixed: per-audience `groups` scoping now reads a self-described
  client's own gate, `client_documents.requires`.**

  `policy.Policy.ScopeGroups` fell through to "no gate" for any audience
  that was not a declared client or resource row, which included every
  self-described client (an MCP client or similar, admitted by URL
  through `client_documents`) — even though such a client IS gated, by
  `client_documents.requires`, which every one of them shares. Report
  mode was logging every held group as dropped for these audiences, and a
  future enforce mode would have handed them no groups at all.

  `client_documents` also gains its own optional `groups` override,
  validated the same way a client's or a resource's is, so an
  installation can widen it for every document client at once. An
  audience that matches no gate at all — not a client, not a resource,
  and not a URL `client_documents.origins` permits — still keeps nothing,
  which remains the useful report-mode signal that it needs one of the
  three before enforce mode could narrow anything for it correctly.

  Also notes, for whichever release ships `enforce`, that `userinfo` has
  to be scoped by the token's own audience too: narrowing only the token
  and leaving `userinfo` unscoped would let a relying party read the
  fuller list by calling `userinfo` instead.

## v1.32.0

- **Added: per-audience `groups` scoping, in REPORT-ONLY mode — no token
  changes shape yet.**

  `docs/decisions/0006-groups-claim-scoped-per-audience.md`, refined by
  0010's vocabulary: a token would keep only the held groups whose
  `<scope>:<thing>` pair appears among its audience's `requires` pairs, in
  any role, plus a declared `groups: all` or `groups: [thing, ...]`
  override on that client or resource row. `policy.Policy.ScopeGroups`
  computes this at every place a token's `groups` claim is built — the ID
  token, the access token (authorization code, refresh and a token
  exchange's own claims all converge on one hook), and the console's
  internal mint — and logs ONE line at INFO (audience, client, subject,
  dropped groups) when it would have dropped something, rate-limited per
  (audience, subject, dropped-set) to once every ten minutes. **It is
  never applied**: every token this release mints carries exactly the
  `groups` it always has, byte for byte, proven by tests for the ID token,
  the access token and an exchange alike.

  The chart's new `groupsScoping` value (`GROUPS_SCOPING` in the
  environment) is `off`, `report` (the default) or `enforce`; `enforce`,
  which would actually narrow a token, is **refused at issuer start** in
  this release, by name, so the switch is visible and wired before an
  installation can reach for it. See
  [docs/reference/policy.md#groups-in-a-token-scoping](docs/reference/policy.md#groups-in-a-token-scoping),
  [docs/reference/configuration.md](docs/reference/configuration.md) and
  [docs/how-to/read-the-groups-scoping-report.md](docs/how-to/read-the-groups-scoping-report.md)
  for reading what report mode finds.

- **Added: an optional `vocabulary` table declares which scopes and things
  exist, each thing's role ladder, and which role implies which other
  one — things, scopes, roles, inheritance and mapping wildcards.**

  Opt-in and strict: a policy with no `vocabulary` table changes nothing,
  proven by a test. Declare one, and the policy refuses to load when a
  concrete grant anywhere in the file — a `groups` key, a `claims` key, a
  `lifetimes` key, any `requires`, any GitHub binding — names an
  undeclared scope or thing, a scope the thing does not have, or a role
  the thing does not declare. `rung:`/`emp:` names stay exempt, as they
  always were.

  Roles imply explicitly (`admin: [operator]`, branching where a ladder
  branches) and the graph may not cycle; holding a role also holds every
  role it implies, transitively, on the SAME scope and thing — never
  across scopes, so `all:x:admin` never implies `devel:x:admin`. This is
  applied once, in evaluation, so a client's `requires`, a token's
  `groups` claim and GitHub team reconciliation all see the expanded set
  without knowing inheritance exists: a GitHub team bound to `S:T:viewer`
  is now fed by anyone in `S:T:admin` too.

  A `groups` key may use `*` in the scope and/or thing position —
  `*:k8s:admin`, `devel:*:viewer` — expanding, against the vocabulary,
  into every concrete grant a declared thing has for that scope and role,
  excluding every scope marked `sensitive`. A role wildcard and `*:*:*`
  are always refused, wildcards need a declared vocabulary, and a
  wildcard may only appear as a `groups` key — never in `requires`, a
  GitHub binding, `claims` or `lifetimes`. Nothing past evaluation ever
  sees anything but a concrete name.

  `policy.Result`'s `Held` now carries, per held group, which `groups`
  key matched directly and which concrete group's role implied it, so a
  later console change can explain the chain; the `Unconsumed` lint
  counts a wildcard key as consumed the moment any group its expansion
  names is.

  See [docs/reference/policy.md#vocabulary](docs/reference/policy.md#vocabulary),
  [docs/taxonomy.md](docs/reference/taxonomy.md) and
  [docs/decisions/0010-a-declared-vocabulary.md](docs/decisions/0010-a-declared-vocabulary.md).

- **Added: a person's page states the whole "why do I hold this group"
  chain, not just the group's own name.**

  `Explain`'s `HeldGroup` now carries `granted_by_key` (the `groups`
  table key that matched — a mapping wildcard's own spelling when that is
  what matched, the group's own name otherwise), `wildcard` (whether that
  key is a wildcard), and `implied_by` (the direct parent group, when the
  entry came from inheritance rather than a direct hold). Every existing
  field is unchanged; these are additive, on new field numbers.

  The console renders one line per held group by walking `implied_by`
  hop by hop, client-side, from the already-fetched `Explain` answer:
  `stage:k8s:viewer ← implied by stage:k8s:operator ← implied by
  stage:k8s:admin ← wildcard *:k8s:admin ← directory group
  sre@example.com`.

- **Breaking: the `access-proxy` chart is no longer published.** Gateway-native
  OIDC (a `SecurityPolicy` with `oidc:` on Envoy Gateway) is the replacement
  for a console with no OpenID flow of its own. For a gateway that is not Envoy
  Gateway, run upstream `oauth2-proxy` yourself following the recipe in
  [docs/design/access-proxy.md](docs/how-to/connect/oauth2-proxy.md).

  Versions of the chart already published remain available in the OCI registry,
  so existing pins keep working. See
  [ADR 0003](docs/decisions/0003-deprecate-access-proxy.md) for the rationale.

## v1.31.0

- **Added: the issuer and github-roster warn at load if an internal group is
  declared but nothing consumes it.**

  A group referenced by a client, resource, or GitHub binding is working. A
  group referenced only by `claims` or `lifetimes` is still unused, because
  claims and lifetimes decorate a group and do not consume it. The hub's own
  roles (`operator` and `viewer` on `access-roster`) are not reported, because
  the hub reads them directly from the token. A group with the `rung:` or
  `emp:` prefix is never reported, because these are identities and sessions,
  not grants.

  **Known limitation:** a relying party may read groups beyond what its
  `requires` names, for its own role mapping (a console's viewer vs editor
  role, for example). Such groups are consumed outside the policy's view, so
  this warning may name them. The gap closes when a client can declare what
  it maps — per `docs/decisions/0006-groups-claim-scoped-per-audience.md`.

  The warning reaches an operator at the moment they see their logs, where
  they can check whether the group is a leftover, a typo, or intentionally
  ahead of the client that will use it. It is a warning rather than an error,
  because an installation may legitimately prepare a group before its consumer
  arrives.

- **Added:** the issuer signs with several algorithms at once — RS256,
  ES256 and ES384 — chosen per token by the audience it is minted for,
  rather than one algorithm for the whole installation.

  A client row or a resource row may pin `signing_alg: RS256 | ES256 |
  ES384` in the policy; a row naming none keeps signing the installation
  default (ES384 in the chart, unchanged). This is for the relying
  parties that lag: EKS's associated OIDC identity provider and Kargo's
  verifier both accept RS256 only, and OIDC Core §15.1 expects a provider
  to be *able* to sign with it — previously the only fix was moving the
  *whole* installation's default key to RSA, off every other audience's
  ES384 too.

  `signingKey.additional[]` in the `access-issuer` chart adds a key per
  extra algorithm, each its own cert-manager `Certificate` and `Secret`,
  each on its own rotation track: renewing one never disturbs another's
  schedule, including the default key's. A policy row naming an algorithm
  with no configured key is refused loudly at issuer start, never a
  silent fall-back to the default. The existing single-key
  `signingKey.certificate` shape is unchanged and renders byte-identical
  output.

  See [reference/policy.md#signing-algorithm-per-audience](docs/reference/policy.md#signing-algorithm-per-audience)
  and [ADR 0009](docs/decisions/0009-a-default-signing-algorithm-and-per-audience-exceptions.md).

## v1.30.0

- **An absolute session limit: every session now ends 24 hours after
  sign-in, no matter how often it is refreshed. This is a behaviour
  change.**

  A per-client session slid forward on every refresh — `ExpiresAt =
  now + lifetimes.refresh` — with nothing measuring how long ago the
  person actually signed in. A client that refreshed often enough (a
  proxy, a CLI left running) stayed signed in indefinitely, and
  `auth_time` was carried into every token without ever being checked.

  `lifetimes.absolute` (default `24h`) is the new cap, and a session's
  end is now `min(now + lifetimes.refresh, auth_time + lifetimes.absolute)`
  — decided when it opens and recomputed on every rotation, so a sliding
  refresher plateaus at the limit instead of climbing past it:

  ```yaml
  lifetimes:
    refresh: 12h
    absolute: 24h # may be shorter OR longer than refresh; the earlier wins
  ```

  **Three things enforce it.** A refresh presented at or after the limit
  is refused (`invalid_grant`) and the session is revoked, with its own
  audit reason distinct from an ordinary inactivity timeout. An access or
  ID token's `exp` is capped the same way, even when the ordinary token
  lifetime would reach further — a session that has just hit the limit
  must not go on answering `userinfo` for whatever was left of its last
  token. And a **single sign-on** session whose `auth_time` is past the
  limit is ended outright — the same cascade `/logout` runs, Back-Channel
  Logout included — rather than answering a silent `/authorize`, which is
  what would otherwise let a browser left open renew its sign-in one
  client at a time forever.

  **Unaffected:** a workload or a machine trading a proof through token
  exchange authenticates nobody, carries no `auth_time`, and has nothing
  to measure the limit against — it keeps its ordinary refresh window,
  exactly as before. The console's own session is capped the same way,
  bounded by whichever of its own `directory.sessionLifetime` and the
  absolute limit is shorter.

  **Refused at render and at start**, not defaulted, if `lifetimes.absolute`
  is zero, negative, or shorter than `lifetimes.token`: a session cannot be
  limited to less time than its own first access token needs to live.

- **Fixed: the signing key rotates without a restart or a verification
  gap, and changing its algorithm is now just a rotation.**

  cert-manager's `rotationPolicy: Always` makes every renewal a new key,
  but the issuer only ever read `SIGNING_KEY_FILE` once at start. A
  renewal took effect only at the next restart; a rolling restart between
  it and then left replicas signing with different keys, so roughly half
  of every verification failed depending on which replica's JWKS was
  fetched; and once every replica had restarted, every token the previous
  key had signed failed at once, because the JWKS never kept it published
  after.

  The issuer now polls the mounted file and keeps a key ring: a newly
  seen key is published in the JWKS immediately, but this replica signs
  with it only once every other replica has had time to notice and
  publish it too — by default 15 minutes, long enough that verifiers'
  JWKS caches (Envoy's default is 10 minutes) and Secret projection delay
  have all passed — and the key it replaces stays published for at least
  as long as a token it signed can still be presented plus 5 minutes for
  clock skew before it retires. The schedule is decided once and shared
  through Valkey when one is configured, so a replica that restarts
  mid-rotation does not forget a key still inside its overlap; with none,
  it is kept in memory, which is right for one replica and a local run.
  **Changing `signingKey.certificate.algorithm` — RSA to ECDSA, or back —
  is now safe as just a rotation**: the JWKS and discovery's advertised
  algorithms carry both kinds for the overlap, then only the new one.

- **Breaking: `accessctl secrets` is removed.**

  Reading a team's shared values back out of a secret store's KV engine is
  the store's own job, once it can authenticate a person by the issuer's
  token. A courier command here duplicated a client the store itself should
  ship.

  **The replacement:** The store's own client. With OpenBAO: `bao login
  -method=oidc` then `bao kv get <path>`. With External Secrets in a
  cluster: write a SecretStore with the issuer as an auth provider. See
  [docs/decisions/0002-mission-boundary-tokens-and-memberships.md](docs/decisions/0002-mission-boundary-tokens-and-memberships.md)
  for the reasoning.

  Running `accessctl secrets` now fails immediately with a message naming
  the version and the replacement.

- **Breaking: the console's secret-store view and the `SecretManagerService` API
  are removed.**

  The console's Secret stores page displayed OpenBAO's policies, groups and
  aliases with a reader grant the issuer held. This is out of scope for
  access-roster, which delivers tokens and memberships, not authorization for
  other systems. See [docs/decisions/0002-mission-boundary-tokens-and-memberships.md](docs/decisions/0002-mission-boundary-tokens-and-memberships.md).

  **Migration:**

  1. Delete the `secretManagers` block from your deployment values.
  2. Revoke the reader grant you gave the issuer in OpenBAO: the policy
     `sys/policies/acl/*`, `identity/group/name*` and `sys/auth` — see
     [docs/connect/openbao.md](docs/how-to/connect/openbao.md) for the full path list.
  3. Delete the secret stores page from your console bookmarks.

  **Sign in to OpenBAO stays unaffected:** the issuer still hands OpenBAO a
  bearer from its own token, and people still sign in with OpenBAO's own OIDC
  login or the issuer as a broker.

  **Values validation:** if `secretManagers` is still set, the chart render fails
  with a message that repeats this guidance. `SECRET_MANAGERS_FILE` at the issuer
  raises an error on start-up, with the same message. A deployment trying to activate
  the feature is clearly intentional, not silent drift.

## v1.29.0

- **A token can be minted for a resource, not only for the client asking
  — and a `resource` a client sends is now refused rather than ignored.**

  A client's id was always the audience, which held while the client and
  the thing a person reached were one object: sign in to Argo CD, get a
  token for Argo CD. It stops holding as soon as they are not. A Model
  Context Protocol client is somebody's editor; what it wants a token for
  is a service elsewhere, and `aud` naming the editor is both untrue and
  useless — a service pinning `aud` would have to pin the name of every
  editor that might call.

  The new `resources` table declares what a token may be for. A client
  names one with the `resource` parameter (RFC 8707) and `aud` becomes the
  resource:

  ```yaml
  resources:
    https://mcp.example/:
      requires: [prod:k8s:admin]
      ttl_cap: 5m
  ```

  **The two gates compose**: a client's `requires` says who may use that
  client, a resource's says who may reach that service, and a caller must
  satisfy both. Both `ttl_cap`s apply and the shorter wins.

  **`resource` was previously ignored, which was the worse half of this.**
  The library models no such field on an authorization request and its
  decoder drops unknown parameters, so a client asking for a token scoped
  to one service was handed one scoped to itself and told nothing — and
  the failure surfaced later, somewhere else, for a reason nobody connects
  to the request. It is now validated before the library sees it and
  refused with `invalid_target` as RFC 8707 says: for a resource this
  installation does not declare, for a relative URI or one carrying a
  fragment, and for more than one resource at a time.

  **Nothing changes for a client that names no resource** — its own id is
  still the audience, so no relying party needs to re-pin anything.

  **The session remembers which resource it was opened for.** A refresh
  carries a token and nothing else, so without that the renewed token
  would be minted for the *client* while the original named a resource —
  silently changing what the token is for halfway through a session, with
  the resource's gate unchecked for the rest of its life. It is re-checked
  on every refresh, which is where a withdrawn grant actually bites.

- **A client may register itself by serving a document, instead of needing
  a row in the policy.** Software this installation does not deploy and
  cannot enumerate — an editor, a hosted assistant, the clients of the
  Model Context Protocol — presents an HTTPS URL as its `client_id`, and
  that URL serves a JSON document describing it (an OAuth Client ID
  Metadata Document). The issuer fetches it, validates it, and treats the
  client as `public`. Nothing is registered and nothing accumulates.

  **Off unless an origin is named**, and the new `client_documents` block
  is where that happens:

  ```yaml
  client_documents:
    origins: [clients.example]
    requires: [rung:engineering]
    ttl_cap: 5m
  ```

  `requires` is **mandatory** alongside `origins`: turning the mechanism
  on without saying who may use it would admit every person who can sign
  in at all, which is not a decision anybody makes on purpose.

  Why an allow-list of origins rather than a refusal of unknown clients:
  registration is not the authorization decision here. Reach is decided by
  the groups a caller holds, so a client this issuer has never seen cannot
  widen anything — it can only ask a person to consent to the reach that
  person already has. A document may ask for a different `kind`, a longer
  cap or a wider `requires`; none of those fields is read. The threat is
  therefore phishing rather than escalation, and the allow-list is what
  bounds it.

  **A declared client always wins.** The policy is consulted first, so
  nothing is fetched for a client already in the file and a document
  cannot displace one. Existing clients are unaffected.

  The refusals, each with the failure it prevents, are in
  [reference/policy.md](docs/reference/policy-clients.md#clients-that-describe-themselves).
  Two worth naming here: a document whose `client_id` is not the URL it
  was served from is refused, because otherwise a document at any
  allow-listed host could claim to be any client; and a document that
  cannot be fetched fails the flow rather than falling back to a cached
  copy, because a cached copy is redirect URIs the client may have
  retired.

  `client_id_metadata_document_supported` is advertised in discovery only
  while an origin is named — a client reads it to decide whether to
  present a URL at all, so advertising it on an installation that admits
  none would send every such client into a refusal.

  Dynamic Client Registration is **not** implemented and is not planned:
  the Model Context Protocol deprecated it in its 2026-07-28 revision in
  favour of these documents, and its own failure mode is registrations
  that accumulate with nothing to review them against.

## v1.28.0

- **`accessctl` holds a session per issuer, so one laptop can be signed in
  at two estates at once.** There was a single `session.json`, so signing
  in at the second installation replaced the first one's refresh token.
  `--issuer` then selected the right endpoint and handed it the **wrong**
  token, which the issuer refuses as `subject_token is invalid` — a
  message that reads as expiry, and which sent people to re-run a login
  that had already worked. The sign-in now lives in
  `<config>/sessions/<issuer>-<hash>.json`, one file per installation.
  Nothing else moves: `config.yaml` still names the default issuer, a bare
  `accessctl whoami` behaves as before, and every context `kubeconfig`
  writes has always carried its own `--issuer` — the plumbing was already
  multi-issuer and only the login cache was not.

  Separate files rather than one file holding them all, for a second
  reason: more than one process writes here — every `kubectl`, every
  provider of a Pulumi stack — and with the sessions together, two exec
  plugins for **different** estates would rewrite the same file and one
  would lose its token. The refresh lock moves with the file, so a busy
  estate no longer makes the other one wait.

  The name is derived from the issuer so the directory is readable, and
  the issuer is stored **inside** the file: that is what a read checks, so
  a name that collides fails closed as *not signed in* rather than opening
  a session at the wrong estate. **No one signs in again** — an existing
  `session.json` is adopted by the first issuer that asks and replaced by
  the next `login`.

## v1.27.1

- **1.27.0's grace window was never reached, and this is the release that
  delivers it.** The refresh endpoint reads the token twice — the library
  resolves it to a request before it asks for new tokens — and the window
  was only in the second of those readings, so a replay was refused by the
  first and the window never ran. The one thing 1.27.0 changed in a running
  installation was the wording of the refusal. A deployment that took
  1.27.0 for the concurrent-refresh fix does not have it: a gateway that
  refreshes per request, or a page that opens several calls at once, still
  loses the session to its own concurrency. Both readings now resolve
  through the window, so a refresh token replayed within thirty seconds of
  being spent is answered with the successor the winning refresh produced,
  as 1.27.0 said it would be. `ByToken` is unchanged and still exact —
  revocation and listing are asking whether a token is the live one, which
  is a different question — and revocation resolves through the window too,
  so a caller revoking with a token a refresh rotated a moment earlier
  still ends the session rather than being refused and leaving it open.
  Nothing a caller passes or reads changed: upgrading from 1.27.0 is a fix
  and nothing else.

## v1.27.0

- **Breaking: `@truvity/access-roster/server` verifies with the algorithms
  the issuer advertises.** It pinned RS256, and the chart has signed with
  ECDSA P-384 by default since 1.19.0, so a Node service built per the
  documentation against a default installation rejected every token. The
  verifier now reads `id_token_signing_alg_values_supported` from
  discovery, falls back to the four the issuer can sign with (RS256,
  ES256, ES384, ES512) when nothing is advertised, never accepts `none`
  or an HMAC, and takes an `algorithms` option to narrow. A caller that
  relied on the RS256 pin against an issuer advertising more now accepts
  more; narrow it if that matters.
- **Every relying-party guide says which algorithm it needs.** kube-apiserver's
  `--oidc-signing-algs` defaults to RS256 and must name ES384 for a default
  installation; ArgoCD follows discovery; **Kargo is RS256-only** at the
  source, so an installation Kargo signs into must set
  `signingKey.certificate: {algorithm: RSA, size: 2048, encoding: PKCS1}`;
  IAM accepts ES384. The README's worked example says it ships ES384.
- **A concurrent refresh gets the winner's token, not a refusal.** Rotation
  spent the refresh token the moment it was presented, so of several
  refreshes carrying the same token exactly one could succeed and the rest
  were told the token is not live — the answer meant for a thief — which a
  relying party reads as a dead session. A gateway that verifies per request
  presents one spent token several times inside a second. Inside a short
  grace window a replay is now answered with the successor the winning
  refresh produced: the loser holds exactly what the winner holds, and the
  token it offered is never minted, so there is still one live credential.
- **The reference pages catch up with 1.17 to 1.25.** The configuration
  reference gains every chart key added since 1.17 — the signing-key
  certificate, the route's private key, both push blocks, the whole
  `secretManagers` section, `serviceAccount.annotations`,
  `networkPolicy.gatewayNamespace` — with the objects and environment it
  renders; the contracts page gains `SecretManagerService`; the accessctl
  reference documents the token caches and lock 1.25.1 added; the OpenBAO
  guide follows 1.24 and 1.25; the proxy reference gains
  `exposure.parentRefs`; the runbook's rotation section now states the
  per-Secret restart rule the chart's values state, replacing two
  instructions that did not work.

## v1.26.0

- **The audit trail is an installation of this service's own.** Every
  record goes to an installation of
  [truvity/audit](https://github.com/truvity/audit) rendered beside this
  service, in its namespace, at the address `audit.writer` names — which
  also takes the catalogue this service registers at start — and the
  console's Audit page reads that installation's query service
  (`audit.query`) with a token this issuer mints for the person signed in.
  This service holds no bucket, no keys and no cloud identity for the
  trail. What it keeps is the vocabulary: `internal/audit/catalogue/roster.yaml`
  declares thirty-seven actions, one constructor each, and
  `just audit-catalogue` holds the code to the document.
- **Breaking: the chart refuses `audit.s3.*` and `audit.maxEvents`**, and
  the `AuditService` and `AuditSinkService` RPCs are gone with the trail
  they served. The GitHub controller records as itself, with its own
  projected token, so the `all:access-roster:reporter` group it used to
  report through is not read; a policy may drop it. Objects the old trail
  wrote under `events/` are not migrated and age out under their lock.
- **Delivery is a bounded queue in memory, not an outbox on disk.** A
  record goes on the queue before the request completes and is retried
  with backoff until the writer takes it; past the queue's bound the oldest
  are dropped, counted (`audit.emit.records.dropped`) and each one logged
  by id at Error. A restart loses what the queue holds. The one exception
  stays: a recovery sign-in is `block`, kept before it succeeds and refused
  when it cannot be.
- **A refused catalogue ends the process**, whether at start or, after an
  unreachable start, on the retry that first gets an answer — a service
  whose records nobody accepts does not run for the life of the pod keeping
  nothing. A token the installation does not trust is said at Error naming
  the token file, rather than as "could not be reached" until the queue
  gives up.
- **`audit.forwardedForTrustedHops` counts the way the audit emitter
  counts**: X-Forwarded-For read left to right with the connection's peer as
  the last entry, so the peer is one hop. A gateway alone is 1; an edge
  that appends the client and a gateway that appends the edge's connector
  is 2 — where it used to be 1. A deployment with one proxy that appends
  the client, which the old count could not express, now records the
  client.
- `RecordDurable` refuses an action the catalogue does not declare `block`,
  because for any other it would return at enqueue and the caller would
  have been told a durability it did not have.
- Pinned at the component's v0.2.5, in the Go module and the console's
  package; the documentation describes the trail as it now is.

## v1.25.2

- `accessctl`'s token-cache lock builds on Windows too.

## v1.25.1

- **`accessctl` no longer signs the operator out of every audience when two
  of its processes run at once.** A refresh spends the refresh token — the
  issuer rotates it so a stolen one is good for a single use — and a
  credential process is not called once: every AWS provider a tool starts,
  and every kubectl, ran its own refresh, the first to finish rotated the
  token, and every other was left holding one the issuer refused, reported
  as "not signed in". `kube-token` and `aws` now keep a per-cluster and
  per-profile token cache under the config directory, take a lock around a
  refresh, and every command presents the issuer's own access token,
  kept in `session.json` beside the refresh token, while it lives.
- The chart's values say which of the Secrets it writes need a restart to
  rotate: none of them is watched, and each is read at a different moment.

## v1.25.0

- **A group's door twin is one row, not drift.** A store holds one alias
  per identity group, so admitting a group at two doors takes two
  identity groups — the bare name, and `<name>@<door>` carrying the same
  policy. The page drew them separately, which reported every group in
  the installation twice, the second time as drift nobody declared. They
  are now one row carrying both doors, which is what the column was for.
- **Only the groups the store holds a policy for are expected.** A
  cluster's tier, a CI job's group, the console's own: all of them are
  groups of the environment, and no secret store holds a policy for any
  of them. Reported as "not applied yet" they sent a reader looking for
  an apply that was never going to make them. The list is now the
  store's own exchange audience — a group is admitted to it exactly when
  the store holds a policy for it — so the page reads the same list the
  issuer enforces.
- **A group that spans environments is legitimate wherever it appears.**
  An `all:`-scoped group belongs to no single namespace: drawn as
  expected it was missing from every one of them, and left out entirely
  the installation's own reader appeared as drift. It is now shown where
  the store has it and never reported absent.

## v1.24.0

- **`secretManagers[].caCertConfigMap`** names a store's private root in
  a ConfigMap instead of a Secret. A root is not a secret, and
  cert-manager's trust-manager distributes one as a ConfigMap into every
  namespace — an installation that has it there had to copy it into a
  Secret to use it here. Name one of the two, never both: two bundles at
  one path is one of them silently unused, and the render refuses it.

## v1.23.0

- **The console shows a secret store.** A deployment declares one in
  `secretManagers:` — its address, the door to log in on, and which
  namespaces mirror which environments — and the console draws, per
  namespace, every group the deployment declares beside what the store
  holds, what each group's policy opens, and which doors admit it. A
  person's page gains what their own groups reach. `ListSecretManagers`,
  `GetSecretManagerNamespace` and `ListSecretManagerReach`, all viewer.
- **Read-only, and not by omission.** A store's desired state is written
  where you write it and applied by whatever applies it; a console that
  also wrote would make two custodians of one thing. No call here takes a
  write path, and the reader is granted the declared state — policies,
  identity groups, their aliases, the auth mounts — and **no KV path at
  all**, so a page built on this cannot show a secret's value however it
  is later changed. A drift row links to where the fix is made.
- **Four states, because a refused read is not an empty namespace.**
  `bound`, `not applied yet` (declared and not in the store — not drift:
  this side is already right), `not declared` (in the store and declared
  by nothing, the row worth opening the page for) and `cannot read`. The
  last is never inferred from an empty answer: the two are identical
  except in the status of the call, and reporting one as the other sends
  somebody to look at an apply that is fine.
- **The reader is this service's own ServiceAccount**, projected for the
  exchange audience and traded at the issuer for the store's audience
  exactly as a CI job's token is. No second credential, nothing stored,
  and the file is read per call because the kubelet rotates it under the
  pod. Give that identity `list` on `sys/policies/acl`,
  `identity/group/name` and `identity/group-alias/id`, `read` on each of
  their children and on `sys/auth`, and nothing else
  ([docs/connect/openbao.md](docs/how-to/connect/openbao.md)). An
  exchange it is not granted is drawn on the page, naming the audience,
  rather than raised as an error.
- **`secretManagers[].caCertSecret`** mounts a PEM bundle trusted in
  addition to the system's roots, for that store alone.

## v1.22.0

- **`accessctl secrets env` fetches the values a team shares while it
  develops.** `accessctl secrets env --namespace staging --prefix
  orders/local-dev/checkout --out .env` exchanges the sign-in for
  `openbao`, logs in on the same JWT mount `credential` uses, walks a KV
  version 2 prefix to its leaves and writes one `KEY=value` line per leaf,
  `0600`, by rename. Nothing new is granted: reading a project's prefix is
  its `{env}:{project}:viewer` group, writing it is `deployer` and
  `approver`, and a `403` says so by name instead of repeating "permission
  denied". A run that reads **zero** keys fails rather than writing an
  empty `.env`, which is the failure nobody notices. Only the key names
  are printed, never a value.
  [docs/connect/openbao.md](docs/how-to/connect/openbao.md),
  [docs/reference/sluisctl.md](docs/reference/sluisctl.md).

## v1.21.0

- **`access-issuer` keeps a recovery copy of the two Secrets nothing can
  re-deliver**, and `accessctl kubeconfig` writes `interactiveMode` on the
  entries it generates, without which kubectl refuses them.

## v1.20.2

- A pushed credential now declares the fields the `PushSecret` CRD
  defaults, so a deployment comparing desired against live stops reporting
  drift on a resource that is working.

## v1.20.1

- Shared CI v3.0.1.

## v1.20.0

- **`serviceAccount.annotations`** on the `access-issuer` chart, which is
  how a cloud identity reaches this service: an admission webhook — EKS Pod
  Identity, GKE Workload Identity, or the self-hosted
  `amazon-eks-pod-identity-webhook` — reads the annotation and injects the
  credentials into every pod using the account. The chart mounts no
  credential of its own and takes none as a value.

## v1.19.0

- **Breaking: the signing key is an EC P-384 key, and the issuer signs
  ES384.** The algorithm follows from the key rather than being declared
  beside it — an RSA key signs RS256, and P-256, P-384 and P-521 sign
  ES256, ES384 and ES512 — and it is what
  `id_token_signing_alg_values_supported` advertises, so **every relying
  party has to accept it before you upgrade**. Check each one; a verifier
  built on a library's defaults is the one to look at, because several
  default to RS256, ES256 and PS256 and would reject ES384 with a message
  that names the algorithm rather than the token. To stay as you were, set
  `signingKey.certificate: {algorithm: RSA, size: 2048, encoding: PKCS1}`
  before upgrading and nothing changes. Upgrading rotates the key either
  way: tokens signed with the previous one stop verifying once the old
  public key leaves the JWKS, so do it when a token lifetime of downtime
  for in-flight tokens is acceptable.
- **`signingKey.certificate.algorithm`, `.size` and `.encoding` are
  values.** `RSA` or `ECDSA`, sizes per algorithm (ECDSA 256, 384 or 521;
  RSA 2048, 3072 or 4096), `PKCS1` or `PKCS8`. A combination that
  cert-manager would decline is refused at render with the reason, rather
  than issuing a Certificate that never becomes a Secret and a listener
  that stays dark. An installation reading a key from
  `signingKey.existingSecret` is unaffected and may hand over either kind.
- **`route.certificate.privateKey` is passed through to the route's
  Certificate.** Empty by default, so nothing changes; set it when the
  issuer signs only one kind of key. A PKI role pinned to an algorithm
  refuses the request at issuance, long after the render succeeded, with
  the reason on the CertificateRequest where nobody is watching.
- **Verifiers accept what the issuer advertises.** The `identity` package
  and the GitHub verifier take their algorithms from the issuer's
  discovery document instead of the library's default, so an issuer
  signing with P-384 or P-521 is verifiable by a consumer that did not
  have to be told.

## v1.18.0

- **A default set of GitHub Apps, shipped as values to copy.** Every
  estate needs the same few automations, and every estate has been
  building them by hand. `charts/access-issuer/examples/github-apps.yaml`
  declares four — `renovate-public` and `renovate-private` (split so the
  App whose pull requests are world-readable cannot reach a private
  repository; the installation is what enforces it), `ci-automation`
  (approves bot pull requests so a required-approvals rule is satisfied
  without a person rubber-stamping a version bump, and cuts release
  tags), and `iac` (a Pulumi or Terraform program that manages the
  organisation) — each with the permissions it needs and why. It is
  values to read and copy, not a default the chart applies: creating an
  App stays an owner of the organisation confirming a manifest. The
  guide's new [*A default set*](docs/how-to/connect/github-apps-catalogue.md#a-default-set)
  says why they are four identities and not one.
- **A catalogue App's credential can be projected to a secret store.**
  An entry may carry `push: {secretStore: {name, kind}, remoteKey,
  refreshInterval, deletionPolicy}`, and the chart renders an External
  Secrets `PushSecret <release>-github-app-<id>` that copies exactly that
  App's three property keys — `app_id`, `installation_id`, `private_key`
  — to the path the operator names. Off unless an entry carries it; the
  chart invents neither store nor path; the record and every other App's
  keys stay where they are. It is refused at render when two entries
  share one path in one store, or without `directory.store: kubernetes`.
  This is for a consumer that cannot ask the issuer at the moment it runs
  — an infrastructure-as-code apply that must work while this service is
  upgraded or restored. Everything that *can* ask should still exchange a
  token per run and hold nothing. **What lands in the store is a real
  credential**: the App's private key, a second durable copy, to be
  rotated as one, and the store that holds it is in the App's blast
  radius.
- **New guide:
  [connect/infrastructure-as-code.md](docs/how-to/connect/infrastructure-as-code.md)**
  — the line between the two sides (this service owns identities and
  credentials; the program owns structure and names identities), a worked
  Pulumi program in Go that reads the credential from the store and
  builds a GitHub provider with it, the Terraform equivalent, the
  run-time exchange a CI job should prefer instead and when to choose
  which, and the rotation procedure for the copy.

## v1.17.0

- **Breaking:** anything that selects the service's objects by their old
  kind label — a backup, a script, a dashboard — must switch to the new
  one with this upgrade. **The service's own label keys are under the
  project's prefix.** Every ConfigMap and Secret the service writes is
  labelled `access-roster.truvity.github.io/kind`, and a workspace record
  is annotated `access-roster.truvity.github.io/workspace-id`, in place
  of the `directory-roster.truvity.com/` keys earlier releases wrote.
  `app.kubernetes.io/managed-by` and `app.kubernetes.io/part-of` are
  unchanged.
  - **Upgrading needs no step.** At start, before it reads anything, the
    service moves every object of its release (`managed-by` and
    `part-of` its own) from the old keys to the new ones, by a merge patch
    that two replicas can apply at once; a start with nothing to move
    writes nothing. While a rolling upgrade still runs replicas of the
    older release, which keep writing the old keys, the move repeats
    every minute. A start that cannot move them stops, rather than serve a
    directory missing the workspaces it could not see.
  - **Upgrade note — rolling back past this release needs a manual
    reverse relabel.** An older release reads only the old keys, so after
    the rollback it sees no connected workspace. Once no replica of this
    release is left running (it would move them straight back), run, with
    your namespace and release name:

    ```sh
    kubectl -n <namespace> get configmap,secret -o json \
      -l 'app.kubernetes.io/part-of=<release>,access-roster.truvity.github.io/kind' \
      | jq '.items[].metadata |= (.labels["directory-roster.truvity.com/kind"] = .labels["access-roster.truvity.github.io/kind"] | del(.labels["access-roster.truvity.github.io/kind"]) | if .annotations["access-roster.truvity.github.io/workspace-id"] then .annotations["directory-roster.truvity.com/workspace-id"] = .annotations["access-roster.truvity.github.io/workspace-id"] | del(.annotations["access-roster.truvity.github.io/workspace-id"]) else . end)' \
      | kubectl replace -f -
    ```

    then restart the service (`kubectl rollout restart`), which reopens
    stored workspaces only at start.

- **The repository runs a leak canary.** Every pull request is scanned
  for particulars (account ids, ARNs, internal hostnames, credential
  prefixes) in tracked files; the few mechanism shapes it allows (AWS's
  documented example account ids, ARNs composed from inputs, the pod
  secrets mount root) are listed with their reasons in
  `hack/leak-canary.sh`.

## v1.16.3

- **`access-proxy`: an `exposure.routes` entry with no `backend` at all
  gets the chart's own message.** It failed the render with `nil pointer
  evaluating interface {}.name`; it now fails with the same `route "…"
  needs backend.name, or attachRouteName …` as an entry whose `backend`
  has no `name`. Only the error changed: every values file that rendered
  still renders the same.

## v1.16.2

- **`accessctl credential` trusts a private root for OpenBAO when told
  to.** The connection to OpenBAO verified against the system's roots
  only, so an installation serving its API under a private root could
  not be reached from a machine whose system did not trust that root —
  short of pointing `SSL_CERT_FILE` at the bundle, which replaces the
  roots of every connection the command makes. `ssh`, `db` and `client`
  now take `--ca-cert <file>`, a PEM bundle, or read `BAO_CACERT` (then
  `VAULT_CACERT`), the flag first. The bundle is **added** to the
  system's roots and used for the OpenBAO connection alone: the exchange
  at the issuer keeps the system's trust.
  - **A bundle that trusts nothing is refused**: one that cannot be read,
    or holds no PEM certificate, exits 2 before anything is exchanged,
    rather than being ignored.
  - **An untrusted OpenBAO certificate says how to trust it**: the error
    names `--ca-cert` and `BAO_CACERT`.

## v1.16.1

- **`accessctl credential db` and `client` make the key on this machine
  and never send it.** v1.16.0 called `pki/issue/<role>`, which has the
  manager generate the private key and return it in the response — and a
  role that offers only `sign`, as a role that keeps keys off the wire
  does, refused it. Both kinds now generate an ECDSA P-384 key locally,
  send a CSR to `pki/sign/<role>` (the common name, and any URI SANs, in
  the CSR and in the request, so `use_csr_common_name` and `use_csr_sans`
  work either way), check the certificate that comes back is for that
  key, and write the key `0600` where the issued one used to go: the
  files and their names are unchanged. A role must accept `key_type: ec`
  with `key_bits: 384`, or `any`.
- **`accessctl credential` works in the environment's own namespace by
  default.** v1.16.0 defaulted `ssh` and `client` to `platform/<env>` and
  `db` to `<project>/<env>` — a layout the command assumed rather than one
  an installation had, so the shortest command logged in on a namespace
  that did not exist and every caller had to add `--namespace <env>`. The
  default is now `<env>` for all three kinds: `--env staging` works in
  `staging`. `--namespace`, `BAO_NAMESPACE` and `VAULT_NAMESPACE` still
  override it, in that order.
  - **`db --project` is optional.** Without it the database role is looked
    for in the environment's namespace; with it, in `<project>/<env>` as
    before, so a command line written for v1.16.0 means what it meant.
  - **`credential ssh --help` says what the two roles are for**: `user`,
    the default, for everyday logins as a host's ordinary account; `admin`
    for the account that administers it, granted separately and signed
    only when asked for by name.
  - **A missing OpenBAO address says how to give one** — the `--address`
    flag or `BAO_ADDR` (`VAULT_ADDR` is read too), each with the shape of
    a value — and the flag's help names both variables.

## v1.16.0

- **`policy.SplitGroup` reads any grant back into scope, thing and role.**
  The shape `<scope>:<thing>:<role>` was documented in trust.md and taken
  apart in one place, `SplitScopedGroup`, which only answered for this
  hub's own two roles. A relying party keying its authorization on the
  scope or the role — the audit log granting `<tenant>:audit:viewer` — had
  no reader to import and would have written a second one. Now there is
  one; `SplitScopedGroup` is a thin reading of it. The scope comes back as
  written, `all` included, because what `all` means is the reader's:
  this hub reads it as the absence of a scope, an audit log as every tenant.
- **`accessctl credential`: short-lived SSH, database and machine
  certificates, minted by OpenBAO and keyed by the roster subject.** One
  command family, three kinds — `accessctl credential ssh|db|client --env
  <env> [--role <role>]` — and one sentence behind all three: the sign-in
  (or the job's own token) is exchanged for `aud=openbao`, that token logs
  in on the JWT mount in the environment's namespace, and **one** call is
  made, `ssh/sign/<role>`, `pki/issue/db-client` or `pki/issue/client`.
  Three records then name the same subject: the issuer's audit trail for
  the exchange, OpenBAO's for the signing, and the server's own log for
  the certificate's `key_id` or common name — which the command prints,
  because a credential nobody can find afterwards is a credential nobody
  can investigate.
  - **The CLI decides nothing about the credential.** No request carries a
    TTL: the role's `ttl` and `max_ttl` are the whole answer, so
    shortening a role shortens every credential in flight and no flag here
    can ask for longer. What is sent is a public key, and the principals
    or common name that were asked for; a role that will not sign them
    refuses, which is where that decision belongs.
  - **The OpenBAO token is never written anywhere.** It lives in memory
    for one command and is revoked on the way out — and a mount handing
    out batch tokens refuses that revoke, which is not an error: such a
    token cannot be revoked, it was never on disk, and it expires on its
    own.
  - **Delivered where each kind is actually used.** SSH: a key pair
    generated for this one certificate, added to the **ssh-agent** with a
    lifetime the certificate decides, so the agent forgets it exactly when
    it stops working; or, with `--identity`, written into `~/.ssh` with
    the certificate beside the key where `ssh -i` finds both — and never
    over a key this tool did not write. Database: the files, plus a
    **psql service entry** whose `user` is the certificate's common name,
    for `psql "service=<name>"`, one block per service so a second
    database does not delete the first. Machine: the certificate, its key
    and the chain at the path the caller named.
  - **It works unchanged in a job**, like `kube-token`, `aws` and `token`
    before it: with the Actions variables set it exchanges the job's own
    identity token instead of a sign-in.
  - The OpenBAO roles this calls are the other half of the arrangement and
    are deployed separately; the command is proven here against a fake
    installation that signs and issues for real.

## v1.15.0

- **An App's recent tokens load.** The *Recent tokens* section of a GitHub
  App's page asked the audit trail for its own events — kind
  `github.token.minted`, target `github-app:<id>` — and narrowing the trail
  that way reads the objects of hour after hour, one by one, to find the
  handful that match. Measured against a real installation the call was
  still running fifteen seconds later when the gateway gave up on it, while
  the same listing unfiltered came back at once; the page had been shipped
  with an apology in place of the table. The service now keeps the last ten
  requests of each App beside the half that mints them, and the page reads
  those: when, who asked and how they proved it, the grant, the
  repositories and permissions, and whether it was minted, refused or
  failed, with the refusal's reason in the tooltip. A page load, not a
  scan.
  - **It is memory, not a record, and says so.** The ring is this replica's
    own, bounded per App, and a restart forgets it — which the page prints
    under the table and in place of the table when there is nothing yet
    ("No token has been asked for since this service started, 3h ago"),
    because "nothing has been asked for" on its own would be a claim about
    all time that it cannot make. The audit trail remains the record and
    holds every request for as long as the bucket does; **the token itself
    is still in neither**.
  - **A refusal is a row.** The refusals are usually why the page is open,
    so they are shown as refused with the reason the caller was given,
    rather than being the events that are hardest to find.
  - **A section that cannot be read says so in words.** Where the Apps
    cannot be read, or the deployment mints nothing here, the section
    prints the sentence rather than an HTTP code — and never an empty
    table, which reads as a quiet week.
- **The Audit page can be narrowed by its address.** `#/audit` now takes
  `source`, `kind`, `subject` and `target` as query parameters, and
  narrowing the page by hand changes the address. So *Recent tokens* links
  to the whole trail for that App — and any narrowing an operator is
  looking at is a link they can send.
- **A group's page says when each grant it holds was last used.** From the
  same memory, so the page still costs no call to GitHub and reads no
  store. A grant with nothing beside it is one nothing is remembered for,
  never one known to be unused.
- **A demonstration run has tokens to show.** `DEMO=1` seeds one App's
  recent requests — mints by a workflow, a person and a workload, a
  refusal outside the grant and a failure at GitHub — and leaves another
  App with none, which is the other thing the page has to say well.

## v1.14.0

- **One API for every GitHub App.** The console's Apps list and Apps pages
  were one list assembled in the browser out of four unrelated answers —
  a link App, an organisation's connection, a table of runner Apps and a
  table of catalogue Apps. The service now answers the question the page
  asks: `ListGitHubApps` returns every App this deployment keeps a key for
  or is declared to, in one shape, and `GetGitHubApp` returns one of them
  by the id its page is addressed by. Nothing about the pages changes;
  what changes is that the ids, the states and the words for them are the
  service's, so anything else that asks — a script, a second console — is
  told the same thing.
- **The service's own Apps are checked against their declaration, like the
  catalogue's.** The link App, each organisation's controller App and each
  tier's runner App are created from a manifest this service writes, and
  until now nothing ever compared that manifest with what GitHub holds. An
  owner could edit the permissions of an organisation's App on GitHub and
  the console would go on reporting it installed. Each of them now shows
  its permissions — before it is created, as what it will ask for — and
  after it is installed, what GitHub says it holds beside what was
  declared, with *Re-check* on the page to ask again.
  - **The runner App that cannot register a runner** is the case this was
    built for: an owner narrows `organization_self_hosted_runners` to
    read, every scale set stops registering, and nothing but the runners'
    own logs said why. It is now an App that *needs you*, with the single
    sentence that fixes it.
  - **A preset whose key cannot be read says so, and claims nothing.** The
    link App is authorized by a person rather than acted as, so this
    service holds no App key for it and cannot ask GitHub what it holds:
    its page says that, and no App ever reports "matches its declaration"
    on a check that did not happen.
- **Create, Install, Re-check and Disconnect are one call each.** They
  take the App's id and work for all four kinds. The calls they replace
  still answer, so a console and a service mid-rollout are never a broken
  page, and a browser part-way through GitHub's two clicks when the
  service restarts finishes exactly where it was going to: the generic
  call starts the same flow, under the same signed state, ending at the
  same callback.
- **A group's page says which GitHub tokens it may mint, from the
  service.** It used to fetch the whole GitHub report — which asks every
  connected organisation for its status — and work the reverse edge out in
  the browser, to render one section. The internal group now carries its
  `github_grants` from the catalogue: the Apps it may mint installation
  tokens of, the repositories each covers and the most a token may carry.
  The section no longer shows each App's state on GitHub, because reading
  the policy should not cost a call to GitHub; the App's own page is one
  click away and says it.
- **The Apps list says where each App's key is kept.** The Kubernetes
  Secret and the keys within it were a shape the console guessed from the
  release name; they are now the names the service actually writes, and a
  deployment that keeps no state in Kubernetes names no Secret instead of
  naming one that does not exist.

## v1.13.0

- **The People list shows the GitHub account each person linked.** The
  *GitHub: linked / not linked* facet told you a person had linked
  something without ever saying what; there is now a **GitHub** column
  beside the address, holding the login, linking to that account's page on
  GitHub. It is the same account the person's own page calls theirs — one
  rule, read in one place — so the two can no longer disagree. Somebody
  who has linked nothing gets an em dash, not a chip: a chip is a state,
  and a state on every second row of a directory-length table is a wall of
  grey that says nothing.
- **The GitHub facet is now the service's answer, not the browser's.**
  It used to be applied to the page in hand: with a snapshot of hundreds
  and a page of two hundred, "GitHub: linked" could report a handful while
  the rest of the company sat past the cut. `SearchPeople` now takes the
  filter and returns the login on each row, narrowing before the limit
  like the provider, domain and account filters always have — so the
  caption reads "the first 200 of 1,204 that match" and means it. The page
  also makes one call again instead of two: it no longer fetches the whole
  GitHub report, which asks every connected organisation for its status,
  to colour one column.
- **An unreadable link store says so instead of reporting a clean
  company.** Where links cannot be read — a deployment where nobody can
  link an account, or a failed read — the column is blank for everyone and
  the page says why underneath. Narrowing by *linked* or *not linked* then
  fails outright rather than answering "nobody".

## v1.12.0

- **One list and one page for every GitHub App.** The console's *Apps* tab
  was five unrelated blocks — a card for the link App, a table of
  organisations that linked the organisation rather than its App, a table
  of runner Apps, a box counting a catalogue behind a third level, and a
  directory-length table of linked accounts. It is now one list: every App
  this service keeps a key for or is declared to, grouped by organisation,
  those needing an operator first, with columns for what each App is for
  in plain words, where it stands, the repositories it reaches and the
  internal groups that may mint its tokens.
  - **Every App has a page**, at `#/github/apps/<id>`, not only the
    catalogue's. It opens with a sentence built from its facts — "Mints
    tokens for 2 internal groups on every repository in example-org;
    installed, matches its declaration" — then its facts, then its edges:
    who may mint its tokens, the organisation it acts on, the accounts
    linked through it, the last tokens asked for. Permissions, events, the
    Kubernetes Secret its key is in and its ids on GitHub are reference
    material behind a disclosure, and the permissions table is one column
    unless GitHub holds something other than the declaration.
  - **One vocabulary of state.** An App is *done*, *needs you*, *waiting on
    person* or *waiting on controller*, with the exact state in the
    tooltip — the same four words the membership rows use. An App that
    needs an operator says the single thing to do, and those Apps are now
    counted on the GitHub overview's *Next* line and on each
    organisation's card, which they never were.
  - **Create, Install, Re-check and Disconnect** sit on the App they
    change, and *Disconnect* asks first, stating what it does: what stops
    working, that the key is forgotten here, and that the App itself stays
    on GitHub with a link to its settings.
  - **Old addresses still work.** `#/github/apps/catalogue` opens the Apps
    list and `#/github/apps/catalogue/<id>` opens that App's page.
- **Organisation, team and person pages say each thing once.** An
  organisation lists the Apps installed or declared for it; what the
  controller leaves alone — owners, ignored addresses, outside
  collaborators and members nobody linked — moved into a *Left alone*
  disclosure, so "Nothing to change." is only said when there is nothing
  to change; people waiting to link or accept are counted rather than
  repeated once per team; a *Fed by* column that only repeats each team's
  own group is dropped; and the rule that owners are managed outside the
  policy is stated once above a table instead of on every owner's row.
- **An internal group says what it may mint.** A group granted tokens of a
  GitHub App now shows those Apps, their repositories and the most a token
  may carry, instead of reading "it only adds claims to a token".
- **People can be narrowed to who linked a GitHub account**, with a
  *GitHub: linked / not linked* facet, which the link App's page links to.
  The narrowing is applied to the page in hand for now.
- **The last tokens of an App degrade gracefully.** Where the audit query
  behind *Recent tokens* is slow or fails, the page says so in words and
  points at the Audit page, rather than showing a bare HTTP code after
  fifteen seconds.
- **A demonstration run shows the whole Apps page.** `DEMO=1` now declares
  runner tiers and a catalogue, with one App of each state: installed as
  declared, edited on GitHub since, and not created yet.

## v1.11.1

- **The audit log line is safe on every branch, not only the typed ones.**
  A field of a type the line does not expect is now rendered and escaped
  like any other caller-supplied value instead of being logged as is. No
  field takes that path today; the line no longer depends on that staying
  true.

## v1.11.0

- **A catalogue of GitHub Apps, created and installed from the console.**
  A deployment needs more GitHub Apps than the three this service creates
  for itself; each is now a declaration and two clicks.
  - **Declaring.** `githubApps.catalogue` lists Apps as data: `id`, `org`,
    optional `name`, `description`, `public`, `permissions`, `events`,
    `installation` (`all` or `selected`) and `grants`. It is rendered to
    `ConfigMap <release>-github-apps-catalogue` and read once at start
    from `GITHUB_APPS_CATALOGUE_FILE`. The service refuses to start on a
    malformed entry: an unknown key, a duplicate id, a level that is not
    `read`, `write` or `admin`, a name over GitHub's 34 characters, a
    grant above the App's permissions or naming a group the policy does
    not declare.
  - **Creating one.** On the GitHub page, *Apps*, then *Catalogue*: an
    operator presses *Create* on an App's page, an owner of its
    organisation confirms on GitHub, then installs. The App is created
    under the organisation the entry names and no other.
  - **Where it is kept.** `Secret <release>-github-catalogue-apps`, created
    empty at start: `<id>.github_app_id`, `<id>.github_app_installation_id`
    and `<id>.github_app_private_key` beside `<id>.record.json` once
    installed, `<id>.pending_private_key` until then. A deployment backs it
    up like the other Secrets, for example with a `PushSecret`.
  - **Drift.** Each App shows whether GitHub still holds what was
    declared: the App's permissions and events, and the permissions its
    installation accepted — a pending permission request reads as drift.
    GitHub has no API to change an App's permissions, so the page links
    to the App's settings; *Re-check* asks again. Answers are cached for a
    minute, and a failure to ask is the App's reason, never the page's
    error.
  - **Disconnect** uninstalls the App and forgets its keys. It does not
    delete the App on GitHub; its owner does, from the linked settings.
  - **Grants** — which groups may ask for an App's installation tokens,
    for which repositories, with at most which permissions — are declared,
    validated and shown on the App's page.
- **Installation tokens for catalogue Apps, minted under the grants.** A
  job, a workload or a person exchanges the proof it already holds at
  `/token` for a GitHub App installation token: RFC 8693 with
  `requested_token_type=urn:access-roster:params:oauth:token-type:github-installation-token`,
  `audience=github-app:<id>`, and optional `repositories` (names) and
  `scope` (`name:level` permissions). The response carries GitHub's token
  with `token_type: N_A`, its `expires_in`, and the repositories and
  permissions GitHub granted, under `Cache-Control: no-store`.
  - **One grant covers the whole request.** Of the App's grants for the
    proof's groups, in catalogue order, the first covering every named
    repository and permission is used; no repositories needs a `["*"]`
    grant; no permissions asks for exactly the grant's. GitHub is always
    sent that narrowing, so nothing the installation holds beyond the
    grant reaches a token.
  - **Errors** are RFC 6749's: `invalid_target` for an App that cannot
    mint or a proof no grant names, `invalid_scope` for a request wider
    than any one grant (or refused by GitHub), `invalid_grant` for a
    subject that is not a proof, `invalid_request` for a malformed request
    or an `actor_token`.
  - **Proofs are the same.** The verifier chain, the sign-in exchange's
    client rule and the group evaluation are the ones every exchange uses;
    ordinary exchanges are unchanged.
  - **Audited, never stored.** Every request is one `github.token.minted`
    event — proof kind, App, organisation, grant, repositories,
    permissions, installation, expiry — minted or refused. The token is
    never recorded and never kept.
  - **`accessctl github-token --app <id> [--repository name]...
    [--permission name=level]... [--json]`**, from the job's identity in
    CI and the sign-in on a laptop, and `Exchanger.GitHubInstallationToken`
    in the `tokens` package.
  - **The action** takes `github-app`, `repositories` and `permissions`,
    and sets the masked `github-token` output. `audiences` is now optional
    when `github-app` is given.
- **A GitHub matcher can pin the workflow file.** `workflow_ref`,
  `job_workflow_ref`, `sha`, `event_name` and `ref_type` are read from the
  job's identity token and matched as globs, so a group — and the grant
  that names it — can admit one reviewed workflow on one branch rather
  than every job in a repository. Matchers without them are unchanged;
  deploy the issuer before writing them in a policy, as an older one
  refuses the keys.
- The roster's own three Apps are built from the same catalogue shape by
  one manifest builder; the manifests GitHub is posted are unchanged.
- **The audit log line cannot be forged by what a caller sent.** Every
  string on the `audit` log line — a user agent, a subject, a request
  attribute — now drops record separators and control characters and is
  cut to 256 bytes, as the service's other log lines already were. The
  record kept in the trail is unchanged and keeps each value exactly.
- **accessctl rides out a dropped connection in a job.** Every tool call
  that runs `accessctl kube-token` (or `aws`, `token`, `github-token`)
  asks the job's token service and then the issuer again, so a long job
  makes many round trips, and one network blip among them used to fail
  the step. Both requests are now tried up to four times, waiting about
  a quarter of a second, then twice as long each time up to two seconds,
  jittered, when the failure may pass: no answer at all (a connection
  reset or refused, a timeout, a temporary DNS failure), `429` or a
  `5xx`. A refusal, any other `4xx`, a certificate that is not trusted, a
  name that does not exist, or an answer with no token is final at once,
  as before. The exchange is retried the same way on a laptop. A failure
  that outlasts the attempts exits as it did; the job's token, and an
  exchange that never got an answer, say so — `(after 4 attempts)`.

## v1.10.0

- **access-issuer's routes can attach to a ListenerSet.** `route.parentRefs`,
  when set, is used as written for both of the issuer's routes, and the
  chart then renders no Gateway or TLS Certificate of its own: the parent
  owns the listener for `route.host` and its certificate. Without it the
  chart renders exactly what it did.

## v1.9.0

- **access-proxy routes can attach to a ListenerSet.** `exposure.parentRefs`
  replaces `exposure.gateway` when set and is used as written for every
  route the chart renders, so a console's hostname can move to a
  ListenerSet, or sit on a Gateway and a ListenerSet together while it
  moves. Without it the chart renders exactly what it did.

## v1.8.1

- **A rollout no longer leaves the GitHub controller failed for an
  interval.** A policy change restarts the console's replicas and the
  controller at different moments, and for a few seconds the Service can
  still route the controller's questions to a replica on the previous
  policy. The controller rightly changed nothing on those answers — and
  then waited its whole interval (15 minutes by default) before asking
  again, so the GitHub page showed the pass failed and newly bound teams
  stayed empty until then. A pass that meets another policy, in a holders
  list or in a removal's confirmation, is now tried again after 5
  seconds, then twice as long each time up to a minute, six times at
  most; after that the interval resumes. Nothing is changed on an answer
  under another policy, as before.

## v1.8.0

- **Workspace credentials written by the release-rename script migrate
  too.** 1.7.0 found an old per-workspace credential Secret by its
  workspace annotation. The objects the release-rename script wrote carry
  none, so 1.7.0 refused them and kept reading them where they were.
  Start-up now starts from the workspace records and finds each old
  object by name, the way a read already did. An old object with no
  record is left alone.
- **Runner Apps are created from the console.** A runner App is the
  GitHub App a self-hosted runner scale set registers with, one per
  organisation per tier.
  - **Declaring tiers.** Set `githubRunnerApps.tiers`, for example
    `[preview, stable]`.
  - **Creating one.** An operator creates each App on the GitHub page
    (Apps), with the same two clicks as an organisation's App: create,
    then install.
  - **Scope.** The App asks for `organization_self_hosted_runners: write`
    and nothing else. It is private and has no webhook.
  - **Where it is kept.** Every App is in
    `Secret <release>-github-runner-apps`, under the keys gha-runner-scale-set's
    `githubConfigSecret` reads: `<tier>.<org>.github_app_id`,
    `.github_app_installation_id` and `.github_app_private_key`. A
    deployment copies them to its runners, for example with a
    `PushSecret`.
  - **Keys appear only once installed.** Until the App is installed, its
    key is kept under another name, so a copy never replaces working
    runners with an App they cannot register with.
  - **Disconnect.** It uninstalls the App and forgets its keys.

## v1.7.0

- **Three Secrets restore everything a console added.** Workspace
  credentials are one Secret, `<release>-workspace-credentials`, with a
  key per workspace. Before, each workspace had its own Secret, named with
  a hash of its tenant id, which nothing outside the service could
  select. Each credential now carries a copy of its record, as does every
  GitHub connection's and the link App's in `<release>-github-apps`.
  - **Start-up restores what is missing.** It rebuilds a workspace
    ConfigMap or a GitHub record that is gone, from the copy in its
    credential. So copying `<release>-workspace-credentials`,
    `<release>-github-apps` and `<release>-github-links` is a whole
    backup, for example with one External Secrets `PushSecret` each
    ([configuration](docs/how-to/back-up-and-restore.md)).
  - **Existing credentials migrate at start.** The first start copies each
    per-workspace Secret in, and gives older GitHub credentials their
    records.
  - **Rollback stays safe.** The per-workspace Secrets are left for a
    rollback. Reconnecting or disconnecting a workspace removes its old
    one.
  - **Probes don't touch the copy.** It leaves out the last probe, so a
    probe does not rewrite the Secret.

## v1.6.6

- **A failed GitHub pass keeps what was last known.** It reported the
  failure over an empty organisation, so the GitHub page blanked for as
  long as passes failed, and a controller started right after a failure
  found no held or reported rows to remember and recorded them all again —
  which is what an upgrade does, when a pass meets a console still on the
  old release. The failure is now reported over the last report with rows.
- **accessctl installs through devbox.** A release now also carries
  `accessctl_<version>_nix-flake.tar.gz`, a Nix flake over that release's
  own archives. A repository adds its URL with `#accessctl` to
  `devbox.json`
  ([docs/design/sluisctl.md](docs/explanation/sluisctl.md#installing-it)).

## v1.6.5

- **Audit events keep their request on a deployment, not only in tests.**
  The request's address, user agent and id were put into the context by a
  wrapper around a handler the service's listener never served, so every
  event recorded in production had none of them. The issuer's listener
  now serves the wrapped handler itself (`issuerapp.Deps.Around`), and the
  service's `Handler` is that same handler.

## v1.6.4

- **The client address is read from the right of `X-Forwarded-For`.**
  `audit.trustForwardedFor` took the first entry, which a caller can
  write, since an edge and a gateway append rather than replace. It is
  replaced by `audit.forwardedForTrustedHops`: the number of the
  deployment's own proxies that append, the client being the entry just
  left of them. `0`, the default, still records the connection's peer.

## v1.6.3

- **A restarted GitHub controller does not record every held and reported
  row again.** Which rows it had recorded lived in the process, so each
  restart wrote a fresh `github.owner.reported` for every owner in every
  team into a trail that is now durable. The first pass after a start
  takes them from the report the previous process wrote, so a restart is
  not news.
- **The Audit page says where the trail is kept:** in S3, not a capped
  stream whose durable copy was the log.
- **Audit records speak the Elastic Common Schema.** Each event kept in
  S3 is one ECS document (`event.action`, `event.category`, `event.type`,
  `event.outcome`, `user.name`, `user.target.name`, `observer.name`,
  `service.target.name`, …), and each log line carries the same fields
  under the same dotted names, with an `event.id` shared by the line and
  the record. What ECS cannot say stays under `access_roster.*`: the
  native outcome, the target and the attributes. The log line keeps
  `audit=true`; `kind`, `source`, `actor` and the `attr.<name>` keys are
  gone, so a log query naming them needs the new names. **Upgrade note:**
  objects written by 1.6.2 are read as before, beside the new records.
- **An audit event says where it came from.** `client_address`,
  `user_agent` and `request_id` (the gateway's `X-Request-Id`) are kept
  for every event a request caused, from sign-ins and console calls to
  token exchanges, shown on the Audit page, and accepted from a reporter
  within bounds. The address is the connection's peer unless
  `audit.trustForwardedFor` is set, which only a deployment behind a
  gateway that replaces `X-Forwarded-For` should do.
- **Audit writing is a ConnectRPC contract.** `AuditSinkService`
  (`WriteAuditEvents`, `ListStoredAuditEvents`) is what the service
  records through and what a writer implements: the S3 writer and the
  in-memory one, joined in process with no network. A writer in another
  process can implement it later without anything that records changing;
  none is configured in this release.
- **A recovery sign-in is refused when its audit record cannot be
  written.** It is the one event that fails closed: at the issuer and at
  the console's door alike, the record is put in S3 before the sign-in
  succeeds, and a failed put refuses it with a message naming the audit
  trail, and records the refusal. Every other event still never waits on
  S3. The runbook's *When the audit trail cannot be written* says what to
  check.
- **The audit writer publishes metrics:** `access_roster.audit.writes`
  by outcome and durability, `access_roster.audit.dropped`, and
  `access_roster.audit.queue`, over OTLP when `telemetry.otlpEndpoint` is
  set. The runbook carries the alert rules for when a collector exists.

## v1.6.2

- **The audit trail is kept in S3, never in Valkey.** `audit.s3.bucket`
  names the bucket; the service appends events as JSON-lines objects by
  the hour (`<prefix>YYYY/MM/DD/HH/…jsonl`), at most
  `audit.s3.flushInterval` (10s) after each event, and the console's
  Audit page and `ListAuditEvents` read them back, newest first. Recording
  still never waits on the store: events queue in the replica, are listed
  from there until written, and are written when S3 answers again. The
  Valkey stream is gone, and with it `audit.maxAge`; a deployment that
  sets no bucket keeps the trail in one replica's memory and says so at
  start. **Upgrade note:** the stream held in Valkey is not carried over;
  the pod's AWS identity needs `s3:PutObject`, `s3:GetObject` under the
  prefix, `s3:ListBucket` on the bucket, and the bucket key's
  `kms:GenerateDataKey`/`kms:Decrypt`. Put Object Lock on the bucket.

## v1.6.1

- **A CI matcher can require a repository's visibility.** `github:
  { owner: example-org, visibility: private }` admits every private repository
  of an organisation and nothing else: not its public ones, and not a
  fork, which is another repository. The issuer reads GitHub's
  `repository_visibility` claim into the proof, `Explain` takes it in
  `github.visibility`, and a value other than `public`, `private` or
  `internal` is refused when the policy loads. Deploy the issuer before
  writing the key in a policy: an older one refuses it.
- **Security: a policy rollout no longer removes people from GitHub teams.**
  The GitHub controller and the console load the policy when they start,
  and a rollout restarts them at different moments. For that window the
  controller could decide with the new policy and ask a console still on
  the old one, which knows nothing of a team the new policy binds: nobody
  holds its group there, and `Explain` lists no such group, so the
  controller took every member of the newly bound team as confirmed to
  leave it: binding a new team removed its members until the next pass,
  under one policy, added them back. `Explain` and `ListHolders`
  now carry `policy_digest`, the digest of the policy each answer was
  computed under, and the controller changes nothing on a holders list
  under another policy (the pass fails and is retried) and confirms no
  removal on an `Explain` under another one (the row waits). A console too
  old to send a digest counts as another policy, so deploy the controller
  and the console together, as the chart does. Organisation removals were
  never exposed: they rest on the directory's found and suspended, which
  no policy changes.

## v1.6.0

- **The sign-in page names the application.** It said "the application
  that sent you here", which is true of every sign-in page, a phishing
  page's included. Now it says *Sign in to continue to **Argo CD***, with
  a one-line description under it and the host the sign-in returns to,
  as text. A client declares the two new optional keys `display_name` and
  `description`; one with no name is shown by its id, and a `k8s:<cluster>`
  client as *Kubernetes — `<cluster>`*. When the redirect is on the
  person's own computer (kubelogin, accessctl) the page says a program on
  this computer is asking, and shows no port. The refusal for a client a
  person holds no group of names it too. Both keys are shown to anyone
  who starts a sign-in, so keep them free of anything a stranger should
  not read. Deploy the issuer before adding them to a policy: an older
  issuer refuses the unknown keys.
- **`accessctl token --audience <client>`** prints a token for one audience
  and nothing else, for a caller that is neither kubectl nor an AWS SDK —
  OpenBAO's JWT login reads it from stdin. Like `kube-token` and `aws` it
  answers from the sign-in on a laptop and from the job's own token in CI.

## v1.5.6

- **An owner the controller reports reaches the audit log.** The
  `github.owner.reported` event carried the outcome `reported`, which the
  audit stream does not have, so it refused the whole batch and the
  owners' events never landed. It is recorded with the outcome `ok` now;
  the kind says what happened. This only shows in an organisation the
  controller acts in.

## v1.5.5

- **Security: token exchange no longer accepts an ID token as a proof.**
  Before 1.5.5 the exchange accepted any token this issuer had signed whose
  claims named a person. An ID token names one, and is handed to every
  relying party a person signs in to, so a holder of it could exchange it
  -- presenting any public client -- for a token for any audience that
  person's groups admit, a cloud role or a cluster included. Signature,
  issuer and expiry were always checked; what was missing was any rule
  about which of this issuer's own tokens may stand for a person. Now a
  proof comes only from a verifier (a GitHub job, a cluster workload) or
  from a CLI sign-in as described below, and every other token this
  issuer signs is refused. Upgrade every access-issuer to 1.5.5; there is
  no configuration-only mitigation.
- **`accessctl` works on a laptop.** `accessctl login` followed by
  `accessctl kube-token` or `accessctl aws` exchanges that sign-in for the
  requested audience, so one kubeconfig and one aws.ini serve a person and
  a CI job alike. Of the tokens this issuer signs, the exchange takes only
  this one as a proof: the access token of a live session at a client
  declaring the new `sign_in_exchange: true` (a `public` client: the CLI),
  presented by that client. Deploy the issuer before adding the key to a
  policy: an older issuer refuses the unknown key.
- **Revoking a session reaches a renewed access token.** A token minted
  by a refresh now names its session, as the first one does, so
  `userinfo` stops answering for it once the session is revoked.

## v1.5.4

- **An organisation can ignore addresses and GitHub logins.** The policy's
  `github.<org>.ignore` lists what the controller leaves alone whatever the
  bindings say: an address in a bound directory group that nobody here can
  take out of it, a temporary owner. An ignored address is never invited
  and never reported as waiting; an ignored login is never added, removed
  or changed. The organisation's page lists them.

## v1.5.3

- **An owner is added to teams, and never removed from one or demoted.**
  Owners — an organisation's break-glass seat among them — are managed
  outside. The controller still adds an owner to the teams the policy wants
  them in and promotes them where it wants a maintainer; it no longer takes
  an owner out of a team or turns a maintainer into a member. Both are
  reported instead. On the first real dry run every role change it proposed
  was an owner being demoted.

## v1.5.2

- **Disconnecting the link App leaves profile matches and imports alone.**
  It made every linked account unverifiable, including the ones matched
  from a public profile or imported, which hold no token of the App's. So
  moving the link App to another organisation would have unlinked every
  imported account; now it unlinks only the people who authorized it.

## v1.5.1

- **The GitHub page is tabs.** *Overview* says what needs attention next,
  with a card per organisation and the people who have not linked, and a
  button to copy their addresses. *Organisations* opens each
  organisation — what enabling it would do in one sentence, removals
  first, its teams each with a page — and *Apps* holds the link App and
  every organisation's App. Rows read **OK**, **waiting for them** or
  **needs you**, with the controller's exact state in the tooltip.
- **A person's page shows them on GitHub**: the linked account, their
  teams and what comes next, and on your own page a button to link your
  account. A group's page lists the GitHub teams it feeds.

- **The TypeScript package is published to GitHub Packages** at each
  release tag's version, built and tested by the release workflow.
  Install `@truvity/access-roster` with the `@truvity` scope pointed at
  `https://npm.pkg.github.com` and a token that can read packages
  ([docs/reference/typescript.md](docs/reference/typescript.md)). A git
  install stopped working when `ts/dist` left git: yarn 4 packs a git
  dependency without running `prepare`, so it shipped no `ts/dist`.

## v1.5.0

The GitHub controller runs joiners, movers and leavers with no human in the
loop, and stops itself where a person is needed. Every organisation stays
a dry run until it is listed in `githubRoster.actsIn`, as before.

- **Three ways a GitHub account becomes somebody's.** Linked by the person,
  as before; **matched from a public profile** that shows a work address
  the directory has, live — GitHub lets an account publish only a verified
  address; and **imported** from approved pairings elsewhere through the
  new operator RPC `ImportGitHubLinks`, after three checks. The latter two
  are never re-checked on GitHub and never displace a link the person made.
- **Seats.** Nobody is invited past the last free seat, and nobody at all
  while the seats cannot be read; the GitHub page says how many seats to
  buy. **An organisation's App now asks for organisation administration
  (read).** Apps already created need an owner to add it on GitHub.
- **Mass removals wait for a person.** A pass whose removals concern more
  than half an organisation removes nobody until an operator presses
  Confirm, for exactly that set (`ConfirmGitHubRemovals`).
- **Two expired invitations stop the third**, until the person links again.
- **Owners are reported, never held**: owners and billing are managed
  outside. Transient trouble — the directory unable to vouch right now, a
  change GitHub refused — is **retried** every pass instead of held.
  Outside collaborators are listed.
- **OpenTelemetry metrics**, pushed over OTLP when
  `telemetry.otlpEndpoint` (or `OTEL_EXPORTER_OTLP_ENDPOINT`) is set:
  passes, changes, rows by state, seats, breaker trips, links by state and
  source. Nothing is exported, and no listener opened, without it.

## v1.4.0

- **`accessctl` works inside a GitHub Actions job.** With
  `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` set,
  `accessctl kube-token` and `accessctl aws` exchange the job's own
  identity token, minted for the issuer, and present the audience as the
  client, the way the root action does. Elsewhere nothing changes. One
  committed kubeconfig and one `aws.ini` now serve a laptop and a job
  alike, where a repository used to keep a second copy of each for CI.

## v1.3.2

- **The GitHub page reads in the order the work happens.** *Set up* first,
  as three ticked steps: every GitHub App in one table — the link App for
  all organisations and each organisation's team App, with Create and
  Disconnect beside each — then the link page to send people, with a copy
  button, then letting the controller act. *People* next, once per person
  across every organisation and team, filtered to what needs a change or
  is waiting on them. Each organisation's section keeps its teams and the
  changes it would make; nobody waiting to link is repeated there once per
  team.
- The demonstration shows linking: a link App, linked accounts, a lost
  link and people not linked.

## v1.3.1

- **An organisation waiting on people to link says so.** With nobody
  linked there is nothing to change and nothing held, and the controller
  reported `in-sync` — which is exactly what an operator reads as "safe
  to enable". It now reports `waiting`, with a count of the rows waiting
  on somebody to link (`tick.waiting`), and the GitHub page shows it.

## v1.3.0

People link their own GitHub account. GitHub discloses members' work
addresses only to organisations on its Enterprise Cloud plan, so on every
other plan the controller could match nobody: every member read as
unlinked and every invitation was held. Nothing was changed on GitHub by
that, and nothing is now until a person links.

- **Self-service linking.** An operator creates the **link App** once,
  from the GitHub page: a public App that asks only to read a person's own
  email addresses and is installed nowhere. A person opens
  `/connect/github/link`, authorizes it, and their account is linked to
  each verified work address the directory has, live. No console role is
  needed, and nobody types a username. Linking a second account with the
  same address moves the address to it.
- **A link is checked every pass.** The controller keeps the person's
  token pair and reads the account's verified addresses again each pass.
  An account whose linked addresses are all gone or unverified, or whose
  authorization was revoked, **leaves the organisation at once** — the one
  removal that does not ask the directory. An outage changes nothing. A
  token pair lost in an interrupted renewal makes the link
  *unverifiable*, never lost: renewals are written as in progress first.
- **Invitations go to the linked account**, by its id, straight into its
  teams. Somebody wanted who has not linked is `not-linked` — waiting on
  them, not held. Invitations by address are no longer sent.
- **Chart:** the controller's Role may update one Secret by name,
  `<release>-github-links`, which the service creates. The link App's
  credential sits beside the organisations' in `<release>-github-apps`.
- **Console:** the GitHub page gains *Linking accounts* — the link App,
  the page to send people to, and every link with its state.

Also in this release:

- **An access token names the person.** `name`, `given_name` and
  `family_name` now travel in the access token as they already did in the
  ID token, from the same one directory call. The access token is what a
  gateway or a proxy forwards to an application, so one showing who is
  signed in no longer has to call userinfo on every page. A workload's
  token carries no names rather than empty ones.

- **The Go `identity` package reads them**: `Verified` gains `Name`,
  `GivenName` and `FamilyName`, and `WhoAmI` answers `name`, `givenName`
  and `familyName` — the keys the TypeScript `Identity` already declared,
  and which nothing had been filling.

- **A server half for Node: `@truvity/access-roster/server`.** The Go
  package's issuer anchor in TypeScript — `Issuer`, `middleware`,
  `requireGroups`, `whoami` — with the same checks, the same caller and the
  same `whoami` body, connect-style for Express and Nest. It exists for the
  business applications leaving gateway-auth, whose backends are Node and
  which had nothing to verify a token with but a header or a generic JWT
  library. `jose` becomes the package's one runtime dependency; a browser
  bundle importing only the root or `/react` never loads it.

  access-roster itself does not run this half, being a Go service, which
  is the one place the rule that it consumes what it publishes cannot hold.
  Its tests verify tokens over a real listener against a real key set, and
  the Go test beside them proves the issuer's own tokens carry what both
  halves read.

## v1.2.0

GitHub organisations, managed from the policy, and an audit stream for the
whole service. **Nothing changes for a deployment that sets nothing new:**
the GitHub controller is off unless `githubRoster.enabled`, and changes no
organisation until it is listed in `githubRoster.actsIn`.

The one incompatible change is to the policy's `github` table, which
nothing rendered and nothing but the console ever read: it now names
internal groups. A policy carrying the old shape is refused at start
rather than reinterpreted.


- **A GitHub team binding now names internal groups, not provider
  addresses**, and declares both of GitHub's team roles:

  ```yaml
  github:
    globex:
      members: [all:globex:employee]      # in the org, with or without a team
      teams:
        team-platform:
          members: [all:platform:engineer]
          maintainers: [all:platform:lead]
  ```

  A team is a consumer of a group exactly as a client's `requires` is,
  so everything the policy already does applies to a team for free:
  holders from several workspaces, a matcher for the day before a
  provider group exists, the naming convention, the console's holders
  view. Which accounts hold a group stays a question the directory
  answers once, in `groups`, rather than one this table asks again.

  An organisation's own `members` is new and is for the people who
  belong in it without a team — without it they are people no binding
  accounts for, which is exactly who a controller would remove.

  Refused, each because it is otherwise silent: a group nothing
  declares, a team fed by neither role, an organisation binding nothing
  at all, and either half declared twice across two merged files.

  **This is a breaking change to the table**, which was read by nothing
  but the console: no controller exists yet, and none of it has ever
  reached a token. A deployment carrying the old shape fails the render
  rather than being reinterpreted.

- The Rules page reads the new shape: a binding's rule is the internal
  group and links to its page, *depends on* is whatever that group
  depends on, and an organisation's own members are their own kind.
  `GetPolicy` carries `maintainers` on each team and a new `orgs` list.

- **A workload in a federated cluster can call the console's API with
  its own ServiceAccount token.** It presents the projected token as the
  bearer, with the audience token exchange uses, and it is verified
  against the same cluster key sets — no exchange in front of a
  same-cluster call, and no TokenReview, so the service still holds no
  access to its own cluster. The identity has the new source `workload`
  and is whatever the policy's `service_account` matchers make it:
  never recovery's operator, the only other identity that arrives as a
  ServiceAccount. It is how the GitHub controller will read
  `ListHolders`.

- **`ListHolders` no longer answers as though complete when it was not.**
  `truncated` reported only the response limit, so past the examined cap
  of 10,000 accounts an answer silently left holders out and looked
  whole. It now reports either bound. Its documentation also says what
  was always true and is the thing a consumer removing access needs to
  know: absence from `holders` is never evidence on its own, because a
  workspace whose snapshot cannot be read contributes no accounts — a
  removal is confirmed per account with `Explain`.

- **A GitHub page in the console**, on the internal side beside
  clients. Per organisation it shows the policy's bindings beside what
  the GitHub controller last reported — each team's groups in both roles,
  every person's state and what happens next, held actions with their
  reason, and members no binding explains. Read-only. The report is a
  ConfigMap, `<release>-github-status`, which the service now creates at
  start so that the controller's Role can name the one object it
  updates; `GitHubService.GetGitHubStatus` serves the join. The
  demonstration run carries bindings and a sample report, so the page
  can be walked through without a controller.

- **Connect a GitHub organisation**, from the console's GitHub page. An
  operator presses Connect; the organisation's owner creates the App
  GitHub offers — private, `members: write` and nothing else, no webhook
  — and installs it. Nothing is typed or pasted, and the key exists only
  in this service's namespace. Coming back from Install the service asks
  GitHub where the App is installed rather than trusting the redirect.
  Only a bound organisation can be connected; an App created and never
  installed is finished rather than created again; an installed one is
  refused rather than duplicated. **Disconnect** uninstalls and forgets.
  Records live in `<release>-github-orgs`, keys in `<release>-github-apps`,
  both created empty at start. The service now calls `api.github.com`,
  which a default-deny egress policy has to allow.

- **An audit stream for the whole service**, and an operator-only
  **Audit** page. The issuer records sign-ins and their refusals, recovery
  sign-ins as their own kind, refused refreshes, token exchanges, revokes
  and sign-outs; the console records its own sign-ins, provider connects,
  disconnects and domain and group changes, and GitHub Apps created,
  organisations connected and disconnected. One Valkey stream shared by
  every replica and both halves, capped by `audit.maxEvents` and
  `audit.maxAge`; in memory without Valkey. **Every event is also a log
  line** with `"audit":true`, which is the durable copy.

  A component in another process reports through
  `AuditService.RecordAuditEvents` with its own ServiceAccount token, and
  only a workload in the new group `all:access-roster:reporter` may. The
  service stamps who reported and when, and refuses a report naming one of
  its own sources — so a reporter cannot forge a sign-in. A component never
  writes to Valkey itself: that store holds every session and refresh
  token. Recording never fails what it records.

- **The GitHub controller**: `github-roster`, a second binary
  and image from this repository and a second process from this chart,
  behind `githubRoster.enabled`. It makes each organisation's teams match
  the policy's `github` table and reports on the GitHub page.

  Every pass, for every bound organisation: it asks the console who holds
  each bound group, with its own ServiceAccount token; reads GitHub
  through the organisation's App — members with their verified-domain
  addresses, invitations, teams and roles, all of it or the pass fails;
  links a login to a person by verified address alone; invites joiners
  straight into their teams, adds and re-roles members, removes a member
  no bound group wants from a team, and removes from the organisation only
  somebody the directory no longer has. **Each removal is confirmed** by
  asking about that one address, and done only on an answer the directory
  vouches for. Owners, members with no verified address and unbound teams
  are never touched.

  **Born disabled:** organisations not in `githubRoster.actsIn` are derived
  and reported, and nothing is changed. Changes and newly held actions are
  recorded in the audit stream. The chart gives its account one
  permission — updating its report — and mounts the App keys as a volume;
  it refuses to render without the cluster row the service verifies the
  controller's token against.

## v1.1.0

- **SECURITY: a client's `requires` is now enforced when somebody signs
  in through a browser, and again when a session is refreshed.** It was
  enforced on token exchange and nowhere else, so for a browser client
  the list was documentation: anybody the issuer would authenticate was
  issued a token for any declared client, and what stopped them was
  whatever the application checked for itself.

  Most relying parties were saved by that second check — ArgoCD admits
  nobody by default, Kargo authorizes itself, the consoles check their
  own roles. One was not. A console with no authorization of its own,
  behind a proxy with posture `authenticated`, had `requires` as its
  only gate, which means it had none.

  Not a way in for a stranger: the identity still had to be one this
  issuer authenticates. What it collapsed was *which* console a
  signed-in person could open.

  The refusal is a **page**, not a redirect carrying an error. The
  relying party is not the one that needs telling, and sending the
  browser back to it produces a console rendering its own version of a
  refusal it does not understand. The page says the sign-in was fine and
  that signing in again will not help, and it does not name the group
  that would have admitted them — that is telling somebody what to ask
  for by name. The detail goes to the log.

  **Checked again at refresh**, because checking only at sign-in would
  make the gate good for as long as a refresh token lives: somebody
  taken out of a group would go on renewing for up to twelve hours
  against a client no longer theirs. Now it ends at the next refresh,
  which for a proxied console is its `ttl_cap`. The answer is
  `invalid_grant`, which is what a relying party acts on — it stops
  renewing and starts a new authorization, which meets the same gate.

  Found by migrating a console and watching an identity that held none
  of its groups be admitted by the issuer and refused by the console.

## v1.0.0

The first release the design document, the architecture as shipped and
the OpenID Foundation's suite all describe the same thing. No code
changed since 0.17.1; what changed is what can be said about it.

- **Four conformance plans, zero failed, from a suite the issuer can
  reach.** Config OP, Basic OP, RP-Initiated Logout OP and Back-Channel
  Logout OP, run against the deployed issuer from the suite running in
  the cluster. The Foundation's rule is that only FAILED and INTERRUPTED
  disqualify; every REVIEW screenshot was looked at and shows the page
  its step demanded. The logout pair — RP-Initiated plus one of the
  other three — is what the Foundation requires for a logout
  submission, and both halves are green for the first time.
  [docs/conformance.md](docs/explanation/conformance-findings.md) carries the run and what
  each column means.

- **Documentation at 1.0**: one design document for the one
  service, the architecture drawn as shipped, a connect guide per row of
  the fan-out table, references that match the schema, and nothing in
  the tree describing what is not built except under a heading that
  says so.

## v0.17.1

- **Back-Channel Logout reaches every client that signed somebody in,
  and names the session that client actually saw.** Two defects, both
  found by the Foundation's Back-Channel plan the moment it could
  receive a token, which it could not from a laptop.

  A client that asked for `openid` alone holds no refresh token, and a
  session here IS a refresh token — so the issuer recorded nothing for
  it and, at sign-out, announced nothing. The sign-in now remembers
  which clients were issued an ID token under it, and each is told when
  it ends, whether or not it holds a refresh token.

  And the logout token carried the browser sign-in's id as `sid`, while
  the ID token had carried the per-client session's. A relying
  party matches the two by that value; a token that verified and matched
  nothing was a sign-out that silently did not happen. The logout token
  now names the `sid` the ID token did, and a client whose ID token had
  none is told by `sub` alone, which the specification allows.

- **The conformance suite runs in the cluster**, rendered exactly while
  its client rows are declared, at a hostname of its own. Only the
  relying-party paths are public; the control plane is reached over the
  tailnet. It is the only way the Back-Channel module can be witnessed
  at all: the issuer has to POST to the suite, and a laptop suite is the
  pod's own loopback.

## v0.17.0

- **The Sessions page filters MATCH rather than equal.** Typing `lovelace`
  in the person box finds `ada.lovelace@globex.example`, and `karg` finds
  `kargo` — prefix, suffix and middle, one rule. A box you have to fill
  in exactly is a box you can only use once you already know the answer,
  which is not the state anybody is in when they open that page.

  Valkey cannot do this for us, and it does not need to. Its glob is
  over KEY NAMES and the identity lives inside the record, not in the
  key; there is no substring index in the core and the search module is
  not deployed. What is there is a set per exact identity and a set of
  every live session — and the unfiltered listing already reads the
  second one. So a substring is the read the page was doing anyway, with
  the comparison changed. What it gives up is the narrow index, which is
  why the exact path stays exact: an identity's own page, and any
  non-operator, still read one small set.

  **Operator-only, and refused rather than narrowed for anyone else.**
  Every other rule here decides what a caller may see from the identity
  they NAMED, and an exact identity is the caller's own or nobody's. A
  substring names an unknown set: `globex.example` is everybody, and the
  checks underneath would pass it precisely because it is not anybody's
  identity to refuse.

  **`RevokeSessions` takes no such field.** It shares the query type, so
  this is enforced rather than merely absent: a revoke scoped to
  *anything containing this* takes `kargo` and `karma` together, and
  there is no undo.

## v0.16.0

- **Back-Channel Logout (OIDC Back-Channel Logout 1.0), opt-in per
  client.** A relying party that declares `backchannel_logout_uri` is
  posted a signed logout token, server to server, the moment a sign-out
  ends a session it holds. One that declares none is never contacted, so
  serving this changes nothing for a client that has not asked.

  It closes the window this design otherwise only bounds. Revoking is
  immediate at the issuer and invisible at the relying party, which keeps
  serving on a valid access token until it next refreshes.

  Of the three optional logout mechanisms it is the only one worth
  serving. Session Management and Front-Channel both load something from
  this origin inside the application's page — a polled iframe, or one
  hidden iframe per client — which browsers block by default, so both
  fail quietly in exactly the case they exist for. And neither can reach
  a PROXY, which is what holds the session for a console running no
  OpenID flow of its own.

  Two details the specification is strict about, both pinned by tests:
  `typ` is `logout+jwt`, and there is no `nonce`. Both exist so a logout
  token cannot be mistaken for an ID token by a relying party that checks
  too little — which would turn *you are signed out* into *you are signed
  in as somebody*.

- **The access-proxy chart refreshes every minute rather than every
  five.** That number IS how long a sign-out or a revoke takes to become
  true at a proxied console: the proxy keeps serving on the token it
  holds until it next asks for a new one. The cost is five times the
  refresh traffic, which for a handful of consoles is nothing.

  oauth2-proxy cannot receive a logout token — it encrypts each session
  with a secret that lives only in the user's cookie, so nothing
  server-side can find the session one names. Until that changes
  upstream, the refresh interval is the whole of the dial.

## v0.15.3

- **The Sessions page shows one row per identity**, with how it proved
  itself, how many browsers, and one *Sign out all*. It showed one row
  per browser, which is the truth and not the answer that page is for: a
  recovery account with 104 sign-ins rendered 104 rows, 103 of them with
  an empty name column, and pushed the sessions the table introduces a
  screen and a half down. Nobody reads 104 rows; they read *104* and act
  on it. The detail belongs on that identity's own page, where there is
  one identity and the rows carry information.

- **The column is *Identity*, not *Person*.** The row that made the case
  reads `prod:k8s:access-issuer:access-issuer-recovery`, which is a
  ServiceAccount. People still means people — the directory-backed page
  is unchanged — but a session listing holds workloads and CI jobs too.

- **The conformance driver signs out between modules.** Clearing cookies
  drops the browser's copy and leaves the sign-in record alive at the
  issuer, so a thirty-five module plan abandoned thirty-five of them.
  That is where the 104 came from.

## v0.15.2

- **A declared `exchange` client could never authenticate**, so every
  token-exchange audience was unusable. The library authenticates the
  caller of an exchange by HTTP Basic; public clients were handled —
  presenting nothing is right, because in an exchange the subject token
  IS the credential — and `exchange` fell through to a secret lookup.
  The policy refuses to let that kind carry a secret at all, so the
  request failed with *the client secret does not match* while the policy
  read correctly.

  This is the deeper reason token exchange had never verified a proof.
  Not only was no cluster federated: even with one, the audience could
  not authenticate. It would have affected every exchange target the
  design describes, including the AWS roles.

  Found by performing the first real exchange this issuer has ever been
  asked for, with a workload token minted in another cluster.

- **A revoked session's access token stops answering at `userinfo`.** An
  access token is a JWT verified offline everywhere else, so nothing can
  be told to stop honouring one before it expires — but `userinfo` holds
  the record, so it is the single place a revocation can reach a token
  already in circulation. Access tokens now name their session and are
  refused when it has ended.

  Conformance found it through the narrowest door: a reused authorization
  code must revoke what it issued (RFC 6749 4.1.2). We revoked the
  session and `userinfo` went on answering with the access token from the
  first redemption. Fixing only that case would have left every other
  revocation with the same hole.

- **The conformance driver can hand the browser step to a person**
  (`--manual`). Four Basic OP modules ask what a real sign-in returns,
  and recovery has no name or email to return.

## v0.15.1

- **The sign-ins table writes the person's name once.** Grouping the
  sessions list by identity in v0.15.0 fixed the half that sits below the
  fold; the table that actually fills the Sessions page is *Sign-ins*,
  and it repeated one identity fifteen times down the screen after a day
  of testing. One row per browser is still right — a sign-in is a browser
  and ending one is the unit of sign-out — but the name is written once,
  with a count of how many browsers.

  Found by taking a screenshot of the live page rather than trusting the
  change.

## v0.15.0

- **Sessions group by identity, then by browser.** Browser alone was the
  whole grouping, and on the installation-wide listing that reads as
  noise: one identity that signed in eight times is eight groups of one,
  stacked, repeating the same name eight times. By person first, with
  *same browser* kept underneath, because that is the unit a sign-out
  ends. Each heading carries its count.

- **The split deployment is gone from the code, not just from the
  cluster.** `HUB_ADDRESS` and `HUB_TOKEN_FILE`, the branch that dialled
  a directory over the network, and the whole `hubclient` package. The merge
  folded the hub into this process and nothing has dialled it since, so
  what was left was a branch that could not run and two settings nothing
  set — configuration that reads as a supported deployment and is not one.
  The directory is a required in-process dependency now.

  Removing it found the last caller: three tests were driving a stub HTTP
  hub, which is the shape from when the hub was a service of its own.
  They use an in-process directory now, which is what they were testing
  all along.

## v0.14.9

- **Charts are published by the release workflow, not by goreleaser**, so
  the set of shipped artifacts is written down in two places — and the
  second one kept `directory-roster` after the chart was deleted. v0.14.8
  built fine and then failed on `copy chart: lstat
  charts/directory-roster: no such file or directory`.

  Swept the repository for the rest rather than finding them one release
  at a time. Two were stale instructions that would have wasted
  somebody's afternoon: the issuer chart's README told you to install
  with `--set hub.address=...`, naming a service that no longer exists,
  and the acceptance command's doc comment described the wrong binary.


- **The release builds the bundles through `just console`** rather than
  restating the npm commands. v0.14.7 failed on its first release after
  the bundles left git: the hook built the console against `ts/dist`,
  which is what the console imports as `file:../ts` and which nothing had
  built yet. The ordering was already declared in the Justfile, and
  writing it out a second time is how the two got to disagree.


**The git history was rewritten at this version, and every tag before it
was deleted.** Nothing in the code changed by the rewrite — the tree at
this commit is byte-identical to what v0.14.6 shipped — but every commit
before it has a new hash, and the old tags are gone. A clone from before
this point cannot be fast-forwarded; re-clone instead.

- **Generated bundles are no longer in git.** `frontend/dist` cost 29.2 MB
  of history, about seventy per cent of the repository, because a
  minified bundle is a new blob on every dependency bump. It also bought
  a silent failure: a committed artifact goes stale while everything
  still compiles, which is how v0.14.0 shipped a console reading a
  protobuf field the server no longer sent. Absent, the embed is a
  compile error. Loud beats stale.

  `ts/dist` goes for the same reason, with a `prepare` script so a git
  install still needs no toolchain of its own. `gen/` STAYS: it is Go
  source, small and diffable, and it is what makes the module
  `go get`-able without buf.

- **`directory-roster` leaves the release.** One service, one image, one
  chart. The hub was a second binary until the merge folded it into the
  issuer, which runs it in process — `internal/app` is still here and
  still does the directory work. What goes is the separate deployment,
  which had gone on being built and pushed for a service deployed
  nowhere.

- **react 19.3.0**, which is what Renovate's two open pull requests were
  for. Neither could land: a bot can edit a lockfile and cannot rebuild
  what it changes. There is nothing to rebuild now.

- **The chart/binary environment check now points at the issuer's chart**
  and reads both environment consumers. It immediately named eight
  settings the merged service supplies to nobody, `SIGN_OUT_URL` among
  them — which is the defect behind *"sign out does not work"*, reported
  twice. Nothing failed at the time, because an empty string is valid
  everywhere it lands. The list is in the test now, with what replaced
  each one.

## v0.14.6

Both of these were found by LOOKING at the screenshots the conformance
suite had already collected. The suite cannot see what is in them — it
asks a person — so a module sitting in REVIEW with a picture of the
wrong thing passes silently. Two did.

- **The signed-out page was telling people the opposite of what had
  happened.** It said applications they already had open *"keep their OWN
  sessions until those expire"* and that ending a sign-in here *"cannot
  reach into them"*. True when it was written, and false since v0.14.2:
  signing out revokes every session the browser opened. So the page
  claimed a person's other consoles were still open at the moment it
  closed them.

  It now says what happens, and keeps the one piece of honesty that is
  still due: a proxy already holding a valid access token finds out at
  its next refresh, so a console can serve for a few minutes more. That
  delay is bounded by the client's `ttl_cap`.

- **`/authorize` refused with the library's bare text.** Unstyled black
  on white, with nothing on it saying which service had been reached.
  It is reached exactly when there is nowhere safe to redirect somebody
  — an unregistered `redirect_uri`, an unknown client — so the person is
  left looking at it with no way onward. It renders as a page now, in
  the same voice as the rest, keeping the library's sentence because it
  already says what is wrong.

## v0.14.5

- **A client's `ttl_cap` now reaches the tokens a browser gets.** It was
  applied on token exchange and nowhere else, so declaring it on a
  console did nothing at all — the code flow used the deployment-wide
  lifetime whatever the client said.

  That number is the lever over how long a REVOKED session keeps working.
  A client only learns a session ended when it next has to refresh, so
  the access token's remaining life is exactly the window in which a
  sign-out has not taken effect yet. Reported twice from live use, as a
  console that went on serving after signing out; the sessions are now
  revoked at sign-out (v0.14.2, v0.14.4) and this is what makes the
  revocation prompt rather than eventual.

  Back-channel logout would close the window entirely by telling each
  client at the moment of sign-out. It is still not served, because
  oauth2-proxy does not consume it, so a short cap is the lever we have.

## v0.14.4

**Security.** An unauthenticated `GET /end_session` ended every session
in the installation — every person and every workload, from anyone on the
internet, at an address the discovery document publishes.

The library hands the storage whatever the end-session request named, and
a bare request names nothing: no `id_token_hint` and no `client_id` left
both arguments empty. `TerminateSession` passed that straight into
`Revoke(Query{})`, and an empty query selects everything.

The same path had a second, quieter weakness. An `id_token_hint` is a
*hint* in the specification rather than a credential, and the library
accepts an **expired** one by design. Honouring it as authority to revoke
meant anybody who found an old ID token — in a log, in browser history,
in a referrer header — could sign that person out of a console.

`TerminateSession` now ends nothing. What a logout request can actually
prove is the cookie it carries, so the browser's own sign-in is the only
authority for what gets revoked. The hint keeps its real job, which is
choosing the client's signed-out page. Ending one identity's sessions at
one client is still available through `RevokeSessions`, which authorizes
the caller first.

- **Both sign-out doors now do the same thing**, and the one that had the
  weaker half was the one a PROXY uses. `/logout` revoked every session
  the browser had opened; `/end_session` — what oauth2-proxy chains to —
  only ended the sign-in. So a console behind a proxy went on refreshing
  and serving pages after a sign-out that reported success, which is how
  it was reported from a proxied console. Both call one `SignOut` now.

## v0.14.3

- **Signing out lands on the page that says so.** `/end_session` had no
  default landing page, so a request naming nowhere to go redirected to
  the issuer root, which redirects to the console, which starts a new
  authorization — and the last thing a person saw after signing out was a
  login prompt. That reads as the sign-out having failed. It now lands on
  the signed-out page, which says what did and did not end.

- **A refused sign-out no longer signs anyone out.** The browser session
  was ended on the way IN to `/end_session`, before the library had
  looked at the request. A request it then rejected — a bad
  `id_token_hint`, an unregistered `post_logout_redirect_uri` — produced
  an error page for a sign-out that had already happened. The response is
  now held until its status is known, and the sign-in ends only if the
  request was good.

- **`/end_session` answers a browser with a page.** Every failure there
  was an OAuth JSON body, which is right for `/token` and wrong for an
  endpoint a person's browser is redirected to. Anything that does not
  ask for HTML still gets the JSON, with its `error` code intact.

- **A `post_logout_redirect_uri` with no `id_token_hint` and no
  `client_id` is refused** rather than quietly dropped. There is no
  client named, so there is nothing to have registered it, and "not
  registered" is the only answer available. Nothing was being sent
  anywhere unregistered before this — the URI was ignored and the person
  signed out regardless — but silence in answer to a request is its own
  defect.

- **The conformance driver answers the suite's manual steps.** Eight of
  the eleven RP-Initiated Logout modules end at a page the suite cannot
  see and ask a human for a screenshot of it. Unanswered, each sat in
  WAITING until the next module interrupted it — a row of greyed-out
  results that looked like a server fault and was a step nobody had
  performed. The driver uploads the screenshot from the browser it is
  already driving.

## v0.14.2

- **Signing out ends what the browser opened**, not only the sign-in
  itself. The design leaned on those sessions dying *"at their next
  refresh"* — they do, because a revoked session's refresh is refused —
  but nothing was revoking them. So every console the person had opened
  kept its own session until it happened to refresh, and a sign-out that
  reported success left access in place.

  The sessions go first and the sign-in second: if the first half fails
  the sign-in is still there and the person can try again, where the
  other order would leave sessions running with nothing listing them.

## v0.14.1

- **The console bundle shipped in v0.14.0 was stale**, so the Sessions
  page's new sign-ins table would have shown nothing: a protobuf field
  was renamed `signins` → `sign_ins`, the TypeScript was regenerated, and
  `frontend/dist` was never rebuilt — so the bundle read a field the
  server no longer sends.

  `just check` did not catch it because `ts` and `console` were not in
  it: CI runs each recipe as its own parallel job, so the local check and
  the pre-push hook skipped the two that build. They are in it now. The
  check is slower and it is the check that runs before a push.

## v0.14.0

- **Reusing an authorization code ends the session it opened.** RFC 6749
  4.1.2 says a code used twice must be denied and SHOULD revoke the
  tokens already issued from it. We denied the reuse — the first
  redemption deletes the request — but left the first redemption's tokens
  working, which conformance saw as a resource endpoint answering 200
  where it wanted a 4xx.

  Denying alone is the worse half: a code presented twice is a code
  somebody else has, and the tokens from its first use are the ones now
  in doubt. PKCE with S256 is required here and already defeats most code
  interception, so this is defence in depth rather than an open door —
  and it is the cheap half, because the code arriving twice is the whole
  signal.

  A reuse is logged at WARN, since it is either a broken client or a
  stolen code and both are worth seeing.


- **A token says how the person was proved.** `acr` was empty, so a
  client asking with `acr_values` got none back — which conformance
  flags, and which matters more than the flag: a recovery sign-in
  bypasses the directory **by design**, and a relying party that wants to
  refuse one had no way to see it in a token. Two classes, and discovery
  now advertises both, because a client cannot ask for a value it has no
  way to learn about.

  `amr` said `pwd` for everything, which is untrue of every sign-in this
  issuer serves: recovery presents a ServiceAccount token, and a
  directory sign-in presents whatever the provider asked for — which we
  are not told, so claiming a password was an invention. It is empty
  where we were not told.


- **A response carrying a credential is never cached.** RFC 6749 5.1
  requires `Cache-Control` on the token endpoint and the library does not
  set it, so conformance failed `oidcc-refresh-token` with *"token
  endpoint response does not contain 'cache-control' header"*. The rule
  exists for a reason worth stating: a token response sitting in a
  proxy's cache, or a browser's, is a credential anybody who can reach
  that cache now holds.

  `no-store` and `Pragma: no-cache` go on `/token`, `/revoke`,
  `/userinfo` and `/introspect` — and deliberately **not** on discovery
  or the key set, which are public documents that should be cached.
  Telling the world not to cache a key set would put a fetch of it in
  front of every verification anybody does.


- **`prompt=none` with nobody signed in now answers the CLIENT.** OpenID
  Connect Core 3.1.2.6 requires `login_required` at the redirect URI; the
  issuer rendered an HTML page saying so instead, and the code comment
  had predicted the failure without fixing it.

  Worse than it sounds: the caller of `prompt=none` is usually a hidden
  iframe doing a silent renewal. It cannot read an HTML page, has nobody
  to show it to, and waits until it times out — so a relying party doing
  silent refresh would hang rather than re-authenticate. Conformance
  called it *"expected an error but did not get one"*, which is the same
  fact from the other side.

  Found by the Basic OP profile, which is the first thing that run has
  paid for.


- **The conformance driver runs in headless Chrome, because it has to.**
  The first one used `curl` on the reasoning that the flow is redirects
  and one form POST. It is not: the suite's callback is an HTML page that
  posts the result back **with JavaScript**, so `curl` reached it, never
  ran it, and every module sat in WAITING while the authorization it was
  waiting for had already succeeded. From outside that is indistinguishable
  from a hang.

  `hack/conformance_drive.py` drives Chrome over the DevTools protocol and
  submits the recovery form in the page, so the browser follows the
  redirects itself and runs the callback's script.

- **What a recovery sign-in cannot prove**, now written down. A recovery
  subject is a ServiceAccount: no address, no name, so `userinfo` returns
  no `email`, `name` or `preferred_username`. Every module checking the
  claims a scope implies warns for a reason that does not exist when a
  person signs in. The unattended run is evidence for the protocol
  modules and not for the claim-bearing ones.

- **Run one plan at a time.** The suite serves every plan's callback under
  its alias and the clients register one callback, so a second plan with
  a different alias is refused a redirect it never registered, and one
  with the same alias interrupts the running test — which it reports only
  in its own log, so from outside it looks like a hang. This is what
  stopped the first attempt.


- **The Sessions page lists the SIGN-INS**, above the sessions they
  opened. This is what made revoking look like a no-op: the page showed
  every per-client session and no sign-in, so emptying the list changed
  nothing about who could walk back in. Worse, the console's own sign-in
  appeared nowhere at all — it never redeems the code it gets back, so it
  opens no session — while being the very thing keeping the reader signed
  in.

  Each row ends that browser's sign-in and everything under it.
  `ListSessionsResponse` gains `sign_ins`, the sign-in store gains a
  global index beside its per-identity one, and both are returned under
  the same permission rule as the sessions.


- **Signing out leads back to signing in.** The signed-out page said what
  had happened and left you there; sign-out now lands on the console,
  which is where a sign-in can actually start. Not `/login` itself: its
  buttons carry the id of a pending authorization request, so visiting it
  without one is a page that looks like a sign-in and cannot finish. The
  console's front page sends an unauthenticated browser through
  `/authorize`, which makes that request. A deployment with no console
  still gets the signed-out page, which is the honest ending for one.

- **"Sign out everywhere" on your own page follows through.**
  *Everywhere* includes here: naming no client ends the sign-in as well
  as the sessions, so the page was left acting signed in until its next
  call failed with a sentence about tokens — reported as an error on
  revoke, and it was the trace of the sign-out that had already worked.

- **A refused call says the session ended**, rather than repeating the
  issuer's sentence about tokens, which is true and is not what happened
  to the person reading it.

## v0.13.3

- **Sign-out still did not work, and for a second reason.** The route
  existed but answered **GET only**, and the console sends **POST** — its
  own sign-out was a POST on the reasoning that a link which logs you out
  is a link anyone can put in a page. So the button got a 404 from a
  route that was there.

  It was verified the first time with `curl`, which sent GET, so the
  proof missed exactly the path the button takes. The network tab showed
  it in one line: `logout POST 404`.

  `/logout` now answers both. And the console is told the issuer's
  sign-out **absolutely**, because it treats a bare `/logout` as its own
  and fetches it — right where it serves that route, wrong here, where
  the route answers with a redirect a fetch would swallow and leave
  somebody reading "signed out" with the session still open.


- **The account block went back to the foot of the rail.** Moved up and
  moved back on looking at it: near the brand it competed with the
  navigation for the first thing the eye lands on, and a console is
  opened to go somewhere rather than to check whose account it is. What
  it needed was not height. It was a sign-out that says *Sign out*
  instead of an icon to guess at, and that stays — as does Settings in
  the list of places to go rather than alone in the foot.

## v0.13.2

- **The Config profile runs in CI**, daily and on demand
  (`.github/workflows/conformance.yaml`), and the three profiles are
  named in the README with which of them can run unattended and why.

  Config is the one that can: it reads the discovery document and the key
  set, so it needs no client, no secret and nobody at a browser. It also
  guards the surface most likely to break by accident — change a provider
  option and discovery changes with it, silently, for every relying party
  that reads it.

  **On a schedule rather than on a tag**, deliberately. A tag is a build,
  not a deployment: at the moment `v1.2.3` is pushed the cluster still
  runs what it ran before, so a run then would certify the OLD issuer and
  file the result under the NEW version. Certifying the tag itself means
  standing its image up inside the job with a policy, a signing key and a
  certificate the suite accepts — worth doing, and a different job.

  The other two sign somebody in, and the signing-in needs a credential
  from the cluster. CI has no business holding that, so they stay a
  person's job — `hack/conformance-drive.sh` makes thirty sign-ins one
  command.


- **The rail puts who you are at the top, and says "Sign out".** The
  account block sat at the foot of a scrolling column, which is the last
  place somebody looks for the first question a console like this raises:
  *which account am I looking at this with*. Settings sat down there too,
  alone behind two dividers, rather than in the list of places to go.

  Sign-out is a labelled button now instead of a bare icon. An icon alone
  is a guess, and this is the one control nobody should have to guess at
  — it was reported as not working when it was both hard to find and, as
  above, a 404.


- **Two directories read `provisional · stale` on a service that was
  working.** The refresh lease was held for the whole refresh interval,
  so the lease and the ticker were the same length and beat against each
  other: a tick arriving a second before its predecessor's lease expired
  was refused, and the next chance came a whole interval later. The
  effective period doubled to thirty minutes — **exactly the freshness
  window** — so the snapshot aged out and every domain in it lost
  authority.

  The evidence was in the ages: two directories at 32 minutes, and a
  third that happened to miss the collision at 17. The lease now runs
  three quarters of the interval, which still refuses a replica whose
  turn comes a few minutes later and never refuses one ticking on
  schedule.

  **The start-up catch-up now refreshes what is DUE rather than what is
  already stale.** A fourteen-minute-old snapshot on a pod that has just
  replaced another used to wait a whole interval more, reaching
  twenty-nine minutes — one rollout short of provisional. A restart
  should not extend the schedule, and an afternoon of releases should
  not look like a directory going bad.

## v0.13.1

- **Sign-out did nothing, because nothing served `/logout`.** The
  console's button pointed there, the issuer answered 404, and the
  sign-in survived — reported as *"even sign-out button does not work"*,
  which it did not. The issuer serves `/logout` now: it ends the browser's
  sign-in, clears the cookie and lands on the signed-out page.

  `/logout` rather than pointing the console at `/end_session`, and
  that is the right call: this is the address a person expects and
  types, `end_session` is a name from a specification, and the console
  could not supply the `id_token_hint` that endpoint wants anyway — it
  never redeems the code it gets back, so it holds no ID token. Both end
  the same thing.

- **The signed-out page stopped linking to a page that was deleted.** It
  sent people to `/account`, whose page went into the console. It now says what
  ending a sign-in does and does not reach, and points at the console's
  Sessions page for the rest.

- **`hack/conformance-drive.sh`** completes the browser half of a run
  without a browser. Nothing in that flow is JavaScript — a chain of
  redirects and one form POST — so it follows them with `curl` and signs
  in through recovery, which is a ServiceAccount token rather than a
  person at a Google prompt. It signs in fresh each time, because that is
  what the logout modules exist to check.

  It also treats `INTERRUPTED` as terminal. `conformance-run.sh` did not,
  which is why an alias conflict looked like a hang: **run one plan at a
  time**, because the suite serves every plan's callback under its alias
  and starting a test in a plan that shares one interrupts the running
  test.

## v0.13.0

- **Revoking every session left the sign-in standing.** Reported from the
  live console: *"I revoked all sessions, but still has access
  everywhere."* Every Revoke button sent a session id, and that path ends
  one session and nothing else — so a browser whose rows were all revoked
  kept its SSO session, the list emptied, and the next `/authorize`
  completed silently with no password. The half sign-out this design
  names twice, shipped in the console that was supposed to prevent it.

  The Sessions page now offers **signing the browser out** beside the
  rows, which ends the sign-in and every session under it. The rows keep
  their narrow meaning, because ending one session that is not the one
  you are using is a real thing to want.

  `RevokeSessionsRequest` gains `sso`, and it is checked against the
  identity before anything is ended — the permission check is against the
  identity the caller names, so without that an id alone would end
  somebody else's sign-in. Both properties have tests, and the first was
  verified to fail without the fix.

  **What this does not reach** is a relying party's own session. A console
  that ran its own flow holds its own cookie; `end_session` is
  front-channel and back-channel logout is not built, so Kargo answers
  until its own session expires however thoroughly you revoke here. Said
  plainly in `docs/design/sluis.md` rather than left to be
  discovered twice.

- **`hack/conformance-run.sh` runs a whole plan.** The suite's page has no
  *run all* and Basic OP has thirty modules. It starts them in order,
  waits for each, prints the result, and names the URL to open for the
  ones that genuinely need a person at a sign-in. `--from` resumes after
  a failure rather than re-running what passed.

## v0.12.8

- **Filter rows stopped stepping sideways when they wrap.** MUI's
  `spacing` sets margins on children and `gap` does not, so a row that
  declared both wrapped with a stray indent on every line after the
  first — visible on the Sessions filters on a phone, and latent in three
  more rows. The rows that wrap now use `gap` alone.

## v0.12.7

- **Every filter on every page wraps now.** Provider groups was still
  scrolling 73px sideways on a phone after the first pass, because it had
  its own copy of the control. Rules and its proof simulator are on the
  shared one too, so there is one filter in the console rather than five
  spellings of one.

## v0.12.6

Found by signing in to a running console with a headless browser and
looking at every page at 1440px and at 390px.

- **The Overview stopped shouting.** "Needs attention" rendered one row
  per internal group that opens no client — **73 of them**, because the
  clusters and cloud accounts that will require those groups are not
  connected yet.
  A healthy installation read as a broken one, the page was 4832px tall,
  and the working state was pushed off the screen. Runs of the same
  finding now collapse into one row with a count once there are more than
  three: below that the names are the information, above it the count is.

- **The filters fitted on a phone.** A `ToggleButtonGroup` is a flex row
  that does not wrap, so twelve domains ran off the side of the screen
  and took the page's width with them — the People page scrolled 182px
  sideways and Provider groups 73px, with the filter itself unreachable.
  One shared `Facet` now wraps, and three pages that had hand-rolled the
  same control with slightly different spacing use it.

- **The account block stopped linking to a page that cannot exist.** A
  recovery sign-in has no address — it is a ServiceAccount the cluster
  vouched for — so the link went to an empty person page. It still says
  who is signed in, and shows the full subject on hover rather than its
  first twenty characters.

## v0.12.5

- **The conformance runbook was followed, and it was wrong twice.**
  It told you to leave `requires` off the two temporary
  clients — the policy refuses a client that requires no group, so the
  render would have failed before a single test ran — and it gave their
  hostname without the port, which the render also refuses, because a row
  names one host and the redirects are on `:8443`. Both are fixed, and
  the page now records what the last run actually reported.

  **Config profile: PASSED against the merged service**, 34 checks, no
  failures and no warnings. The two attended profiles need a person at a
  browser and are the remaining gate.


- **The documentation describes what ships**. The sweep found
  seven Go symbols the guides promised and the module does not export —
  `identity.NewIssuerVerifier`, `identity.ClusterConfig`,
  `identity.ServiceAccountRef`, a `directory` package, an `authz`
  package — every one written down as though it shipped. A stranger
  following `docs/connect/console-app.md` or `service-to-service.md`
  could not have compiled what they were told to write.

  `just check` now asks the compiler: `hack/check-docs-symbols.py` runs
  `go doc` for every `identity.`/`tokens.`/`policy.` name the docs use,
  and fails on one that does not exist. Nothing compiles a code block in
  a Markdown file, which is why this drifted in silence.

- **`docs/integrations.md` stopped carrying a banner saying it was
  wrong.** Cases ④, ④b and ⑤ described the two-service shape; ④ is now a
  workload proven against its own cluster's published key set, ⑤ is a
  function call, and ④b is gone with the listener it described. The
  two-anchor summary was rewritten too: recovery alone stands on the
  cluster now.

- **`docs/connect/console-app.md` covers both console shapes** — a proxy
  in front, or its own flow — and says which is which, with the real
  `identity.Middleware`/`identity.Require` rather than a package that was
  never built. It also warns against registering a sign-out landing page
  as a redirect, which is the trap the cutover hit.

- The install line for the TypeScript package named a tag from eight
  releases ago.

## v0.12.4

- **The console says `access-roster`.** It said `directory-roster`, which
  was the name of a service that no longer exists, and the wording around
  it still called this "the hub" — a word that meant something only while
  there were two services. The provider pages, the setup steps and the
  settings hints say `access-roster` now.

- **The Sessions page is a table.** Four facts per session were stacked
  into one caption line, so a page that can hold a hundred rows used a
  third of its width and lined up none of its columns: a reader scanning
  for *whose session expires soonest* had to read every line. It is now
  Person, Client, Way in, Browser, Opened, Last used, Expires, with a
  facet for the way in that offers only the ways actually present.

  **The browser grouping became a column** rather than a run of
  subheadings. Two rows carrying the same mark came from one browser, and
  a blank is a session with no browser behind it at all — which is what a
  token exchange is. A column can be compared down the page, which is the
  one thing a subheading cannot.

  The grouped list stays for a person's page and a client's page, where
  it sits in a narrow column, holds three rows, and the grouping is the
  point.

## v0.12.3

- **The Sessions page came back.** It vanished at the cutover, silently:
  the console learned where its issuer was from the FORWARDED bearer's
  issuer, which the proxy in front configured, and on one origin there is
  no proxy. An empty issuer reads as *there is no issuer to talk to*, so
  the console hid the Sessions page and the sessions section of a
  person's page — on exactly the deployment where they work best, since
  the call is now same-origin and carries the browser's own SSO cookie.

  The merged process now tells the console its own issuer URL, the same
  way it already hands over the session reader and the sign-in entry.
  Nothing failed and nothing was logged, so the test asserts `whoami`
  reports an issuer and fails without the wiring.

- **The Providers page filters by provider and by state.** The state
  facet offers only the states actually present, so it never shows a
  button that returns nothing — and one function decides a domain's state
  for both the chip and the filter, because two would drift.

- **The People page's domain filter lists only served domains.** A
  provider discovers every domain its tenant owns, most of them parked;
  offering eleven when three can hold an account made the filter mostly
  buttons that return nothing, and hid the ones that work among them.


- **`hack/cutover-cleanup.sh`**, for the two messes the one-service cutover
  leaves that will not resolve on their own.

  A **stuck ValkeyCluster**: the retired store was pruned with foreground
  propagation, so it waits for its dependents while the Valkey operator
  keeps recreating them. The tell is that its pod, StatefulSet and
  Service are a few seconds old however long you watch.

  And **orphans**: the two retired Applications were pruned without a
  cascade, so everything they owned still runs with a tracking-id naming
  an Application that no longer exists. Nothing owns them, so nothing
  will ever prune them — and an orphaned HTTPRoute still competes for its
  hostname.

  It refuses to run unless the proxies' session store and the workspace
  records are both present, because those are what must survive, and it
  names every object rather than selecting by label: a selector here
  would also match what the service itself wrote.

  **In the event neither was needed.** ArgoCD finished the cascade on its
  own a few minutes later and the operator let the store go. The script
  stays because the state it describes is real, was live for about twenty
  minutes, and is not something to diagnose a second time from scratch —
  and because next time it may not resolve itself.

## v0.12.2

- **`console.mount` accepts `/console/` as well as `/console`.** The
  chart matched the trailing-slash spelling exactly and then redirected
  it to `/console//`, a path the console does not serve — from a value
  nothing rejects, because both are legal strings. Our own configuration
  writes it both ways: a declared client carries `prefix: /console` and
  `mount: /console/`. The Go side already trimmed it; now the chart does
  too, and `just check` renders both spellings and diffs them.

  Found while pointing a live installation's values at the merged chart, which
  is exactly where it would have bitten.

# Changelog

One line per release; full detail lives in the release notes and the
git history.

## v0.12.1

- **The v0.12.0 release published nothing**, and the reason is the one
  this repository's own release config warns about: release machinery is
  exercised only by a tag. `accessctl` is the single binary built for
  Windows, so in one shared archive the Windows download held one binary
  where every other held four, and goreleaser refuses that — four minutes
  into the tagged run, after every cross-compile had already succeeded.

  `accessctl` now has its own archive, zipped on Windows as that platform
  expects, which is also the better shape: it is the one thing here that
  runs on somebody's own laptop.

  And `just check` now proves every archive is uniform across its
  platforms by reading the same file goreleaser reads. Neither
  `goreleaser check` nor `build --single-target` reaches the archives
  stage, which is why nothing caught this.


- **A session cookie is marked `Secure` by default**, decided by the
  scheme of the service's own public URL rather than by a flag that
  defaults to off. It used to default to off, so an installation that
  simply did not set `SECURE_COOKIES` served its session cookie without
  the flag and a proxy could carry it over a plain-http hop. The chart
  always set it, which is why nothing was wrong in our own deployments
  and why the alert CodeQL raised was about the default rather than
  about any line it pointed at.

  `SECURE_COOKIES` still overrides in both directions, for a TLS
  terminator the URL does not mention.


- **The Action's own comment was an expression.** GitHub evaluates
  `${{ ... }}` anywhere inside a `run:` block, including in a shell
  comment, because the block is a string value before it is a script.
  A comment added in v0.12.0 to explain which inputs are untrusted named
  an event expression literally, so every run of the action would have
  expanded it. Found by CodeQL, which was right to call it code
  injection. **v0.12.0's action is broken; use this.**

- **What reaches `GITHUB_ENV` is now the profile name this run wrote**,
  not the input that selected it. The check against the written list was
  already there, but passing the input through meant the value's shape
  still came from the workflow. A name built here is `role@account` from
  an audience whose whitespace was stripped, so it cannot carry the
  newline that would declare extra environment variables.

## v0.12.0

- **The GitHub Action's `default-profile` is checked against the profiles
  the run actually wrote.** It reached `GITHUB_ENV`, so a newline in it
  declared arbitrary environment variables for every later step of the
  job — the input is trusted only as far as whoever wrote the workflow,
  and an expression like `${{ github.event.* }}` is not trusted at all.
  Found by CodeQL. Checking it against what was written closes that and
  also catches a name that is simply a typo, which would otherwise
  surface as an AWS error three steps later about a profile that does
  not exist.

- **The GitHub Action** (the other half). One action at the
  repository root, `curl` and `jq` and two files: nothing of ours is
  downloaded into a job, and there is no version of ours to bump when
  Amazon's tooling moves. One exchange per audience, a profile per
  `aws:<account>:<role>` with `web_identity_token_file`, a kubeconfig
  context per `k8s:<cluster>`, and every token masked before it is
  written anywhere.

  Exercised end to end against a stand-in for GitHub's id-token service
  and the issuer, which found the bug that would have hurt: `printf '%s'`
  leaves the last line unterminated and `read` drops it, so **the final
  audience of every job was silently discarded** — surfacing as a missing
  profile rather than an error.

- **`accessctl`** (the CLI half). `login` runs the browser flow
  once — authorization code with PKCE on a loopback port, the only
  browser flow left since the device flow was withdrawn — and caches the
  refresh token in a 0600 file. Everything else shares that cache:
  `whoami`, `kubeconfig`, `aws-config`, `setup`, `exchange`, and the two
  credential helpers `kube-token` and `aws` that kubectl and the AWS SDKs
  run themselves.

  It writes into two files that belong to somebody else, so it is careful
  about both. The kubeconfig goes through `kubectl config` rather than
  being rewritten, because a person's other contexts are none of this
  tool's business. The AWS config is rewritten only between two markers,
  so a profile for a role somebody no longer holds does not survive as an
  entry that fails when used, and nothing outside the block is touched.

  Exit codes are a contract: 2 usage, 3 not signed in, 4 audience not
  granted, 5 issuer unreachable — so a wrapper can tell *sign in again*
  from *the issuer is down*, and knows not to retry a refusal.

- **A public `tokens` package**: the RFC 8693 exchange, and the two
  envelopes a credential helper has to speak. It is what `accessctl`
  will run on and what a workload can use directly.

  The exchange presents its client in HTTP Basic, because the library on
  the other side reads it from Basic alone and a posted `client_id` is
  refused with an error naming the client rather than the mistake. A
  refusal carries the issuer's own sentence — which audience, and which
  groups the proof holds — since that is the whole value of the error in
  a build log.

  `WriteExecCredential` answers whichever apiVersion kubectl asked for,
  and carries the expiry so kubectl caches instead of running the plugin
  on every API call. `WriteCredentialProcess` writes real STS
  credentials with `Version` as the **number** 1, and
  `AssumeRoleWithWebIdentity` obtains them — unsigned, which is why the
  AWS path needs no stored key: the token is the proof and the account's
  trust policy decides what it opens.

- **A public `identity` package, and access-roster uses it** (the
  Go half). Two verifiers, one per anchor: `Issuer` for a token this
  installation signed, `Cluster` for a ServiceAccount token from the pod
  next door. Both yield one `Verified`, so a handler never learns which
  anchor proved the caller and cannot come to depend on it — the right
  anchor is decided by how far away the caller is, and that can change
  without the handler. No group re-mapping anywhere: the name in the
  policy is the name in the token is the name in the role check.

  With a `net/http` adapter: `Middleware` establishes the caller and
  passes the request on, because a listener serves pages that run before
  anybody is established; `Require` is what refuses, and never names the
  group that would have worked. `WhoAmI` answers rather than refuses when
  nobody is signed in, because that is something a console has to render.

  `Cluster` takes a review function rather than building one, so a
  consumer that only needs the issuer does not inherit Kubernetes client
  libraries.

  **The service consumes it.** `internal/server/forwarded.go` is now a
  thin adapter over `identity.Issuer` instead of a copy of the same
  verification. Doing that caught a narrowing: the lifted reader matched
  `Bearer ` exactly, and RFC 6750 makes the scheme case-insensitive —
  real clients send both spellings, and a caller that had done nothing
  wrong would have been refused.

- **GitHub team bindings are a table in the policy** (the
  access-roster half). `github: <org>: <team>: [provider groups]`, read
  exactly like a group's `members`: the people the directory puts in
  those groups are the people that team should contain. It grants
  nothing here and appears in no token — a controller reads it and makes
  the organisation match — and it lives in this file for one reason, that
  a reader of the access model sees every team's source without opening
  another file.

  The console's Rules page lists them beside the rest with the same
  *depends on* column. They feed a team rather than an internal group, so
  they open no client and the page says so. A team fed by an empty list
  is refused, because "remove everyone from platform" is not something to
  express by leaving a list out; the same team declared in two merged
  files is refused too, because the second would silently replace the
  first.

  The controller, the read-only GitHub page and the directory endpoint it
  authenticates against are the other three parts of this work and wait
  for github-roster.

- **The console signs people in as a client of the issuer**,
  which is what makes one binary mean one door. Somebody with no session
  is sent to `/authorize` with the console's declared client, signs in at
  the issuer's page, and comes back with the issuer's session set. No
  proxy in front of the console running an OpenID flow against a service
  in the same process, and no login of the console's own.

  The code that comes back is never redeemed: what the console needed was
  the session, not a token, and it reads the directory and the policy in
  this same process. It is stripped from the URL so it reaches no
  bookmark and no referrer. Set `console.client` to a declared client
  whose redirects name this origin plus the mount; empty keeps the
  console's own page.

- **The console gets its own HTTPRoute**, and that is not tidiness. A
  gateway policy attaches to a *route*, so anything put in front of the
  console on a shared route would also sit in front of `/token`, `/keys`
  and discovery — every relying party in the estate asked to sign in to
  fetch a key set. It renders whether or not anything attaches to it,
  because discovering at cutover that there is nothing to attach to
  leaves only that bad option. The prefix is deliberately not rewritten
  away: the service strips it itself, so a gateway that stripped it too
  would hand the console a path it never serves.

- **The console admits whoever the issuer signed in.** One origin and one
  process, so the browser's issuer session is read directly rather than
  being relayed. Before this, a console on the issuer's own host was
  authenticated either by a proxy — which ran an OpenID flow against a
  service in the same process, a network round trip and a second session
  store to learn something already known — or by a login of its own,
  which is the second door an installation with a gateway deliberately
  turns off.

  It does **not** remove the need for a way to *start* a sign-in. A
  person arriving with no session anywhere still needs one, and the
  console's own login or a proxy in front of it is still what provides
  that. What this removes is the second session for a person who already
  signed in somewhere behind this issuer.

- **An installation that signs nobody in yet still serves its sessions.**
  The handler returned early on "no sign-in providers" and took the
  session service and the signed-out page with it. The posture where that
  bites is day one: recovery is available with no OAuth client configured
  — that is the whole point of it, the way in before any directory is
  connected — and a recovery sign-in opens a session like any other. An
  operator who had just recovered could not then list or revoke anything.
  The same mistake, in a new shape, as gating the session service on
  `console.origin` once did.

- **The endpoint reference no longer promises two endpoints that do not
  exist.** `/.access/grants` and `/.access/simulate` were listed as
  served. Neither is implemented; both belong to `accessctl`,
  and the table now says so and names what answers the same question
  today.

- **A workload's cluster survives the exchange.** The verified proof
  travels through the OpenID library as a map of claims and is rebuilt on
  the other side, and the cluster was not among them — so it was dropped
  in silence. Two halves of that loss: the subject stopped naming the
  cluster, and the same namespace and name exist on every cluster, so two
  different machines became one `sub` — exactly the collision the
  qualifier exists to prevent. And a `service_account` matcher narrowed
  to one cluster was compared against an empty string, so it matched
  nothing at all and an operator would see a rule granting nothing with
  no reason visible.

  It predates the federation work — the qualifier never reached a token
  through exchange — but federation is what makes it reachable, because
  until now there was one cluster.

- **Both halves of the merged service act on one policy**, loaded once
  and handed to the issuer rather than loaded twice. They read the same
  file in a real deployment, so the disagreement stayed hidden — but
  their fallbacks differ, and two halves that *can* disagree about the
  policy is exactly the class of failure the merge existed to end. Found
  by booting the merged binary: with `DEMO=1` the directory half built a
  demonstration policy and the issuer half refused to start on an empty
  `POLICY_DIR`.

- **One design document.** `docs/design/hub.md` and
  `docs/design/access-issuer.md` fold into
  `docs/design/sluis.md` — the directory model, freshness, the
  policy, the three proofs, the six grants, sessions and one origin, the
  console, the store, recovery, failure semantics — and end with an
  appendix naming everything that was removed and why, so nobody adds one
  back without a reason. `architecture.md` loses its two-services banner
  and draws one container. The README no longer promises a merge that has
  happened.

  `integrations.md` carries a note on the three cases that describe the
  old shape; it is rewritten with the rest of the documentation at 1.0.

- **The console reads.** It no longer writes who is in which internal
  group. A console that could add a membership was a second
  source of truth beside git and a merge layer to reconcile them; who is
  in a group is now the policy, rendered from the installation's own
  access model, and `git log` is the complete history of access. Gone:
  the `memberships` table (refused now, not ignored), the console layer
  of the policy, the `layer` field on every member, and `SetOAuthClient`
  — the OAuth client is a Secret, delivered the way every other
  credential in the estate is.

  Kept, and each for a stated reason: **connect a provider** by admin
  consent, because Google's consent genuinely needs a browser and there
  is no infrastructure-as-code way to obtain that credential; and
  **revoke a session**, which is a removal and the lever between
  sign-out and expiry.

  Nothing was lost in the change: no installation had ever attached a
  membership through the console. Still to come under the same ticket:
  a provider's `serve` and `synced` are chosen in the console today, and
  become values once the values can name a consent-connected provider.

- **The issuer's UI is the login page.** `/account` — a person's own
  sessions and *sign out everywhere* — was server-rendered by the issuer
  because it had to be same-origin with the session service.
  The console is same-origin and, since the merge, the same process, and
  its page for a person already shows both. Two pages showing one thing
  is two things to keep true of each other, so the issuer's is deleted
  and the address redirects into the console. The two POSTs behind it go
  with it: an endpoint that answers after the page using it is deleted is
  surface nobody is keeping honest.

  What the issuer still renders is `/login` and `/signed-out`, and both
  stay for the same reason: each runs before there is anyone to
  authorize, so neither can be a console page.

- **One issuer, many clusters, access to none of them.** A workload's
  ServiceAccount token is verified against the key set its own cluster
  publishes, never by asking the cluster. Asking meant a
  TokenReview, and a TokenReview against a cluster elsewhere meant holding
  a kubeconfig for it — inside the service whose whole design is to hold
  almost no credential. A key set is public: EKS publishes one per cluster
  (it is what IRSA rests on) and Talos serves the same keys at the API
  server's `/openid/v1/jwks`. So a remote cluster's workload proves itself
  exactly the way a GitHub job does, and connecting one is a row of
  `exchange.clusters` naming a URL.

  This service's own cluster is a row like any other. There is no special
  case for it, because a special case is a second code path that only one
  installation exercises. The TokenReview verifier is deleted, and the
  cluster-scoped permission the chart creates now belongs to recovery
  alone — on the day everything else is broken it should depend on
  nothing but the API server.

  What this gives up, plainly: a TokenReview notices a deleted
  ServiceAccount and a key set does not, so a token stays usable until it
  expires. Bound tokens are short-lived, so the window is minutes.

- **Six grants, and the four that were served are gone.** The issuer
  serves the code flow with PKCE, refresh, userinfo, `end_session`,
  revocation and token exchange, and nothing else. The device
  flow is for a machine with no browser, and both headless cases here — a
  CI job and a workload — are token exchange. Client credentials is a
  machine with a stored secret, which is the thing this design exists not
  to have. JWT bearer is token exchange with a different spelling.
  Introspection never applied: these are JWTs, verified offline against
  the key set.

  Withdrawn from discovery *and* from the storage, which is the part that
  matters. The device flow stops working because nothing implements the
  library's device interface any more, so there is no device state kept
  and no way for a code to be stored by something that changed its mind.
  A test asserts each withdrawn grant is refused at `/token`, because an
  endpoint that answers after its metadata stops mentioning it is the
  failure that hides.

  `grant_types_supported` prints three, not six: three of the six are
  grants and three are endpoints, which discovery advertises in fields of
  their own. Listing an endpoint as a grant type would be the metadata
  lying in a new way.

- **One service.** The directory hub and the issuer are one process.
  The issuer asks the directory by calling a function instead
  of dialling a service, so a login now makes no network call except to
  the corporate directory: gone from every single sign-in are a
  ConnectRPC round trip, a TokenReview, a NetworkPolicy hop, and the
  class of failure where the two halves disagree about the same person.
  The console is served on the issuer's own origin under `/console/`,
  which is what lets its session pages use the browser's cookie with no
  bearer in JavaScript, and one `/readyz` answers for both stores. The
  split was built so that several things could ask the directory; the
  issuer became its only consumer, and the endpoint comes back on the
  merged service when the GitHub controller needs it.

  The console is now told where it sits, because a prefix that used to be
  stripped by the gateway also has to appear in every link the console
  hands a browser: `/login` resolves against the origin, where the
  issuer's page is.

  The `access-issuer` chart renders the whole of it: `directory.*` for the
  workspaces and their freshness, `console.mount` for where the console
  sits, a namespaced Role for the store, and no projected token for a hub
  nobody dials. `hub.address` is gone. The `directory-roster` chart and
  binary still ship unchanged, so an installation moves when it chooses
  and can move back; they go once nothing points at them.

  **Moving an installation is not only a chart switch.** What an operator
  connected — the workspace records and their credentials — lives as
  ConfigMaps and Secrets in the namespace the hub ran in. Copy them into
  the issuer's namespace before the cutover, or the new pod starts with no
  directories connected.

- **The console says *provider*, and the tables split domain out.** What
  the code calls a workspace the console now calls a **provider**, and
  the groups it holds **provider groups**; both sides of the rail then
  say simply *Groups*, and the heading above each — *Where people come
  from*, *Internal* — tells them apart, so the reader no longer carries
  a qualifier down every page. Providers list one row per *(provider,
  domain)* pair rather than stacking domains inside a cell, because the
  row is the unit a reader compares. Groups gain filters by provider and
  by domain; People gains a **Domain** column and a domain filter, and
  that one is applied by the hub (`SearchPeopleRequest.domain`) rather
  than over the page the console received — this list is capped at 200,
  and narrowing it in the browser would answer *nobody* while the
  snapshot holds hundreds. URLs and the API keep the old words, so
  nothing bookmarked or scripted moves.

- **A moved Valkey no longer needs a human.** On 2026-09-10 a Valkey pod
  was rescheduled onto a new address; the issuer and a console's proxy went
  on dialling the old one for half an hour, reported **Ready**
  throughout, and had to be restarted by hand. Three things were wrong
  and all three are fixed. **Cluster mode is off by default**: with one
  shard it makes the client learn node addresses from `CLUSTER SLOTS`
  and talk to those, bypassing the Kubernetes Service — the one
  mechanism whose whole job is to survive a pod moving. **Readiness now
  follows the shared store** and liveness deliberately does not, so a
  replica that cannot reach it leaves the gateway's rotation and answers
  fast instead of hanging, while one blip cannot restart the fleet; the
  refusal names the dependency and the reason. And **a test proves the
  recovery** rather than assuming it: stop the server, start it again,
  and the client must work without being reconstructed. It recovers in
  about two seconds.

## v0.11.0

- **The documentation is rewritten around what the product is.** The
  README opens with the idea in three sentences and the niche in one
  table: dex is the right shape and stops one step short of knowing
  anyone's groups; the heavy providers can do all of it at the cost of
  running an identity product to use a fifth of one. `architecture.md`
  is a third of its length, draws the live two-service shape honestly
  and names the merge that follows; `why.md` carries the seven problems
  and seven principles and nothing the README already says. Seven
  decisions taken 2026-09-10 are recorded where they land — one
  service, machines by a federated key set with token exchange
  as the one machine grant, six grants, a read-only
  console, the login page as the issuer's only UI —
  and the two design documents carry a banner saying which of their
  sections are current and which are history. No code changes.

## v0.10.0

- **A runbook for the conformance run.** `docs/operations/conformance.md`:
  the suite from published images so there is no Java build, the Config
  profile as a script that needs no client and no browser, and the two
  attended profiles as a checklist — including the two temporary clients
  they need and the step to remove them afterwards, which is the one
  that gets forgotten.

- **A refused bearer at `/userinfo` now carries a challenge.** RFC 6750
  requires a `WWW-Authenticate` header on a 401 from a bearer-protected
  endpoint; the library answers an unusable access token with a bare
  error and no challenge, which is the one shape a conforming client
  cannot act on — it is told it is unauthenticated and not told what
  would fix it, so a client library reports a transport failure or
  retries the same token for ever. Found by the conformance work,
  fixed in a wrapper because the header has to be set before
  the status is.

- **The API listener's consumers hold a grant.** Admission and
  authorization were one decision: a consumer admitted at all could
  enumerate every group of every company the hub reads. One consumer —
  the issuer — needs exactly that; a team-sync or a cross-cluster hook
  needs one directory and one question. A consumer is now declared with
  a grant along three axes — which directory (`workspaces` or `domains`),
  which `groups`, which `reads` (`resolve`, `groups`, `describe`,
  `probe`). No grant is full read, so nothing declared before this
  changes meaning. Outside the grant answers exactly as an unserved
  domain does, because a refusal would confirm what the grant withholds;
  `Describe` lists only granted domains, so discovery is scoped too; and
  the listener stays read-only whatever a grant says. **Breaking for a
  hub configured by environment alone:** `API_CONSUMERS` is gone and
  consumers are declared in a mounted file, because a grant does not fit
  in a comma-separated list in any spelling an operator could read. The
  chart renders and mounts it from the same `consumers[]` values.

- **The console's Matchers page becomes Rules.** It listed only
  rules that admit a proof by its *shape* and silently omitted those that
  admit by directory membership — 30 of 73 groups on the first real
  installation — so filtering it by a cluster role returned nothing and
  read as missing data rather than as the wrong page. How a rule is
  evaluated is a property to show in a column, not a reason to split the
  answer: a membership rule needs the directory to vouch and degrades to
  the hold window when it cannot; a matcher needs only the proof, which
  is why recovery is one. The page now shows every rule that puts an
  identity into an internal group, adds a **directory group** tab and a
  **depends on** column, and its group filter answers for all 73 groups
  rather than 30. `#/matchers` still resolves, so a bookmark does not
  land on the overview. The policy's `matchers:` field is unchanged, and
  People, Directory groups and Internal groups are untouched: they answer
  the same graph identity-first, source-first and target-first, and Rules
  is the fourth direction rather than a replacement for any of them.

## v0.9.15

- **The session service is mounted whether or not a cross-origin console
  was configured.** It was gated on `console.origin` — a value that
  answers a different question, *may some other origin call this* — so on
  one origin, where there is no CORS to configure and nobody sets it, the
  service was never mounted. Every sessions section in the console
  answered 404 while `/account`, server-rendered beside them off the same
  store, worked perfectly: the half a person is most likely to try
  working, and the half a console shows not. The value now decides only
  whether the CORS wrapper goes on.

## v0.9.14

- **The console asked the wrong host who it was.** `/.access/whoami` was
  fetched as an absolute path, so mounted under `/console/` it resolved
  against the **origin** — the issuer — which serves no such endpoint.
  The console concluded nobody was signed in: it offered *Sign in* to
  somebody already authenticated and disabled every operator control,
  while the Connect calls beside it worked, because those had been
  fixed for the mount point and this had not. Every path the console asks
  for now goes through one `mounted()` helper, so there is one place to
  get it right rather than one per call site.
- **A snapshot already past its freshness window is refreshed at start.**
  A ticker's first tick is a whole interval away, so a restart served the
  previous process's snapshot for fifteen minutes — and a deployment
  rolling more often than that never reached a tick at all. The snapshot
  aged past the window and every domain read *provisional*, which is a
  release cadence showing up to an operator as a loss of authority. Only
  what is already stale is re-read, and the existing lease still means
  replicas starting together read once between them.

## v0.9.13

- **The console's assets are referenced relatively, so one bundle serves
  at any mount point.** Mounted under a path they were requested from the
  origin root — where the issuer answers — so every asset 404'd and the
  console did not load. A build-time base path could not fix it either:
  the bundle is committed and embedded, so baking one deployment's prefix
  into it would ship that prefix to all of them. `./assets/…` resolves
  against the page instead. It requires the trailing slash, so the bare
  prefix now redirects to itself with one, and the Connect transport
  resolves its base from the page rather than a literal.
- **A bare GET of the issuer's host can land somewhere useful.**
  `route.rootRedirect` on the issuer — the issuer serves nothing at `/`,
  every endpoint it answers being a named one, so a person who types the
  domain got a 404. Empty keeps that 404, which is honest for an issuer
  deployed alone.
- **`/register` is dropped, and every client is declared.** Only our own
  proxy could ever have called it; an endpoint that mints clients is the
  one surface an issuer least wants; and declaring them keeps *who can
  obtain tokens for which audience* answerable by reading a repository
  rather than by querying the running service. `registration.enabled`
  leaves the access-proxy chart, `/register` leaves the issuer's surface,
  and the console's Clients page is read-only by construction rather than
  by policy. The drift it would have prevented is closed by generating a
  console's client from one row instead.

## v0.9.12

- **The sign-out chain moves with the console.** Mounted under a path,
  the console's sign-out link still pointed at `/oauth2/sign_out` at the
  root — which belongs to the issuer, not the proxy — and its
  `post_logout_redirect_uri` was the host root rather than the console's
  own page, so it matched no `signed_out` entry and the issuer landed the
  person on its own page. Both halves fail quietly: one is a button that
  404s, the other is a sign-out that worked and reads as though it did
  not. The proxy prefix now derives from `route.pathPrefix` when unset,
  so the two cannot drift, and three guards hold the chain. No prefix
  renders exactly what it always did.

## v0.9.11

- **One Gateway can be shared between the hub and its issuer.** Both
  charts rendered their own for `route.host`, so pointing them at one
  hostname produced two Gateways each declaring a listener for it — and
  Envoy Gateway merges every Gateway of a class into one deployment, so
  they collide on that listener rather than coexisting. Exactly one may
  own it now: the hub's `route.gateway.{name,namespace,sectionName}`
  attaches to an existing one instead of rendering a Gateway and a
  Certificate, and the issuer's `route.sharedWith[]` admits routes from
  the namespaces named (its own always included — a selector, unlike
  `from: Same`, does not imply it). Both default to today's shape.
  Whether a Gateway accepts a route from another namespace is decided
  there and not by a ReferenceGrant, which governs `backendRefs` and has
  nothing to say about `parentRefs`.

## v0.9.10

- **The console's sessions sections need the issuer to share the origin,
  not merely to exist.** They were gated on an issuer being configured at
  all. But the session service is called **same-origin**, with the
  browser's issuer cookie and no bearer — that is the whole point of
  putting the console under its issuer's host — so wherever the two are
  still separate hosts the sections rendered and then called the
  console's own origin, which serves no such service. A 404 per section,
  on every deployment that has not done the cutover. Supersedes v0.9.9,
  which carried the bug.

## v0.9.9

- **Docs: one domain, and where session management lives.** Decided
  2026-09-10: the issuer, the directory's console and the shared UI
  live on one hostname. The issuer sits at the **root** — its URL is the
  `iss` claim and discovery lives at the origin root, so it cannot take a
  path — and the console is mounted under **`/console/`**, the gateway
  rewriting the prefix away. On one origin the console's session pages
  call the issuer with the browser's own session cookie, so the
  cross-origin bearer question stops existing. The global sessions
  listing **exists**: operator-only, capped, audited. The issuer's plain
  `/account` page stays as the fallback for an installation with no
  console. `docs/design/access-issuer.md` *One origin*, `docs/design/hub.md`,
  `docs/architecture.md`, the references.
- **The console can be mounted under a path of its host, so it can share
  its issuer's origin.** `route.pathPrefix` on the hub's chart: the
  console's own route matches the prefix and the gateway rewrites it
  away, so the hub's own routes never learn it exists. The **bootstrap
  surface stays at the host root** — `/login` and `/connect` are
  registered OAuth redirect URIs, and a provider returns to the literal
  address on file, so a prefixed one would be a callback nothing points
  at. Two chart guards hold both halves. Empty is exactly today's shape.
- **The global session listing exists, for an operator.** A request
  naming neither an identity nor a client used to be refused outright;
  it now answers for an operator and is refused for everyone else. It is
  the incident case: the one where you do not know *whose* session to
  look for. Paged by a cursor that is the last session seen rather than
  an offset, because an offset is invalidated by every session that opens
  or closes between two calls and this index is precisely the thing that
  changes constantly. Capped, and a session now reports the browser
  session that parented it.
- **Sessions in the console.** *Active sessions* with Revoke on a
  person's page and *Sign out everywhere* on your own; *Open sessions* on
  a client's page; and a new operator-only **Sessions** page listing the
  installation, filterable by person and client. Rows opened from one
  browser group together. They call the issuer's session service
  same-origin with the browser's own session cookie — no bearer, no CORS
  — and render only when the console knows of an issuer. Removal only,
  never a grant.

## v0.9.8

- **A ServiceAccount's subject names its cluster.** `sub` was
  `k8s:<namespace>:<name>`, and the same namespace and name exist on every
  cluster in an estate — so two different machines were one subject, which
  is the collision `sub` exists to prevent. It is now
  `<cluster>:k8s:<namespace>:<name>`, from the issuer's new `cluster`
  value; scope first, like every group name. An installation that names no
  cluster keeps the unqualified form, so nothing changes until it is set.
  A `service_account` matcher may name a `cluster` to narrow to one, and
  naming none matches any — every rule written so far still means what it
  meant.
- **One spelling for a ServiceAccount, and one reader for all three.**
  A recovery sign-in completed as the API server's
  `system:serviceaccount:<ns>:<name>` while a token exchange minted
  `k8s:<ns>:<name>`, so the same machine had two subjects and a
  `service_account` matcher could admit one and not the other. Recovery
  now completes as the issuer's own spelling. Every spelling the estate
  has minted is still **read** — by one function, in `policy` — because a
  reader that knew only its own would refuse a token from a release either
  side of it, and for recovery that is exactly the day it is the only way
  in.

## v0.9.7

- **The issuer holds a session with the browser, so a second console
  costs no login.** It held only a login-round-trip cookie: every console
  bounced the person back through the corporate directory, and *global
  sign-out* had almost nothing to end. Now an authorization request
  completes against that session — silently — and `auth_time` comes from
  where the person actually authenticated rather than from the moment a
  token was minted. `prompt=login` and `max_age` are honoured, including
  `max_age=0`, which is a request for a fresh authentication and not, as
  a zero duration would otherwise read, no requirement at all. The
  directory still decides: a silent sign-in re-asks the hub, so a
  suspended account stops being admitted instead of coasting on a browser
  session. `end_session` ends the sign-in, not only one application's
  tokens — the half-sign-out that looks exactly like a whole one.
- **An account page, served by the issuer at its own host.** `/account`
  lists what you have open and ends all of it. Being same-origin with the
  session service is the point: the browser already holds this issuer's
  session there, so the page needs no bearer, no CORS and no console, and
  its buttons are form posts rather than JavaScript. *Sign out
  everywhere* ends both halves — the sessions already running and the
  sign-in that would silently open more. Per-client sessions now record
  the browser session that parented them and the time it authenticated.
- **Docs: the SSO session and where session management lives.** The
  design now says plainly that the issuer holds a first-class **SSO
  session** (to build) and that session management is served at
  the **issuer's own host** — an account page, same-origin with the
  session service — rather than through a cross-origin bearer from each
  console. The v0.9.4 `console.origin` CORS path becomes the optional way
  to weave the operator view into the directory console. `docs/design/access-issuer.md`.
- **Docs: `sub` is decided.** A person is their email; a ServiceAccount is
  `<cluster>:k8s:<namespace>:<name>` (the cluster qualifier is the one part
  still to land in code). `docs/reference/policy.md`,
  `docs/design/trust.md`.

## v0.9.6

- **One question to the directory per token, not four.** Every claim a
  token carries comes from one answer, and the code was asking for it up
  to four times over one exchange — the hub call behind it being the
  issuer's hottest. Asking twice is not only two round trips where the
  design counted on one; it is two answers that can disagree, with the
  grants from before a change and the name from after.

## v0.9.5

- **An ID token names who signed in.** It carried no `sub` at all, which
  makes it invalid, and no `email`, `name` or `groups` either. Every
  client asserts userinfo claims in its ID token, so the library
  assembles one from this storage and assigns the result **wholesale** —
  and the hook it assembles from was empty here, deprecated in favour of
  the one the userinfo *endpoint* uses. Supplying nothing did not leave
  the ID token's own claims alone; it overwrote them. A relying party
  that reads the ID token rather than calling userinfo — ArgoCD and Kargo
  both do — saw nobody.
- **A token says which session it belongs to, and when the person signed
  in.** `sid` is the session id the console lists and revokes, so a
  relying party can say WHICH of a person's sessions it holds rather than
  only that it holds one; a token no session backs, such as a workload's,
  carries none rather than an empty one. `auth_time` is the sign-in, not
  the minting: a refresh an hour later carries the same `auth_time` and a
  fresh `iat`, and that difference is the whole of what a
  "re-authenticate for this action" rule reads. Both are identity;
  `groups` still decides.
- **A session remembers the scopes it was granted.** It did not, and a
  refresh arrives carrying a token and nothing else — so the answer to
  "what may this session ask for" was *nothing*. Two consequences, both
  live: a refresh naming any scope at all was refused as though it had
  asked for more than it held, and one naming none minted an ID token
  the library assembled from an empty scope set. A session recorded
  before this release has no scopes and behaves exactly as they all did.

## v0.9.4

- **The issuer answers what sessions it is holding, and ends them.**
  `SessionService` (`ListSessions`, `RevokeSessions`) over the shared
  index, served only when `console.origin` names the one browser origin
  allowed to call it. It can only ever **remove**: no call here grants
  anything, which is what makes it safe to point a console at. Your own
  sessions are yours to list and end; somebody else's need an operator;
  listing a client names everybody on it, so that is an operator's too.
  A request that narrows to neither an identity nor a client is refused —
  "everything" names every person signed in. A session id alone is never
  enough to end somebody else's, and a mismatch answers exactly as an
  absent session does, so an id cannot be probed.
- **A token names the person, not only the address.** `ResolveUser`
  carries the account's given and family names (additive fields 7 and 8),
  the issuer puts them in `userinfo` and the ID token as `name`,
  `given_name`, `family_name` and `preferred_username`, and a relying
  party's UI shows somebody rather than an address — which is what ArgoCD
  and Kargo render after a cutover. Identity, never authorization:
  `groups` decides, as before, and every one of these is absent for a
  workload or a recovery sign-in, which have no names to give. A **held**
  answer carries the last known grants and no names: the hold window
  exists for authorization, and a name recovered from memory would be a
  claim the issuer cannot currently vouch for.

## v0.9.3

- **Every grant is named `<scope>:<thing>:<role>`.** The hub's own two
  roles are `all:access-roster:operator` and `…:viewer`; a role over one
  directory is `<workspace id>:access-roster:<role>`, the scope in the
  first position like every other name, where it used to be an `@`
  suffix. `policy.ScopedGroup` and `SplitScopedGroup` follow, and the
  loader **warns** at start on a name that is neither a grant nor one of
  the two families that deliberately are not grants (`rung:<name>`,
  `emp:<slug>`) — a convention is worth saying out loud where an operator
  sees it, and worth not refusing, since an installation mid-rename holds
  both shapes at once. The demonstration policy is written in the shape
  it documents. **This must be pinned in the same window as the
  installation's own policy rename**, or the console stops recognising
  its operators.
- **Docs: the naming rule.** Every grant is `<scope>:<thing>:<role>` —
  `prod:k8s:admin`, `prod:shop:deployer`, `all:access-roster:operator`
  — with `rung:` and `emp:` the only two-segment families and neither a
  grant. The reasoning is in `docs/design/trust.md`.

## v0.9.2

- **A GitHub Actions workflow can prove what it is.** `verify.GitHub`
  turns a workflow identity token into a proof carrying repository, owner,
  ref, workflow and environment — the five things a `github:` matcher pins
  a job to — so CI can trade its token for one of this issuer's. Two
  settings are the trust boundary, not tuning: `github.owners` (anybody
  may run a workflow in their own repository and get a valid token, so the
  owner allow-list is the whole of what makes one of them ours — empty
  verifies nothing) and the audience, which is this issuer's URL and is
  not configurable, so a token minted for a cloud provider cannot be
  replayed here. A token from another issuer comes back *unrecognised* so
  the next verifier may try it; one this verifier owns and refuses is
  final.
- **The session index is shared, not per-process.** It answered from
  whatever one replica happened to record: a listing was arbitrary rather
  than wrong, and a revocation reported success while the session went on
  working at the pod next door — the worst failure available to a control
  whose whole job is to end access. It now lives in the same store as the
  logins in progress. Sets make it findable, the record's TTL is the whole
  of expiry, and a listing repairs the sets it walks. A refresh token is
  hashed into its key rather than written into the keyspace: an index that
  can be read must not be an index that can be replayed.
- **The rule under everything is written down.** `docs/design/trust.md`:
  a service trusts exactly two anchors — the cluster for a workload next
  door, the issuer for everything further away — chosen by scope, never
  a third; `groups` is the one vocabulary; recovery is the cluster anchor
  used as the floor; a service with a console and an API has two
  listeners. `docs/connect/service-to-service.md` is the how-to. Every
  connect guide names its anchor; the policy examples use the built
  syntax (`clients:` with `signed_out`, `github.owners`) instead of the
  design-era one.

## v0.9.0

- **Sign out ends the sign-in, not just the cookie.** Clearing the
  proxy's session cookie ends the session with one application; the
  issuer still holds the person's sign-in, so the next click — that
  console or any other behind the same issuer — admits them again with no
  password. The screen said signed out and they were not, which is the
  one failure a person cannot see. `access.signOutThroughIssuer` now
  renders both halves (proxy sign-out → the issuer's RP-initiated logout
  → back to the console's front page), and refuses to render half a chain.
  A client's landing pages are a new `signed_out` list in the policy,
  separate from `redirects`: a redirect URI *starts* a sign-in, so landing
  there after signing out begins the login just ended — and listing one
  address as both now fails the load.

## v0.8.6

- **A policy change is a rollout, not a reload.** The declared layer is
  read once at start, and the hub's Deployment carried no checksum of it:
  the ConfigMap changed, kubelet wrote the file a minute later, and every
  replica went on answering from the policy it booted with — a grant
  visible in git, in the ConfigMap and in ArgoCD's *Synced*, and nowhere
  in the running service. Seen live rolling out the derived policy.
  The issuer has carried `checksum/policy` since its first
  release; the hub reads the same file the same way and now does too, and
  `just chart-lint` fails either chart that loses it.
- The console's account block is three lines — name, address, roles —
  with the roles held over a single directory named beside the
  installation-wide one.

## v0.8.5

- **A person's page reads down the column.** The 300px rail was carrying
  the two longest things on the page — every directory group the person
  is in, and the JSON a token would carry — so on a real directory they
  had to be read sideways while the main column ended halfway down. Both
  now sit in the main column, in the order the page already reads; the
  rail keeps the seven short facts, which is what a rail is for.

## v0.8.4

- **A probe retries what it could not ask, and not what was refused.**
  One transient `503` from Google's `domains.list` — seen live, during a
  rollout — flipped a directory whose credential is fine to *failing*,
  and with 0.8.0's reasons attached it told the operator its domains were
  *provisional — probe failed*. A directory that answers 503 has said
  nothing about the credential; one that answers 403 has. Backends now
  mark the first kind `ErrUnavailable`, the hub retries only that, and a
  revoked credential still surfaces on the first attempt.

## v0.8.3

- **React 19**, and the console bundle rebuilt on it. The major itself was
  a deliberate, human-merged update; what a bot cannot do is rebuild what
  `package.json` produces, so the repository declared 19 and carried a
  bundle built against 18. `frontend/dist` is embedded in the binary and
  `ts/dist` is what a git-tag install gets, so a stale one ships a console
  nobody's manifest describes. CI now runs `console` and both bundle
  recipes fail when a fresh build differs from what is committed.
- An empty **Directories** page offers the action it names, instead of
  saying "Add one to start serving its domains" with the only control in
  the page header.
- The connect runbook says what an unpublished consent screen actually
  does: in Testing it admits only listed test users, so the first tenant
  connects and the next company's administrator is refused before the
  request reaches the hub — with the seven-day refresh-token expiry as
  the half that bites later.

## v0.8.2

- **Sign-out ends the session that actually signed you in.** Behind a
  proxy the console's sign-out cleared this hub's own cookie — which
  nothing was using, because the proxy holds the session and forwards a
  bearer. So sign-out did nothing, and it landed on a sign-in page with
  no way in, this hub's own sign-in being off by design. `access.signOutURL`
  names the proxy's own sign-out, the console navigates to it rather than
  POSTing (a redirect a fetch would swallow), and the login page now says
  where the door is instead of showing an empty card.
- **A page waiting for a first snapshot fills in when it lands.** The
  first snapshot runs detached, so a directory's page opens on a
  workspace with nothing in it and used to stay that way — a photograph
  of the first two hundred milliseconds, while the read it was waiting
  for finished five seconds later behind it. The directory and Overview
  pages now say the first snapshot is running and refresh themselves
  until it is not.

## v0.8.1

- **A probe cancelled by the hub's own shutdown is no longer written down
  as a probe that failed.** Seen on the 0.8.0 rollout: the pod stopped
  mid-probe, the token request returned `context canceled`, and that
  became the workspace's health — so a directory whose credential is
  fine showed as failing, and its domains as *provisional — probe
  failed*, until the next pass. A probe that did not happen is not a
  probe that failed.

## v0.8.0

Everything the first live connect exposed, and the two choices it showed
the console was making for the operator.

- **A request never waits on the directory.** The first snapshot ran
  inside the consent callback and met the gateway's fifteen-second route
  timeout: a 502 for a workspace that had already been stored. A console
  listing with no snapshot read the directory under the request's own
  context. Narrowing refreshed before returning. All three now run
  detached; a read that did not ask for freshness never fetches; and
  narrowing excludes from what is already in memory, so it does not
  depend on a read succeeding.
- **The store is the truth and the reader map is a cache of it.** With
  two replicas, a workspace connected on one was "not found" on the other
  until it restarted. A miss now opens the workspace from the credential
  stored beside the record.
- **Group members are read with bounded concurrency** — eight in flight,
  atomic, in the directory's order — and every pass logs how long it took.
- **`hold` is now `provisional`, and says why**: first snapshot, stale,
  probe failed, contested. The Overview also counted *unserved* domains
  as held, which is how one record read "0/7 served, 7 on hold" on one
  page and "six not served, one hold" on another.
- **A connect asks which domains to serve**, with the consenting
  administrator's own domain pre-selected, instead of quietly serving all
  seven a tenant happened to own. "All of them, including ones added
  later" is still the empty list — now chosen rather than defaulted into.
  And a **reconnect keeps the answer**: it brings a new credential, not a
  new configuration.
- **An operator chooses which groups to sync.** `Workspace.SyncGroups`
  existed and was wired to nothing — not the refresh, not either store,
  not the contract, not the console, and `clone` did not even copy it.
- **The setup panel names the redirect URIs where each flow lands.** With
  one shared OAuth client the sign-in returns to the *issuer*, not here,
  so an operator was registering a URI nothing returns to. And the client
  is checked before an administrator is sent to spend a real consent on
  one the provider will refuse.
- **A role may be held over one workspace** (`hub-operators@C0example`),
  so connecting a second company's directory does not hand its
  administrator the first one. Recovery stays installation-wide by
  construction.

## v0.7.2

- **A consent that fails says so on a page.** The callback answered 502
  and the CDN in front of the console replaced it with its own "Bad
  gateway" — six kilobytes of Cloudflare HTML in place of the line naming
  the exact Google project and the exact API to enable. The diagnosis
  survived only in the log, which is the one place the person who could
  act on it was not looking. Every browser-facing outcome of the consent
  callback now renders a page in the console's own style, carrying what
  the directory said verbatim and the usual causes in order of
  likelihood, and none of them answers 5xx. A test asserts that.

## v0.7.1

- **The customer id is read from the admin's own user record.** `Tenant`
  called `Customers.Get`, which needs a *fifth* scope,
  `admin.directory.customer.readonly`, that is not among the four the hub
  asks for. Google granted the consent and the first read then failed with
  `Request had insufficient authentication scopes` — naming no scope, and
  arriving as a 502 on the callback. A `User` carries `customerId` and is
  covered by the user scope already granted, so the id is free and no
  administrator has to consent again.

## v0.7.0

- **The consent callback takes its operator from the signed state.** It is
  a redirect from Google and it lands on the bootstrap route — the one
  that exists precisely so a callback is not swallowed by a login prompt,
  which means the gateway adds no identity to it. Insisting on an identity
  in that request refused the one flow the route exists to finish: a live
  403, `this needs the operator role`, on the first workspace anyone tried
  to connect. The authorisation still happens where it always did, when an
  operator asks for the consent; the state now carries the answer, signed
  by the hub and pinned to the browser by the cookie the callback already
  checked. `docs/reference/configuration.md` had described this shape all
  along.
- `Identity.Who()` — the address where there is one, the subject where
  there is not. A recovery sign-in completes as a ServiceAccount and has
  no address, so `ConnectedBy` recorded a blank for exactly the sign-in
  whose actions most need a name against them.
- Dependencies: typescript 7, vite 8 with @vitejs/plugin-react 6, MUI 9.4,
  vitest 5. React stays on 18.

## v0.6.4

- **access-proxy writes `weight` out on every `backendRefs` entry.** The
  API server defaults it, ArgoCD normalises core-API defaults but not
  CRDs, and the child Application was therefore permanently OutOfSync —
  the third field in this family after the route's `matches` and
  `certificateRefs.group`, and the only chart of the three that had not
  learnt it. The lint now counts one `weight` per backend.

## v0.6.3

- **The gateway now sends the proxy the session cookie.** An HTTP
  ext_authz service is sent only `Host`, `Method`, `Path`,
  `Content-Length` and `Authorization` unless the `SecurityPolicy` says
  otherwise, so the proxy answered every check without ever seeing the
  cookie it had just written: sign-in completed, the callback returned
  its 302, and the next request began a fresh login — forever. The chart
  names `cookie` itself and will not let a value take it away; the lint
  asserts every rendered policy carries it.

## v0.6.2

- A recovered sign-in has no email address. It completes as a
  ServiceAccount **subject** and is carried as one end to end, instead of
  failing where an address was assumed.

## v0.6.1

- The issuer reads a confidential client's secret. It was constructed
  with no resolver at all, so every confidential client got
  `invalid_client`.

## v0.6.0

- **Recovery sign-in**, so a first installation can be bootstrapped:
  a ServiceAccount token checked by the API server against a mandatory
  audience. It stores no credential and grants nothing by itself — the
  policy's `service_account` matchers decide what it is in.

## v0.5.0

- A bootstrap surface on the hub the gateway does not cover, so the
  console that connects the first directory is reachable before any
  directory exists.

## v0.4.3

- `certificateRefs.group` written out — the last Gateway API field the
  API server defaulted and ArgoCD would not normalise, which left the
  child Application permanently OutOfSync and gated every later wave.

## v0.4.2

- The HTTPRoute path match written out, for the same reason.

## v0.4.1

- Several protected routes on one host, each with its own posture — the
  shape a surface needs where a demo path is open to any employee and the
  application behind it is not.

## v0.4.0

- **The access-proxy chart is published.** oauth2-proxy, a Valkey session
  store and the Gateway API resources that put them in front of one
  console, for applications that cannot run the code flow themselves.

## v0.3.0

- The hub verifies the gateway's forwarded token against the issuer's
  keys instead of trusting a header.

## v0.2.0

- **The TypeScript package exists.** `@truvity/access-roster`, installed
  from git at a tag with `ts/dist` committed so it needs no toolchain and
  no registry: `fetchIdentity`, `useIdentity()` and `<UserBadge>`, with
  react and MUI as optional peers. It parses no token — the browser asks
  the application it is already talking to. Its reference page described
  three states and the endpoint's real shape had different fields; both
  now match what is served, and there is a **fourth state**, `unknown`,
  for when the question could not be asked. A console that showed a
  sign-in button because one request failed would send a signed-in person
  to authenticate again for nothing.
- **Revocation always reaches the shared state.** It took a hit in the
  per-process session index as proof that revocation was done and
  returned — so a token revoked on the replica that happened to hold the
  session stayed valid at every replica, including that one. Both now
  happen, always, in that order.

## v0.1.0

The first release: enough to stand the hub up on a cluster, connect the
companies it serves, and let people in. The issuer is here and runnable
but has not carried a relying party yet.

**Not in it**, though the reference documents describe them: the
TypeScript package (`docs/reference/typescript.md`) and most of the Go
module for consoles (`docs/reference/go-module.md` — `identity`,
`authz`, `directory`, `tokens`). What a `go get` at this tag gets is the
policy engine and the backend interface. Both libraries belong to the
work that replaces gateway-auth, and nothing in the hub's rollout needs
them.

This section describes what is in the release, not the order it arrived
in.

- **directory-roster**, the directory hub: workspaces whose domains are
  discovered, snapshots with the freshness policy (`max_age`, the
  cheapest path, an in-domain miss that always checks live once), routing
  by email domain with conflict detection, and the authority rule that
  makes everything degrade to a hold rather than to "gone".
- **Served domains**: a workspace may be narrowed to a subset of the
  domains its tenant owns — `workspaces[].serve` in the values, or
  *Choose which to serve* on a connected directory's page. What is left
  out is discovered and shown but routes nothing and is not cached, only
  two workspaces that both serve a domain contest it, and the list is
  intersected with discovery so a domain moving between tenants hands
  over without an edit.
- **DirectoryService** over ConnectRPC for consumers, authenticated by
  Kubernetes ServiceAccount tokens; the operator services, the login
  routes, the consent callback and `/.access/whoami` on a second
  listener.
- **Connecting a real Google Workspace**, both ways in: admin consent
  (offline access, a forced consent screen so a reconnect really returns a
  refresh token, and the consenting account read from the id token) and an
  uploaded service-account key. Disconnecting hands the refresh token back
  to Google.
- **Nothing is lost on restart.** What a console changed — connected
  workspaces, their credentials, the memberships, the OAuth client, the
  session key and the break-glass password — is kept as plain ConfigMaps
  and Secrets in the hub's own namespace, written and read by the hub
  itself. `STORE=memory` keeps nothing and says so at WARN; the chart
  always sets `kubernetes`. At start, a declaration removed from the
  values takes its record with it, and a credential that cannot be read
  leaves one workspace unhealthy rather than stopping the hub.
- **Snapshots shared across replicas**, in Valkey: one copy of each
  directory, gzipped, with the reverse index rebuilt on read rather than
  stored. The refresh lease is held for the whole interval rather than for
  the work, so replicas that tick at different moments still read a
  directory once per interval — a quota is per tenant, not per reader —
  while a failed pass hands its lease straight back. No address configured
  keeps snapshots in memory, which the hub says at start.
- **Nothing untrusted reaches a log line unsanitised.** Addresses,
  request paths and the errors built from them now pass through
  `internal/logsafe`, which removes what a reader or a parser would take
  for the end of a record. Structured handlers escaped these already —
  none of it was forgeable in practice — but it is now true by
  construction rather than by the handler's choice, and named where it can
  be seen. The address stays in the line: an audit record that does not
  say who was refused is not one.
- **A login in progress is shared across replicas.** The authorization
  request, the code, the tokens and the device flow were four maps in one
  process — so a browser that started at `/authorize` on one replica and
  came back from the provider at another found nothing, and a terminal
  polling the device endpoint reached whichever pod answered. All four now
  live in Valkey when one is configured, each carrying its own expiry so
  nothing sweeps, with the user code claimed by a single atomic write
  because two replicas minting the same short code must not both believe
  they own it. Memory remains the default and says so at start.
- **The release publishes both services.** `.goreleaser.yaml` was missing
  entirely — the workflow would have failed at its GoReleaser step on the
  first tag ever cut. It now builds the hub, the issuer and the acceptance
  binary for linux and darwin on both architectures, publishes two images
  under `ghcr.io/truvity/access-roster/`, and stamps the git tag into each
  binary's version. `just release-check` validates it without cutting one.
- **access-issuer has a chart**, so the release has both to publish: the
  Deployment, the TokenReview permission the exchange needs, the policy
  ConfigMap, the route, and the cert-manager `Certificate` that produces
  the signing key with `rotationPolicy: Always` — a renewal has to be a
  new key, because a renewed certificate over the same key rotates
  nothing. `issuerURL` and `hub.address` fail the *render* when unset,
  rather than the pod.
- **People can sign in to the issuer.** `/login` is the chooser the
  library sends a browser to, `/login/<provider>/start` and `/callback`
  are the round trip, and `/signed-out` is where a logout lands. One
  button per provider kind, never one per company; with one provider it
  redirects rather than asking a question with one answer. The address is
  all that is taken from the provider — the hub decides whether it is
  anybody here, and refuses on that page, naming the address, because
  everywhere downstream the person would just be admitted nowhere. The
  half-finished request travels in signed state, so a callback cannot
  finish somebody else's login, and that state's key is derived from the
  signing key rather than being a second Secret to provision.
- **The issuer's signing key is provisioned, not minted.** It reads a PEM
  a Secret carries — cert-manager issuing one, external-secrets delivering
  one — mounted as a file, and holds no permission to read Secrets at all.
  A service that creates its own credential is an exception to how every
  other credential here is provisioned. The key id is now the key's own
  RFC 7638 thumbprint rather than a name travelling beside it, which is
  what lets the key arrive from anywhere and makes rotation a matter of a
  new key having a new id.
- **access-issuer is a service.** `cmd/access-issuer` and
  `internal/issuerapp` assemble it from the environment — policy, the door
  to the hub, the signing key, the verifiers — and serve discovery, the
  JWKS, the code flow, exchange, revocation and the device flow, with
  health beside them. It refuses to start without an issuer URL, because
  that string is baked into every token and every relying party's trust
  and a default would be a value nobody chose spread across an estate.
- **Discovery stopped advertising the implicit grant.** `response_types`
  was already corrected; `grant_types_supported` was not, and a relying
  party reads that one and picks — offered implicit, a library uses it,
  and the refusal arrives in a browser redirect where nobody sees why.
- **The chart has been installed and run**, in kind, with the real
  Kubernetes store and token recovery: two replicas, recovery by a minted
  token straight into the console, the API listener admitting the declared
  consumer and refusing every other identity, and no standing credential
  anywhere in the namespace. The first attempt would not start at all —
  the rendered policy carried no `version`, so the loader refused it. The
  version belongs to the chart now, not to the values.
- **An acceptance suite against a real API server** (`just acceptance`, a
  throwaway kind cluster). It covers the three things a fake clientset is
  silent about and the hub leans on: name validation, a create that raced
  another — now handled rather than failed — and TokenReview, which is
  what recovery and the API listener's guard are made of. Running it
  showed that the audience is enforced by *asking* for it: a token minted
  for another audience comes back not authenticated at all.
- **The wiring is testable.** Everything `main()` decided moved to
  `internal/app`, with an acceptance suite that boots a whole hub from the
  environment and walks the use cases over the real handlers, and a test
  that compares every variable the binary reads against every one the
  chart sets — reading both from the source, because the five settings
  that were read and never set were exactly the kind of thing a restated
  list gets wrong.
- **The hub's own sign-in is a switch** (`access.login.directory`), and
  turning it off closes the routes rather than hiding the buttons —
  connecting a directory is unaffected, because an operator granting this
  hub access is not a way in. The external-OIDC login the values gestured
  at is **removed** rather than built: an installation that has an issuer
  has access-proxy in front of it, and the proxy's forwarded identity is
  that path.
- **Four chart values that did nothing now do something.** `PUBLIC_URL`
  was never set, so a deployed hub built both OAuth redirect URIs — and
  the values its setup steps tell an operator to paste — from
  `http://localhost:8081`; it now comes from `route.host`, and the session
  cookie is marked Secure with it. The forwarded-identity path a console
  behind the fleet's gateway has been documented as using was never
  rendered either; it now is, with the header name as a value. Session
  lifetime and log level joined them.
- **The API listener authenticates its callers.** It was open: anything
  that could reach the port got every account and group of every company
  the hub serves. Callers now present a projected ServiceAccount token
  with the hub's audience, verified by TokenReview against the declared
  `consumers`. A deployment declaring none admits nobody; outside a
  cluster there is nothing to verify against, so it stays open and the
  process says so at start.
- **Signing in with a directory works.** `GET /login/<backend>/start` →
  `/callback` asks the provider for `openid email profile` and nothing
  else: the address is all that is taken from it, and whether the account
  is live, which company it belongs to and what it may do are answered by
  the directory this hub already reads. An address in no served domain is
  refused at the door rather than given a session with no role, while a
  person of a served company who is in no group signs in fine — their own
  page explains what they have. One button per directory *kind* on the
  sign-in page, never one per company, which would publish the tenant list
  to anyone who loads it. The OAuth client now needs **two** redirect
  URIs, and the setup step shows both.
- **Recovery replaces the break-glass account.** In a cluster the hub
  stores no credential at all: recovery is a ServiceAccount token minted
  for one audience and a few minutes, checked with a TokenReview, so the
  authority is the cluster's own RBAC — revocable by removing a binding,
  recorded in the cluster's audit log, and naming who recovered rather
  than "admin". Whoever could read a stored break-glass Secret already had
  cluster access, so the secret was only ever converting that access into
  a session; this does it directly. Outside a cluster a password is
  generated and printed once, kept as an Argon2id digest with a random
  salt, serialised, and silent for a minute after ten failures — the
  correct one included, so the limit says nothing about which guess was
  close. Only that shape is a standing credential, so only it gets a
  warning and a "turn it off" setup step. `POST /admin/login` becomes
  `POST /login/recovery`.
- The forwarded identity header is now required to be an address before it
  is taken as a principal; the consent cookie is cleared with the same
  attributes it was set with.
- **The policy** (`docs/reference/policy.md`): five tables — groups,
  claims, lifetimes, clients, memberships — one schema for both services,
  deep merge with a load-time scalar-conflict check, shortest lifetime,
  layered loading, memberships the only console-writable table, clients
  declared or self-registered and never created in a console. The hub is
  a relying party of it: `hub-operators` and `hub-viewers`.
- **The issuer design, completed on sessions and standards**: sessions
  are first-class issuer state, listable per identity and client and
  revocable, which gives the console its second write — Revoke, and
  "sign out everywhere" for oneself — and the operator a lever between
  the proxy's sign-out and the next refused refresh. The standards table
  names what is in (Core code+PKCE, Discovery, RP-Initiated Logout as the
  conformance target; device, token exchange, JWT profile, client
  credentials, revocation, dynamic registration, JWT access tokens) and
  what is deliberately out (introspection, implicit and hybrid, back-
  channel logout for 1.0, the session iframe, PAR, DPoP, mTLS, CIBA), on
  `zitadel/oidc/v3`, with the OpenID conformance suite as the spike's
  seventh item.
- **The console** on the fleet stack, organised as the graph the content
  actually is rather than as a set of tables: search on every page, an
  overview that answers whether anything is broken, and a page per
  tenant, group, client and person, each carrying its edges in both
  directions. Every name is a link, every page opens with a
  plain-language summary, and actions live on the object they change.
  The navigation is two clusters, identity and access, with one adjective
  each — directory groups and internal groups — and a page at every level
  of both: a directory group's page mirrors an internal group's, and the
  membership that joins them is editable from either end. A person's page
  is the chain, one row per internal group held. Matchers lists every
  declared rule — the identity side's second way in, by shape rather than
  by membership — and keeps the proof simulator for a CI job or a
  workload, because a run exists only while it runs. A client's page
  lists the machines that reach it beside the people. People filters by
  directory and by whether an account is live. **Add a directory** offers both ways in and only
  the ways the deployment can take. The visual layer follows Material's
  guidance for a console: a navigation rail with the two sides as groups
  and a header kept for search and identity, a denser lowercase theme,
  and one meaning per form — names are links, chips are states, facts are
  a label over a value, and two-column data is a list. The account sits at
  the foot of the rail with your name linking to your own page, which
  also says how you signed in; detail pages split into a main column and
  an aside on wide windows, so a person's chain fits a screen. The
  contract was tightened for it: `Explain` names the directory that
  served the address, `SearchPeople` reports the total before the limit,
  and `GetPolicy` carries matchers only in structured form.
- **The Google backend**, which is the first real directory the hub can
  read: the Admin SDK over a service-account key with domain-wide
  delegation, discovering the customer id and every verified domain,
  listing accounts and groups with their flat membership, and answering
  one address at a time. Archived counts as not live beside suspended.
  Unverified domains are not served, because a domain anyone may claim in
  a console is not evidence of anything. The credential type is pinned to
  a service-account key, since a credentials file may also name an
  external account that fetches its token from a URL the file itself
  carries. The console's setup guidance now reads its scope list from the
  backend rather than repeating it.
- **Declared workspaces are read at start**, which the chart had shipped
  the configuration for and no code had read. The tenant id became
  optional — the credential opens one tenant and it knows its own id — and
  supplying it turns adoption into a check that refuses a credential
  opening a different tenant. A declared workspace that cannot be adopted
  stops the process rather than leaving a hub that silently serves less
  than it was configured to. The chart renders the Gateway, HTTPRoute and
  Certificate for the console host it had always claimed to.
- **Day one leads itself.** Overview carries what a fresh installation
  still has to do, with that installation's own redirect URI and scopes to
  copy rather than a document's placeholders, and each step disappears as
  it completes. The break-glass account's default is computed rather than
  fixed: it stays off when the values already declare a workspace and a
  non-empty `hub-operators`, because such a deployment signs in through
  the directory from its first boot and a password nobody needs is a
  standing credential. `GetSettings` reports the setup values.
- **A demonstration mode** (`DEMO=1`): two tenants in memory and a
  consent connector, so every use-case is walkable before a credential
  exists.
- **The documentation set**: why it exists, the concepts, the fifteen
  integration points, the architecture, a design per battery, the
  reference pages, one connect guide per kind of relying party, the
  operations runbooks and the extension points.

Designed but not built: access-issuer, access-proxy, the libraries as a
public module, accessctl and the GitHub Action.
