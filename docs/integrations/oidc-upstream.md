---
title: Generic OIDC and peer sluis
description: How sluis signs people in through any OpenID Connect provider, strictly and without trusting the provider's group claims, and how one sluis trusts another.
status: target
sidebar:
  order: 4
---

The **generic OpenID Connect upstream** is the same kind of integration as Google sign-in, for any provider that publishes OIDC discovery: a customer's identity provider, a partner's, or a CIAM product. Each connection is its own process, `oidc@<name>`. A **peer sluis** is the case where the provider is another sluis installation.

## What it's for

- **A partner signs in with their own provider.** sluis sends the person there and accepts the proof it returns.
- **A person is routed by the domain of their e-mail address.** `person@partner.example` goes to the partner's provider, `person@example.org` to the company directory. A connection's claim on a domain is proven, not taken on trust.
- **A CIAM product plugs in as an upstream.** sluis stays a broker and hosts no customer accounts ([REQ-CIAM-017](/requirements/ciam/#REQ-CIAM-017)): a CIAM product that speaks OpenID Connect is one more upstream, and its end users get sluis subjects like anyone else who signs in.
- **One sluis trusts another.** An installation accepts people and robots vouched for by another installation, and the trust graph stays acyclic ([Boundaries](/concepts/boundaries/)).

## Integration kinds

| Kind | What it does |
|---|---|
| `Upstream` | Begins and completes a sign-in as an OpenID relying party. |
| `FactsSource` | Returns the ID token's claims as facts. Whether the provider's asserted groups are used at all is a per-connection switch, off by default. |
| `WorkloadVerifier`, `CredentialMinter` | Peer sluis only: verify the peer's tokens inbound, and ask the peer's policy for a token outbound by token exchange (RFC 8693). |

## What sluis asks the vendor for

Only to be a registered client. sluis asks for `openid email profile` and holds one client id and one client secret (or a `private_key_jwt` signing key) per connection, as credentials. It reads the provider's discovery document and key set from public URLs.

Every returned ID token is checked strictly: issuer and audience match, `nonce` matches the one sent, the signature verifies against the provider's keys, the e-mail is verified, and the value of the connection's subject claim is the one first recorded for that person. Anything else is refused.

The subject claim is `sub` unless the connection says otherwise (`subject_claim`). Microsoft Entra needs `oid`, because its `sub` is different for each application. A person is found by the pair of the issuer and that claim's value, never by e-mail address ([ADR 0027](/decisions/0027-a-person-is-tied-to-an-upstream-account-by-issuer-and-subject-claim/)). The tests sign in through Keycloak on every pull request, and through Google Cloud Identity Free and Microsoft Entra ID Free every night.

## What a connection can do

Beyond sign-in, a connection supports what a CIAM product or any strict provider needs:

- **Claim mapping per connection**, from the provider's claims to the facts sluis uses.
- **`acr` and `amr` passthrough**, so policy sees how strongly a person signed in.
- **Step-up (RFC 9470).** When a grant needs a stronger sign-in than the session has, sluis sends the person back with a challenge for a higher `acr`, or asks for a passkey ([ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#step-up)).
- **Back-channel logout**: the upstream can end the session at sluis with a back-channel logout token.
- **Rate limits** per connection.

The sign-in-only slice of the generic connector is in the first working version ([#1106](https://github.com/truvity/sluis/issues/1106)); the rest of it, [#916](https://github.com/truvity/sluis/issues/916), is in the CIAM upstream milestone ([Roadmap](/roadmap/)). End-user subjects follow the subject-lifecycle decision record (planned, [#1024](https://github.com/truvity/sluis/issues/1024)). A SAML identity provider and SCIM provisioning for AWS IAM Identity Center are Later ([#918](https://github.com/truvity/sluis/issues/918)).

:::note[Proposed]
A connection proves each domain it serves with a DNS TXT record, and carries a `trust` level that limits what a sign-in through it may reach, so a partner's sign-in does not reach a role meant for employees. Both are designed with the CIAM upstream ([#1023](https://github.com/truvity/sluis/issues/1023)).
:::

## What policy controls

- **Which connections exist, and for which domains.**
- **What a connection's claims are worth**: asserted groups ignored (the default), mapped, or trusted for named groups.
- **Admission.** A sign-in proves an identity; the client's `requires` and the policy decide what it reaches.
- **For a peer:** which peer issuers are trusted, and the audiences each may exchange for.

## What is audited

- Each sign-in through a connection: the connection, the class of session and the outcome.
- Each refusal, with its reason in the record (not shown to the caller): a changed subject, an unverified e-mail, a bad `nonce`.
- The connection's lifecycle: created, changed, disconnected.
- For a peer, each inbound verification and outbound exchange.

## Setup

See [the generic OIDC pattern](/integrations/connecting/#generic-oidc-upstream); names and keys are [illustrative](/integrations/#examples-are-illustrative).

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
