# 0054 — mimic is a test identity provider of fixed identities, on development only, that people and CI jobs reach through the stable sluis

**Status:** Accepted; partly superseded by 0056
**Date:** 2026-11-22

Proposal: [issue #1950](https://github.com/truvity/sluis/issues/1950).

## Context

A realm of the group's identity provider takes its staff from an upstream: the company's or the customer's Google Workspace or Microsoft Entra ID tenant ([0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/)). A sluis instance takes its staff from an upstream in the same way ([0019](/decisions/0019-person-and-upstream-link/)). A development installation has no such upstream that a tester may sign in to. So it cannot test those realms, their sessions, first-login linking, or anything downstream of them, and sluis's own federation tests run only against a Keycloak realm on the loopback.

A test upstream that anyone can make say anything is worse than none: a token it mints names nobody, and a test that passes with it says nothing about who did what. So the test upstream must tie every identity it mints to a real person or a real CI job, and must never mint a token that a real relying party could take for a real customer's.

The maintainer decided the five points below on 2026-11-22.

## Decision

We build `mimic`, a small OpenID Connect provider for tests, in this repository at `tools/mimic`, with the executable `sluis-mimic`. It stands in for a company's Google Workspace or Entra ID tenant on development, and it also serves sluis's own federation tests.

### The five decisions

| # | Question | Answer |
|---|---|---|
| 1 | Where it lives | In the sluis repository, as a sluis test tool. It is not part of the product, its releases or its requirements. |
| 2 | The e-mail domains of test identities | Made-up test domains, one per tenant, under a reserved top-level name (`.test` or `.example`, RFC 2606 and RFC 6761). A token never carries a real customer's domain. |
| 3 | Where it runs | On development only. It refuses to start unless its config says `environment: devel`, and an installation refuses its issuers as an upstream on staging and production. |
| 4 | How an identity is chosen | Fixed identities, declared in config. There is no interactive form: a person or a CI job names the identity, as `login_hint`, or takes the one identity it may use. |
| 5 | What it is built on | Our own small provider on the JOSE library sluis already uses (go-jose, [approved libraries](/reference/approved-libraries/)), not mock-oauth2-server. |

### mimic signs people in through the stable sluis

- **mimic is a relying party of the stable sluis,** the released sluis installation that people and jobs already sign in to. It is never a relying party of a sluis instance under test, because a sluis under test that federates to mimic, which signs in at that same sluis, is a loop that tests nothing.
- **A person** signs in at the stable sluis with the authorization code flow and PKCE, with that sluis's own multi-factor sign-in. mimic checks the ID token, may require an `amr` value of its config, and keeps a session cookie of its own.
- **A CI job** gets a token from the stable sluis by workload identity, for mimic's audience, and posts it to mimic. mimic checks it like an ID token and answers with the same session cookie. A browser test (Playwright) then runs the ordinary flow of the relying party under test with that cookie.
- **Who may act as whom comes from sluis groups.** mimic reads the `groups` claim of the stable sluis's token, the claim sluis puts grant names in ([0020](/decisions/0020-grant-vocabulary-and-token-claims/#the-claims-of-a-token)). Each tenant of mimic names one group whose holders may use that tenant's identities. On today's stable line the group is `devel:shared:keycloak:<tenant>:act-as`, a grant name of five segments (v1 ADR 0081, on `master`). The group is config, so a stable line whose grant names have three segments names a three-segment group. The token's `groups` are narrowed to the audience, so mimic's client at the stable sluis declares the act-as things it reads.

### One issuer per profile and tenant

- **An issuer is `<public URL>/<profile>/<tenant>`,** for example `/entra/example-a` or `/google/example-b`. Each has a discovery document, a key set, an authorization endpoint (code flow, PKCE with S256 only), a token endpoint and a userinfo endpoint.
- **A profile shapes the claims like its real counterpart.** `entra` adds `tid` (a stable UUID of the tenant), `oid` (a stable UUID of the identity) and `ver: "2.0"`, and writes `preferred_username` as the e-mail address. `google` adds `hd`, the tenant's test domain, and `email_verified: true`.
- **Every ID token and access token carries:**
  - `iss`, the tenant's issuer;
  - `sub`, a keyed hash of the stable sluis's `sub`, the tenant and the identity, so the same person or job on the same identity has the same subject on every run, and first-login linking and sessions behave as in production;
  - `email`, the identity's local part at the tenant's test domain, and `name`;
  - `groups`, the identity's groups from config;
  - `act: {"sub": <the stable sluis's sub>}` (RFC 8693, section 4.1), which names the real person or job behind the identity, so the relying party can keep it on the account for its audit trail.

### Config and keys

- **Config declares, per tenant,** the profile, the test domain, the act-as group, the clients with their redirect URIs, and the fixed identities: a username, an e-mail local part, a name and groups. A CI job picks an identity by its username.
- **Keys are mimic's own.** The signing key and the secret that the subject hash and the session cookie are derived from are read from a file or an environment variable. On development they come from the installation's secret store. Nothing is shared with any real identity provider.

## Consequences

- Development can test a realm that federates to Google Workspace or Entra ID, end to end, in a browser and in CI, and every test sign-in names the real person or job in `act`.
- sluis's own federation tests can point an upstream at a mimic issuer in-process, with no Keycloak realm to import.
- A token of mimic is useless outside development by construction: its issuer is mimic's, its domains are reserved, and an installation outside development refuses its issuers. The refusal on staging and production is the deployment's to enforce; mimic's own check covers only its own start.
- mimic depends on the stable sluis being up. When it is down, development cannot start a new test sign-in; sessions already open keep working until they expire.
- Each new test tenant is a config change and a grant at the stable sluis, never a code change.
- It costs a small provider to keep: discovery, the code flow, PKCE and the token checks. The JOSE work is go-jose's, and the token checks follow RFC 8725 as sluis's other verifiers do.
- No requirement row follows: mimic is a test tool, and the product's requirements catalogue does not describe it.

## Roads not taken

- **mock-oauth2-server.** A JVM service with claim templates of its own and an interactive login page; it ties no token to a signed-in person, and shaping claims per profile would be templates outside our tests.
- **An interactive form where the tester types an identity.** A token could then say anything, nothing would tie it to a person or a job, and CI cannot drive a form without scraping it.
- **Real customer domains in test identities.** A token with a real customer's domain could be taken for a real customer's token by a relying party that trusts the domain, and it puts customer names into test data.
- **Real test tenants at Google and Microsoft.** Accounts to buy and keep, domains that are real, and secrets shared with a real provider, for a test that only needs the shape of the claims.
- **mimic as a relying party of the sluis instance under test.** A sign-in at the instance under test that federates to mimic would sign in at that same instance: a loop that tests nothing.
- **Running mimic on staging or production.** A provider that mints identities on request has no place where real staff sign in.
- **Who may act as whom in mimic's own config.** A list of people in mimic's config is a second roster beside sluis's grants, and it would drift.

## Follow-ups

- Build the provider and its tests at `tools/mimic`, and point one of sluis's federation tests at it ([#1950](https://github.com/truvity/sluis/issues/1950)).
- An operator page: the config, the profiles, and how a realm points its upstream at a mimic issuer ([#1950](https://github.com/truvity/sluis/issues/1950)).
