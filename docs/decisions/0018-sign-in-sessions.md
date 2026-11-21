# 0018 — Sign-in sessions have two classes with limits per class, and session chains are temp kinds

**Status:** Accepted; partly superseded by 0051
**Date:** 2026-11-21

Proposal: [issue #998](https://github.com/truvity/sluis/issues/998).

## Context

`signin@<boundary>` keeps the sign-in sessions, the session cookie and each person's freshness per boundary ([0002](/decisions/0002-families-of-actors/)). A refresh token ends at sign-out, at an idle limit and at an absolute limit (REQ-SESS-015). This record is about the sessions of sluis's own sign-in. Browser sessions for websites behind a proxy or a gateway are a different thing: they are the product `session`, described in [its first record](https://github.com/truvity/session/blob/master/docs/decisions/0001-purpose-scope-and-name.md).

Three kinds of caller hold tokens, and they differ in what a limit can measure and in who may end them:

- **A person at a browser, the console or the CLI.** The person is present now and then, signs out, and can be asked to authenticate again. v1 ended every chain at an absolute limit of 24 hours from `auth_time`, never reset by use, with an idle limit of 12 hours on the refresh token (v1 ADR 0001 on `master`, and v1's `lifetimes.refresh`).
- **An agent acting on a person's behalf,** such as an MCP client, after the person consented. It runs in the background, often for weeks, with no person to answer a prompt. Its client is identified by a Client ID Metadata Document URL ([#792](https://github.com/truvity/sluis/issues/792)) and its tokens are bound to a resource with RFC 8707 ([#822](https://github.com/truvity/sluis/issues/822)). v1 gave such clients their own class: 14 days idle, 30 days from `auth_time` with a ceiling of 90 days, and access tokens of at most 30 minutes (v1 ADR 0040), and a person's browser sign-out did not end them.
- **A machine or workload** with a workload proof ([0022](/decisions/0022-workload-identity-and-host-enrolment/)). Nobody is present; there is nothing to sign out of.

Things are kept apart here by the test that decides whether two things may share a key: equal audience, lifetime and revocation. A boundary's session is seeded from the front's sign-in through token exchange ([0017](/decisions/0017-identity-and-sign-in/)), so a limit at one boundary is worthless if another can extend it. Agent sessions are planned; this record fixes the classes now so that they join without retrofitting the session model.

A session is state that may be lost: a lost chain costs a sign-in and never data. That makes it a temp kind, with a time to live, and it fixes where the limits live.

## Decision

### The classes

- **Every session has a class, fixed when it is created and never changed:** `human` or `agent`. Policy chooses the class from the client's declaration; a client never declares its own class, and a fetched client document cannot ([0021](/decisions/0021-clients/)). A policy change can shorten a session, never lengthen it or change its class. Only the `human` class is built.
- **A machine or workload client has no session.** A workload exchange gets short-lived tokens and no refresh token, no session chain and no `sid`. A caller that needs a fresh token presents its proof again.

### Session chains

- **A session is a `SessionChain`,** a temp kind that `signin` writes ([0004](/decisions/0004-the-data-model/)). It holds the hash of the current refresh token and its generation, the class, the client, the `sid`, the start and the last refresh, the freshness of the person's authentication for the boundary, the resource of the chain ([0021](/decisions/0021-clients/)) and, for an agent, the consent. Its treatment is hashed: a refresh token is compared, never read back. The chain also holds a random salt, sealed with the facts under the tenant's `chain` key.
- **A token of any older generation catches reuse, and the chain keeps no list of them** ([REQ-SESS-014](/requirements/sess/#REQ-SESS-014)). Every refresh rotates the token to the next generation, and presenting a spent one, of any generation below the chain's, ends the chain at once (REQ-SESS-001). The secret half of a refresh token is 32 bytes: the generation (8 bytes, big endian) and a tag of 24 bytes. The tag is HMAC-SHA384 at key@ under the boundary's `mac` key, label `sluis-refresh-tag-v1`, over the tenant, the `sid`, the generation and the chain's salt, cut to 24 bytes. The salt is 256 random bits made with the chain, so a copy of the store holds neither a token nor what makes one, and only `signin` may ask key@ for the label (the labels that begin `sluis-refresh-` are reserved, [0006](/decisions/0006-keys-and-sealing/)). The chain holds the generation and the SHA-384 of the current token and nothing about the tokens it replaced, so it does not grow with the rotations. A presented token is told apart so:
  - its hash is the chain's: it is the live one, and no call to key@ is needed besides the successor's;
  - its generation is at or above the chain's and it is not the live one: it is a guess, refused as unknown, and nothing ends;
  - its generation is below the chain's and the tag key@ makes for that generation is the token's: it is a reuse, however many rotations ago;
  - any other tag is a guess.

  A guess ends nothing, so a stranger who knows a `sid` (it is in every token, so no proof) and a generation cannot end a session without a tag, which needs key@ and the salt. The `mac` key has no new generation in place, so the token carries no key version; replacing the key is a migration that ends the live sessions.
- **A spent token is answered again for 30 seconds, with the same successor.** A client that lost the response, or a second replica that read the chain before the first wrote it, presents the token the last rotation replaced: within 30 seconds of that rotation it gets the successor it would have had, and the chain is not written again (REQ-SESS-013). The successor is not stored and not random: it is the token of the next generation, which key@ makes from the tenant, the `sid`, the generation and the salt, so every replica derives the same one and the holder of an old token cannot compute the current one. Any other spent token, and that one after 30 seconds, ends the chain; the window is for the generation just below the chain's and for no other. Two requests with one token at once are decided by the conditional write of the rotation: one writes, the other finds the chain holding the successor of its own token and is answered as a retry. A rotation stamped more than 5 seconds ahead of the reader's clock is not a rotation of the window, so a replica with a fast clock cannot hold it open: the spent token is then a reuse. The end of a chain is recorded once, by the request whose delete succeeded, best effort and counted ([audit 0005](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md)). The record of a refresh that was answered as a replay carries `replay: true`, so a log rule can look for it. Tokens already issued are not recalled; they live out their lifetime (REQ-SESS-055).
- **Whether a person is held is asked of a listing, and the number of holds a tenant has is assumed small.** A sign-in, a code and a refresh look for the person's `PersonHold` among the tenant's holds (linearizable, a page at a time, every page), because a `Get` of the usual absent item is a refusal that store@ audits every time. A hold is written by an operator for one person, so a tenant has tens of them. The lookup reads a bounded number of pages and then fails closed: sign-ins and refreshes are refused until the holds are fewer. A tenant that expects more than a few thousand needs a declared index of `PersonHold`, keyed by a keyed hash of the subject, and a lookup by that index instead of a scan; the kind has no index.
- **The time to live of a chain is its absolute limit.** A human chain lasts 24 hours absolute. An agent chain lasts 14 days idle and 30 days absolute, and 90 days at most, which is the built-in maximum. An expired chain reads as absent ([0005](/decisions/0005-the-store/)).
- **A chain is lost with the temp class and is not in a backup.** After a restore, everyone signs in again ([0010](/decisions/0010-backup-and-recovery/)).
- **The person lists and revokes chains.** The listing uses the declared indexes by person and by client, and the console routes a revoke to `signin` ([0027](/decisions/0027-the-console/)). The console is planned.
- **A bearer endpoint serves a token only while its chain lives** (REQ-SEC-038, REQ-TOKEN-024). `signin` checks, before an endpoint reads anything (UserInfo is the first): the header's `typ` is `at+jwt` (an ID token is not a credential), the signature verifies against the key set that `signer` serves without revoked keys, `iss` and `exp` hold, the audience is the client the token was issued to and a declared client, and the chain the `sid` names is there, belongs to that client and is inside its absolute and idle limits. Every refusal is the same 401 `invalid_token`; the audit record names the check. The chain is read from the store on each request, linearizably, so a session that ended before the request began is seen at once and a live chain is never remembered. The only thing remembered is that a chain id is gone (an id is made once, so it cannot go stale). A revoked signing key is out of the key set, and `signin` keeps that set for ten seconds, so a token it signed is accepted for ten seconds at most after the revocation. A token made, or not yet valid, in the future by more than a minute is refused. This is the one place that recalls: a resource server that checks the signature and `exp` alone still accepts a token until `exp`.
- **The session cookie is sealed and opened at key@ on every request,** under the boundary's `cookie` data key, in a batch with the other opens of the request. `signin` never holds that key ([0006](/decisions/0006-keys-and-sealing/)). The pending sign-in has a cookie of its own, `__Host-sluis_signin`, sealed under the same key and kept apart from the session cookie ([0025](/decisions/0025-integrations/)). It is `Secure`, has `Path=/`, no `Domain`, and is `HttpOnly` and `SameSite=Lax`, so that it returns on the top-level redirect from the upstream. `entry` passes this cookie and no other. The hop to key@ was measured in a latency spike ([#1179](https://github.com/truvity/sluis/issues/1179)) on Lambda on 2026-11-03: a whole warm sign-in took about 0.40 s at p50 and 0.49 s at p99. `AuthorizeRequest` carries no session cookie yet, so every authorization starts a sign-in; carrying it, so that `prompt=none` and a returning browser find their session, is planned.

### Human sessions

- **An absolute limit from the start of the session, never reset by use:** the start is the moment sluis accepted the sign-in that began the session, and the `auth_time` of a token stays the upstream's ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)). Default 24 hours, which is also its maximum: an installation or a tenant may only shorten it. A token's `exp` is capped at the limit, a refresh past it is refused, and a silent authorization past it ends the browser session.
- **An idle limit on each refresh token, under the absolute limit:** default 12 hours, and never above the absolute limit. A per-client session ends at the earlier of the last refresh plus the idle limit and the session's start plus the absolute limit, recomputed at every rotation.
- **Step-up re-authentication** follows [0017](/decisions/0017-identity-and-sign-in/). A step-up refreshes the freshness of the authentication, not the session's start, so it does not extend the absolute limit.
- **The person's sign-out ends the browser session and every human session under it,** at every client, with back-channel logout to the clients that registered for it (planned).
- **The limits bind the browser session and its refresh tokens alike.** The console's session is a human session.
- **A client ends its own session by revoking its refresh token (RFC 7009, REQ-TOKEN-025).** `POST /revoke` ends the chain of the token and nothing else: the person's other sessions, at this client and at others, go on. It ends the chain only when the caller holds a token of it (the `sid` is in every token the chain issued, so it is no proof; the secret half is) and the chain is the calling client's. Every other request is answered 200, so the answer does not tell an unknown token from an ended one or from another client's (REQ-TOKEN-026). An access token or an ID token is answered 400 `unsupported_token_type` by its shape alone, because revoking an access token that was issued is a recall, which is not built. The chain is deleted under a grant that policy gives, as every end of a chain is: a held person cannot sign out until the hold is lifted, and the answer is then 503 (RFC 7009 section 2.2.1), not a 200 that says the session ended. The delete is the step that fails the request; the records of the end are best effort and counted, because answering 503 after the delete would have the retry read "absent", answer 200 and record nothing.
- **UserInfo reads the chain and not the person.** A refresh that policy denies for a held person is refused and does not end the chain, so a held person's access token is served until it expires or its chain ends, and an operator's hold ends no session by itself. Only `sub` is released, so nothing personal leaves. Reading the person's state, and the scope a refresh narrowed to, before any claim beyond `sub` is released is a gate on [REQ-TOKEN-006](/requirements/token/#REQ-TOKEN-006).

### Agent sessions

Agent sessions are planned ([#794](https://github.com/truvity/sluis/issues/794), [#1817](https://github.com/truvity/sluis/issues/1817)). The rules are:

- **An agent session starts only from a consent the person gave interactively.** The consent names the client, the resources and their scopes, and is recorded on the chain; an agent authorization never completes silently.
- **No interactive idle timer.** An agent session has an inactivity expiry on its refresh token (default 14 days) and an absolute lifetime from the consent (default 30 days, maximum 90 days). Its access tokens last at most 30 minutes (maximum 1 hour).
- **It never outlives the person or the consent.** Revoking the consent, the directory disabling the person, an operator's revoke, and refresh-token reuse each end it at once. Every refresh re-checks the consent and the person's state.
- **Refresh tokens rotate** at every use, as for every class (REQ-SESS-001).
- **Tokens are narrow:** one resource per token (RFC 8707), with the scopes the consent names for that resource. They are sender-constrained by DPoP where the client supports it ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)).
- **The person's sign-out does not end agent sessions,** and the sign-out page says so.
- **Each agent session is listed and revocable by the person,** in the console, and its start, its use and its end are audit facts.
- **Concurrent agent sessions are capped per person and client:** a default of 5 and a maximum of 20, set per installation. A new consent past the cap is refused; the person revokes one first.

### One session chain per issuer

A `SessionChain` belongs to one issuer, one tenant and one client. Another issuer never reads it, and two tenants never share one, even inside one boundary. Single sign-on across tenants is the upstream's session, and each tenant's policy may still demand a step-up or a fresh sign-in ([0017](/decisions/0017-identity-and-sign-in/)).

### Across boundaries

A boundary's session never outlives the session that seeded it. The exchange into a boundary carries the original start and class of the session, and the boundary applies its own limits under them, so it can shorten and never lengthen.

### Customers

A customer is a human session like any other: sluis adds no class and no limit longer than 24 hours for the people of a customer-identity installation ([0032](/decisions/0032-customer-identity-installations/)).

- **The upstream holds the long single-sign-on session.** sluis keeps the 24-hour absolute limit above. When it ends, sluis authenticates the person at the upstream again, silently with `prompt=none` while the upstream's own session is valid, and interactively otherwise. A silent authentication starts a new session with a fresh check of the upstream and of the person's state; it does not extend the old one (REQ-SESS-035). This renewal is planned. A staff sign-in keeps the periodic full sign-in of [0017](/decisions/0017-identity-and-sign-in/).
- **A customer's step-up is a re-authentication at the upstream** with the `acr_values` and `max_age` that policy asked for (REQ-SESS-036). sluis enrols no factor and asks for no passkey of a person who signs in through a customer upstream (REQ-SESS-037).

### Configuration

- **The limits are fields of the layered kind `TokenLifetime`,** with the combine rule shortest wins ([0004](/decisions/0004-the-data-model/)). The layers are the built-in hard caps ([0006](/decisions/0006-keys-and-sealing/)), the config kind `TokenLifetime`, and the durable kind `TokenLifetimeTenant`, which a tenant admin writes and `exchange` owns. A tenant's value longer than config is refused at the store; a tenant that shortens its people's sessions is tightening, and that takes effect at once. The built fields are `id_token`, `access_token`, `human_session_absolute` and `refresh_idle`; the fields of the agent class come with it.
- **The configuration check refuses** a value above a maximum, an idle limit above its absolute limit, a token lifetime above the absolute limit (a session has to end some time after sign-in, and a token cannot outlive the session that grants it), and an agent class on a client that exchanges a person's token for other credentials. The first three are built ([REQ-TOKEN-050](/requirements/token/#REQ-TOKEN-050)); the agent class waits for agent sessions.

## Consequences

- A person signs in at most once a working day on each device. An abandoned browser or laptop stops refreshing after 12 hours.
- An agent keeps working for weeks without the person present, yet it stops at once when the person leaves, is disabled or withdraws the consent. The cost is a second set of limits, a consent on the chain and a listing the person must be able to read.
- Agents are not ended by sign-out, which surprises some people; the sign-out page and the listing make it visible, and an operator revoke ends them.
- Machine clients have nothing to revoke but their credential; a revocation reaches them when their last token expires, so their tokens stay short.
- A cap on agent sessions bounds the damage of a client that registers a new session on every start.
- A customer is signed out of sluis at least once a day and back in by the upstream without a prompt, so a customer whom the upstream has disabled is refused within 24 hours at the latest, whatever the length of the upstream's own session. The cost is one silent redirect a day and a dependence on the upstream's session outlasting it.
- Every request that carries a session cookie costs one batched open at key@. On Lambda that is an invoke.
- Chains live in the temp class, so a lost engine or a restore signs everyone out. That is the price of keeping sessions out of backups and out of the durable engine.
- A held person's access token is served until it expires or its chain ends, which is as long as the access token lives. That is the cost of a UserInfo that reads the chain only.

## Roads not taken

- **A human absolute limit above 24 hours.** v1 kept 24 hours, and a person who needs a longer-lived grant is what the agent class is for.
- **A cap of one agent session per person and client.** A person who uses the same client on two devices would lose one each time they signed in on the other.
- **No default cap, left to each installation.** An installation that sets none would have no bound on live agent grants.
- **One idle and one absolute limit for every refresh token.** An agent would either stop every day or a person's browser would stay signed in for a month.
- **An idle timeout measured at the issuer for agents.** An agent refreshes on its own schedule, so the issuer has no signal of a person's attention to measure.
- **Agent sessions ended by the person's sign-out.** Signing out of a browser would silently break every background agent; v1 made the same choice for the same reason.
- **Sessions for machine clients.** A session needs someone who signs in and out; a machine has a proof, and a session record would only be state to keep and revoke.
- **The class declared by the client or its metadata document.** Anyone who publishes a document could give their client a month-long session.
- **A third class for customers, or a longer limit per tenant.** A second maximum to defend, and a window of weeks after a disable that nothing signals yet; the upstream already holds the long session.
- **A boundary that sets its own limits from scratch.** A boundary could then extend a session the front had already ended.
- **The successor kept in the chain, sealed.** It would make a refresh token readable from the store with key@'s help for as long as it is current, against the hashed treatment of the kind; the derived successor keeps a copy of the store useless.
- **Spent markers, the hashes of the last 16 rotations.** A token older than that was refused as unknown while its chain lived, so a thief who held an old token was not caught; and a longer list only moves the limit and grows the chain with each rotation.
- **A tag over the `sid` and the generation alone.** It would be the same for every holder of a generation, and key@ plus a guessed `sid` would make a live token; the salt that only `signin` can open keeps a mac key alone from making one.
- **A longer token, with the secret and the tag side by side.** Two key@ calls on each rotation and a token of about 100 characters, to keep a random secret that the hash of the chain and the sealed salt already protect.
- **The generation in the token and nothing else, no tag.** Anyone who guesses the number could end a chain.
- **A random successor and no grace window.** Two requests racing on one token would sign the person out, and a client that lost a response would too. v1 answered a racing replica with the same successor for 30 seconds, and so do we.
- **A successor derived without a key.** A hash of the old token alone lets anyone who ever held an old token compute every later one, which defeats the reuse check.
- **A list of ended sessions for bearer endpoints.** It would be state to keep and to bound, for a question the chain answers; the chain is the list of live sessions.
- **The person's state read at every UserInfo.** A hold or a suspension would then be seen at once, at the cost of a policy decision, or a listing of holds, on a read path that returns one claim. The listing of holds that a refresh takes may make the read cheap enough to do on every request, and that would close the gate on REQ-TOKEN-006.
- **Answering 400 to a token that is not found.** RFC 7009 section 2.2 says 200 for an invalid token: a different answer for a token that is there and one that is not would be an oracle for the existence of tokens.
- **Revoking an access token by ending the chain behind it.** It would make a revocation of a token that every other resource server still accepts look like a recall, and end a session the client did not ask to end.
- **Chains as durable data.** A chain would survive a lost engine, but a restore would bring back sessions of people who were signed out or removed since the archive, and every refresh would be a durable write.
- **The session cookie opened by `signin` with a key of its own.** It would save a hop, but a plaintext cookie key would sit in the memory of a work actor, where plaintext should not live.

## Follow-ups

- Sessions, sign-in, listing and revocation for people: [#793](https://github.com/truvity/sluis/issues/793).
- Agent-class sessions and consent: [#794](https://github.com/truvity/sluis/issues/794); MCP authorization: [#822](https://github.com/truvity/sluis/issues/822); Client ID Metadata Documents: [#792](https://github.com/truvity/sluis/issues/792).
- Sender-constrained tokens with DPoP: [#912](https://github.com/truvity/sluis/issues/912).
- Open: an index of `PersonHold`, which a tenant with more than a few thousand holds needs; none is declared today.
