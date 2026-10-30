---
title: Generic OIDC and peer sluis
description: How sluis signs people in through any OpenID Connect provider, strictly and without trusting the provider's group claims, and how one sluis trusts another.
status: target
sidebar:
  order: 3
---

Google Workspace is one upstream sign-in provider. The **generic OpenID Connect upstream** is the same kind of integration actor for any provider that publishes OIDC discovery: a customer's identity provider, a partner's, or a standalone one. A **peer sluis** is the special case where the provider is another sluis installation.

## What it's for

- **A customer or partner signs in with their own provider.** Their company runs an OpenID Connect provider; sluis sends the person there and accepts the proof it returns.
- **A person is routed by the domain of their email address.** `person@partner.example` goes to the partner's provider, `person@example.org` to the company directory. The routing is on the domain, and a domain claim is proven with a DNS TXT record rather than taken on trust.
- **Several environments, one place to sign in.** An installation in one environment (development, staging or production, or in one regulated entity) accepts people vouched for by an installation in another, and the trust graph stays acyclic.
- **A robot in one environment calls a service in another.** The peer's tokens are verified inbound; outbound, the peer's own policy decides what the caller gets.
- **A customer-identity (CIAM) product plugs in as an upstream.** sluis stays a broker (agreed): it does not become the customer-identity product. A CIAM product that speaks OpenID Connect is one more upstream, reached through this generic connection, and its end users get sluis subjects like any other signed-in person.

## Integration actors

| Actor | What it does |
|---|---|
| `Upstream` | Begins and completes a sign-in as an OpenID relying party. |
| `FactsSource` | Returns the claims in the ID token as facts. Whether groups asserted by the provider are used at all is a per-connection switch, default off. |
| `WorkloadVerifier`, `CredentialMinter` | Peer sluis only: verify the peer's tokens inbound, and ask the peer's policy for a token outbound by token exchange (RFC 8693). |

## What sluis asks the vendor for

Nothing beyond being a registered client. sluis asks for the scopes `openid email profile`, and holds one client id and one client secret (or a signing key for `private_key_jwt`) per connection, as credentials. It reads the provider's discovery document and key set from public URLs.

The checks sluis makes on every returned ID token are strict: issuer and audience match, `nonce` matches the one it sent, the signature verifies against the provider's keys, the email is verified, and the account's `sub` is the one first recorded for that address. A token with a wrong `aud` or `nonce` is refused.

## What a connection can do

Beyond sign-in, a connection supports the following, so that a CIAM product or any strict provider can be an upstream (agreed):

- **Claim mapping per upstream.** Each connection maps the provider's claims to the facts sluis uses.
- **`acr` and `amr` passthrough.** The authentication context class and the methods the provider reports are carried into the session, so that policy can see how strongly a person signed in.
- **Step-up (RFC 9470).** When a grant needs a stronger sign-in than the session has, sluis sends the person back to the upstream with a challenge for a higher `acr`, or asks for a passkey ([Context](/architecture/context/#second-factor)).
- **End-user subjects.** A person who signs in through a CIAM product gets a subject of their own.
- **Back-channel logout.** The upstream can end the session at sluis with an OpenID Connect back-channel logout token.
- **Rate limits** per connection.

A SAML identity provider, and SCIM provisioning for AWS IAM Identity Center, are backlog ([#918](https://github.com/truvity/sluis/issues/918)). The work is tracked as [#916](https://github.com/truvity/sluis/issues/916).

## What policy controls

- **Which connections exist and for which domains.** A connection serves a list of email domains; each is proven by a DNS TXT record.
- **What a connection's claims are worth.** Per connection: whether asserted groups are ignored (the default), mapped, or trusted for named groups. A `trust` level also decides what a sign-in through it may reach, so a partner's sign-in does not reach a role meant for employees.
- **Admission.** A sign-in proves an identity; the client's `requires` and the policy decide what it is admitted to.
- **For a peer:** which peer issuers are trusted, and the audiences each may exchange for.

## What is audited

- Each sign-in through a connection, the connection used, the class of session and the outcome.
- A refusal, with its reason in the record (not shown to the caller): a subject that changed, an unverified email, a bad `nonce`.
- The connection's lifecycle: created, changed, disconnected.
- For a peer: each inbound verification and each outbound exchange.

## Setup

An example; see [the generic OIDC pattern](/integrations/connecting/#generic-oidc-upstream).

1. Register sluis as a client at the provider, with the callback URL that sluis prints.
2. Run `sluis integrations connect oidc --issuer https://idp.example.org --domain partner.example`.
3. Put the client secret in the credential store. The command prints the `cred:` reference to use.
4. Add the DNS TXT record that proves the domain, then run `sluis integrations check oidc partner`.

```yaml
# /etc/sluis/install.yaml (example)
integrations:
  oidc:
    partner:
      actors: [upstream, facts]
      issuer: https://idp.example.org
      client_id: sluis-partner
      client_secret: cred:sluis-int-oidc.partner.client-secret
      domains: [partner.example]
      groups: ignore                      # ignore | map | trust
      trust: external                     # what this sign-in may reach
```

## v1 → v2 notes

- v1 has one upstream, Google, and no generic OIDC connection. The Google sign-in checks are the model for the generic one.
- Peer sluis is new in v2. In v1, separate installations were connected by hand through ordinary clients.
- Parity issue: [#807](https://github.com/truvity/sluis/issues/807) for the Google sign-in the generic upstream generalises. The generic connector itself, with its OpenID relying-party conformance runs, is part of the [conformance programme](/standards/conformance/).

:::note[Proposed]
The generic OIDC connector, the DNS TXT proof of a domain and the per-connection trust levels are designs from the customer-identity work. None is decided in detail, and the connection's configuration keys above are examples.
:::
