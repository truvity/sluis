---
title: mimic, the test upstream
description: How to run mimic, the test identity provider of development that stands in for Google Workspace or Entra ID, and point a realm at it.
status: target
sidebar:
  order: 5
---

`mimic` is a small OpenID Connect provider for tests. On development it stands in for a company's Google Workspace or Microsoft Entra ID tenant, so a realm of the group's identity provider, or a sluis instance under test, can federate to an upstream that a tester may sign in to. Every identity it mints is a fixed one from its config, on a made-up test domain. Every token names the real person or CI job behind it. The decision and the roads not taken are in the proposal, [#1950](https://github.com/truvity/sluis/issues/1950). The code is `tools/mimic`, and the executable is `sluis-mimic`.

## What it's for

- **Development can test sign-in through Google or Entra.** A realm whose upstream is a company's tenant gets an upstream on development that nobody has to buy, and a browser test can run the whole flow.
- **Every test sign-in has a name.** A person signs in at the stable sluis first, with its multi-factor sign-in, and a CI job hands over its own sluis token. The tokens of mimic carry that subject in `act`, so a test account always leads back to who used it.
- **No token can pass for a real customer's.** The domains are under `.test` or `.example`, the issuer is mimic's, and mimic does not start outside development.

## Where it runs

mimic runs on development only. It refuses to start unless its config says `environment: devel`, and `sluis-mimic check` refuses such a config too. An installation on staging or production refuses mimic's issuers as an upstream. That refusal belongs to the deployment that sets each realm's upstream, because a copied config file could still name a mimic issuer.

mimic is a relying party of the **stable sluis**, the released installation that people and jobs already sign in to. It is never a relying party of a sluis instance under test: that instance would federate to mimic, which would sign in at that same instance, a loop that tests nothing.

Run one replica. Authorization codes are held in memory for a minute, so a second replica would refuse the codes of the first. Sessions are sealed cookies, so they survive a restart and a stable sluis that is down.

## The image

mimic is not part of a release (ADR 0054, decision 1), so no release tag publishes it. The workflow `mimic.yaml` builds an image on every push to `main` that changes mimic, and pushes it as `ghcr.io/truvity/sluis-mimic:main-<commit>`, the first 12 characters of the commit, for `linux/amd64` and `linux/arm64`. The run's summary prints the digest; pin the image by it. The image is distroless and runs as a user that is not root, and its default command is `serve -config /etc/mimic/mimic.yaml`. Build the same executable locally with `just build-mimic`.

## How a subject gets a session

A person:

1. A relying party sends the browser to a mimic issuer's `authorize` endpoint.
2. With no session, mimic sends the browser to `/login`, which sends it to the stable sluis with PKCE, a state and a nonce.
3. The person signs in there, and the stable sluis returns to mimic's `/callback`.
4. mimic redeems the code and checks the ID token: the issuer, the audience, the time window, the nonce and the signature. It also checks `amr` when `require_amr` is set.
5. mimic sets its session cookie and returns to the authorization request.

A CI job:

1. The job gets a token from the stable sluis by workload identity, for mimic's audience (`sluis.audience`, by default mimic's client id).
2. The job posts it: `POST /session` with `Authorization: Bearer <token>`. mimic checks it as in step 4 above and answers `204` with the session cookie.
3. A browser test runs the ordinary flow with that cookie. In Playwright, post the token with the browser context's `request`, so the cookie lands in the context, then open the relying party.

The session lasts `session_ttl`, one hour by default. `POST /logout` ends it.

## Who may act as whom

A tenant names one group, `act_as`, whose holders may use its identities. mimic reads it from the `groups` claim of the stable sluis's token, the claim sluis puts grant names in. When the config names no group, it is `devel:shared:keycloak:<tenant>:act-as`. Grant names are narrowed to the audience, so mimic's client at the stable sluis lists the act-as things in its `groups` override. Without that, the group never reaches mimic.

The identity is the `login_hint` of the authorization request. A tenant with one identity needs none. Any other request is refused back to the relying party:

| Request | Error |
|---|---|
| The session's groups do not hold the tenant's `act_as` | `access_denied` |
| The `login_hint` names no identity of the tenant | `access_denied` |
| No `login_hint` in a tenant of two or more identities | `login_required` |
| `prompt=none` with no session | `login_required` |

## Config

```yaml
environment: devel                     # mimic refuses any other value
public_url: https://mimic.devel.example.com
listen: 127.0.0.1:8080
session_ttl: 1h
keys:
  signing: file:/run/mimic/signing.pem # PKCS#8 PEM: ECDSA P-256 (ES256) or RSA of 2048 bits or more (RS256)
  secret: env:MIMIC_SECRET             # at least 32 random bytes
sluis:                                 # the stable sluis, never the instance under test
  issuer: https://sluis.example.com
  client_id: mimic
  client_secret: env:MIMIC_SLUIS_CLIENT_SECRET
  algorithms: [ES384]
  require_amr: [hwk]
tenants:
  - name: example-a
    profile: entra
    domain: example-a.test             # under .test or .example only
    clients:
      - id: idp-staff-example-a
        redirect_uris: [https://idp.devel.example.com/realms/staff-example-a/broker/mimic-entra-example-a/endpoint]
        secret: env:MIMIC_CLIENT_STAFF_EXAMPLE_A
    identities:
      - {username: alice, name: Alice Example, groups: [staff, admins]}
      - {username: bob, email: bob.builder, name: Bob Example, groups: [staff]}
  - name: example-b
    profile: google
    domain: example-b.test
    clients:
      - id: idp-staff-example-b
        redirect_uris: [https://idp.devel.example.com/realms/staff-example-b/broker/mimic-google-example-b/endpoint]
        secret: env:MIMIC_CLIENT_STAFF_EXAMPLE_B
    identities:
      - {username: carol, name: Carol Example, groups: [staff]}
```

| Field | Meaning | Default |
|---|---|---|
| `environment` | Must be `devel` | none |
| `public_url` | Where relying parties reach mimic; each issuer is `<public_url>/<profile>/<tenant>`. HTTPS, or HTTP on a loopback address for a test | none |
| `listen` | The address the server listens on, behind the TLS front | `127.0.0.1:8080` |
| `session_ttl` | How long a session lasts, from 1m to 12h | `1h` |
| `keys.signing` | The signing key of every issuer | none |
| `keys.secret` | The secret that the subject hash and the cookie key are derived from (HKDF) | none |
| `sluis.issuer`, `sluis.client_id` | The stable sluis and mimic's client there | none |
| `sluis.client_secret` | That client's secret; empty for a public client, which uses PKCE alone | empty |
| `sluis.audience` | The audience a CI job's token must carry | `sluis.client_id` |
| `sluis.scopes` | The scopes of a person's sign-in; must hold `openid` | `[openid]` |
| `sluis.algorithms` | The algorithms accepted from the stable sluis | `[ES384]` |
| `sluis.require_amr` | When set, a person's ID token must carry one of these `amr` values | empty |
| `tenants[].name`, `profile` | The tenant's path and its profile, `entra` or `google` | none |
| `tenants[].domain` | The test domain of the identities' e-mail, under `.test` or `.example` | none |
| `tenants[].act_as` | The sluis group whose holders may use the identities | `devel:shared:keycloak:<name>:act-as` |
| `tenants[].clients[]` | The relying parties: an id, the exact redirect URIs, and a secret, or none for a public client | none |
| `tenants[].identities[]` | `username` (the `login_hint`), `email` (the local part), `name`, `groups` | `email` is the username |

A key or a secret is a source: `file:<path>` or `env:<NAME>`. A trailing newline is dropped. A source that mimic cannot read stops the start, and so does one that is empty: an unset or empty variable, a missing file, or a file of a newline alone. mimic never runs a client whose secret it could not read, and never runs it as a public client instead. On development the values come from the installation's secret store, such as AWS SSM Parameter Store, through environment variables or files that the runtime mounts. Make them for mimic alone: nothing is shared with any real identity provider. Keep the secret stable, because a new one gives every identity a new `sub`, and every relying party sees new accounts.

`sluis-mimic check -config <file>` reads the config and every source, and exits `0` when mimic would start. `sluis-mimic serve -config <file>` runs it. Unknown keys are refused.

## Issuers and endpoints

Each tenant is one issuer, `<public_url>/<profile>/<tenant>`, for example `https://mimic.devel.example.com/entra/example-a`.

| Path under the issuer | What it does |
|---|---|
| `/.well-known/openid-configuration` | Discovery: S256 only, `authorization_response_iss_parameter_supported: true` |
| `/jwks` | The key set: one key, its `kid` the RFC 7638 thumbprint |
| `/authorize` | The code flow, with `response_type=code`, an S256 `code_challenge` and `openid` in the scope; answers with `code`, `state` and `iss` (RFC 9207) |
| `/token` | Redeems a code once, within a minute, for the client, the redirect URI and the PKCE verifier it was issued for; client authentication `client_secret_basic`, `client_secret_post` or `none` |
| `/userinfo` | The claims for an access token of this issuer |

mimic's own paths are `/login`, `/callback`, `POST /session`, `/logout` and `/healthz`.

## Claims

An ID token (type `JWT`) and an access token (type `at+jwt`) live ten minutes and carry:

| Claim | Value |
|---|---|
| `iss` | The tenant's issuer |
| `sub` | A keyed hash (HMAC-SHA256, base64url) of the stable sluis's `sub`, the tenant and the identity: the same on every run while the secret stays |
| `aud` | The client id |
| `email`, `name` | The identity's local part at the tenant's domain, and its name |
| `groups` | The identity's groups from config |
| `act` | `{"sub": <the stable sluis's sub>}` (RFC 8693, section 4.1) |
| `nonce`, `auth_time` | The request's nonce, and when the session started (ID token) |

A profile adds what its real counterpart sends:

| Profile | Adds |
|---|---|
| `entra` | `tid`, a stable UUID of the tenant; `oid`, a stable UUID of the identity for this subject; `ver: "2.0"`; `preferred_username`, the e-mail address |
| `google` | `hd`, the tenant's domain; `email_verified: true` |

## Pointing a Keycloak realm at mimic

Add an identity provider of the type OpenID Connect v1.0 to the realm, one per mimic tenant. Then add the realm's broker endpoint to the tenant's client in mimic.

1. Set the discovery endpoint to `<issuer>/.well-known/openid-configuration`, and give the provider an alias such as `mimic-entra-example-a`.
2. Set the client id and secret to the tenant's client in mimic, with client authentication "Client secret sent as basic auth".
3. Turn PKCE on, with the method S256.
4. Turn on passing `login_hint`, so a test can name the identity.
5. Add the realm's redirect URI, `https://<keycloak>/realms/<realm>/broker/<alias>/endpoint`, to `redirect_uris` of the tenant's client in mimic.
6. Map the claims. Take the username from `email` or `preferred_username`, and the subject from `sub`. Use an attribute importer of the claim `act.sub` to keep the real subject on the user for the audit trail. Map `groups` as the realm maps its real upstream's groups.
7. In a browser test, open the relying party with `kc_idp_hint=<alias>` and `login_hint=<username>`, after the hand-off of the CI job's token.

The realm's upstream is a per-environment value: mimic's issuer on development, and the real tenant elsewhere. On staging and production a mimic issuer is refused.

## At the stable sluis

mimic is a client of the stable sluis:

- The redirect URI is `<public_url>/callback`.
- Its `groups` override lists the things of the act-as groups it reads, such as `shared:keycloak:example-a`, so that those grant names reach it.
- A CI job that hands its token over exchanges for mimic's audience.
- A person or a job may use a tenant only while they hold the tenant's act-as grant.
