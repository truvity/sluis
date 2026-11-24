# 0056 — mimic shows a chooser of the tenant's fixed identities when a person names none

**Status:** Accepted
**Date:** 2026-11-24

Proposal: [issue #1966](https://github.com/truvity/sluis/issues/1966).

## Context

[0054](/decisions/0054-mimic-is-a-test-identity-provider-of-fixed-identities-on-development-only/) makes mimic a test identity provider of fixed identities. Its decision 4 says there is no interactive form: a person or a CI job names the identity as `login_hint`, or takes the tenant's one identity. A tenant of two or more identities and no `login_hint` is refused with `login_required`.

A person who signs in by hand through a realm of the group's identity provider starts at the relying party, and the realm builds the authorization request to mimic. The realm's login page has no place for a `login_hint`, so a manual sign-in to a tenant of two identities, such as a staff identity and an admin identity, always ends in that refusal.

The road 0054 did not take is a form where the tester types an identity: a token could then say anything, and nothing would tie it to a person or a job. A list of the tenant's own identities from config, shown after the session and the act-as group are checked, mints nothing that the `login_hint` path cannot mint for the same session.

## Decision

- **mimic shows a chooser.** When an authorization request has a session, the session may act in the tenant, the tenant has two or more identities and the request has no `login_hint`, mimic answers with a plain HTML page that lists the tenant's identities, each with its name and username. Only the config's identities are listed; nothing is typed.
- **A choice is a `login_hint`.** Each identity is a form that posts the choice, and mimic resumes the same authorization request with that identity as its `login_hint`, through the same handler. Every check of the request (the client, the redirect URI, the response type, the scope, PKCE, the session and the act-as group) runs again on the resumed request, and the token names the subject in `act` as before.
- **The choice is bound to the session.** The form is a POST with a token: an HMAC, under a key derived from mimic's secret for this purpose only, of the session cookie, the tenant and the exact query of the authorization request. The session cookie carries a random session id, so two sessions never share a token. mimic refuses a choice whose token does not verify against the request's own session cookie with `403`, and starts nothing. A cross-site page cannot read the chooser, so it cannot learn the token, and the `SameSite=Lax` session cookie is not sent with a cross-site POST in the first place.
- **The page is inert.** Its output is escaped by `html/template`; it loads nothing, runs no script, and is sent with `Content-Security-Policy: default-src 'none'; base-uri 'none'; frame-ancestors 'none'`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` and `Cache-Control: no-store`.
- **A silent request is still refused,** now with `account_selection_required`. OpenID Connect Core 1.0, section 3.1.2.6, defines `login_required` as "the Authorization Server requires End-User authentication", and `account_selection_required` as the case where the End-User may be authenticated with different associated accounts but did not select one, when `prompt=none` forbids the page that would ask. With a session, the person is authenticated and only the choice is missing, so `account_selection_required` is the precise code. `interaction_required` is the generic one, for any other interaction; the specific code tells a relying party what to ask for. A request with `prompt=none` and no session is still `login_required`.
- **Nothing else changes.** A request with a `login_hint`, a tenant of one identity and the CI hand-off (`POST /session`) behave as before.

### What this supersedes

- **[0054](/decisions/0054-mimic-is-a-test-identity-provider-of-fixed-identities-on-development-only/), in part.** In "The five decisions", decision 4's "There is no interactive form": a person may now pick one of the fixed identities on the chooser, as above. Its road not taken, a form where the tester types an identity, stays not taken. Everything else in 0054 stands.

## Consequences

- A person can sign in by hand through a realm to a tenant of two or more identities, with no realm change.
- CI keeps naming the identity with `login_hint`; a test never has to drive the chooser.
- A relying party that sent `prompt=none` without a `login_hint` now gets `account_selection_required` instead of `login_required`.
- It costs a small page, a POST endpoint under each issuer (`<issuer>/authorize/choose`), and one more key derived from the secret. A session cookie issued before this change has no session id and still works; it expires within `session_ttl`.

## Roads not taken

- **A default identity per tenant.** A person could never reach the other identities by hand, and a silent default hides which identity was used.
- **One tenant per identity.** Twice the issuers and the realms' upstreams, and a tenant would no longer stand for one company's tenant.
- **Keep the refusal.** The realm's login page has no place for a `login_hint`, which is the failure this record fixes.
- **Links instead of forms.** A GET that picks an identity can be started by any cross-site link or image, and a token in a URL leaks through history and logs.
- **`interaction_required` for a silent request.** It is the generic code; `account_selection_required` says what is missing.

## Follow-ups

None.
