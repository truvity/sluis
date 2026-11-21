---
title: Generic OIDC and peer sluis
description: How sluis signs people in through any OpenID Connect provider, strictly and without trusting the provider's group claims, and how one sluis trusts another.
status: target
sidebar:
  order: 4
---

The **generic OpenID Connect upstream** is the same kind of integration as Google sign-in, for any provider that publishes OIDC discovery: a customer's identity provider, a partner's, or a CIAM product. `adapter-upstream-oidc@<boundary>` serves it, and it is built for sign-in. The upstreams are fixed config that the operator declares. A customer installation trusts one context realm of the group's identity provider, a Keycloak installation with a realm per context, and never the central realm above them; a customer's own identity provider is configured in that realm and never in sluis ([0032](/decisions/0032-customer-identity-installations/), [0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/)). A **peer sluis** is the case where the provider is another sluis installation (planned).

## What it's for

- **An organisation the operator has agreed with signs in with its own provider.** The operator declares it as an `Upstream`; sluis sends the person there and accepts the proof it returns.
- **A person is routed by the domain of their e-mail address.** `person@partner.example` goes to the upstream that declares that domain, `person@example.org` to the company directory. The operator declares which upstream serves which domain, so there is nothing to prove ([0025](/decisions/0025-integrations/#the-upstream-contract)).
- **The group's identity provider is the trusted context realm.** sluis hosts no customer accounts ([REQ-CIAM-017](/requirements/ciam/#REQ-CIAM-017)): the identity provider holds them, and the products use its tokens. `exchange` takes a token of the trusted realm only as the subject token, and only for a call down to infrastructure ([0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/)); it issues no token with a customer, an organisation or a product. This is planned.
- **One sluis trusts another.** An installation accepts people and robots vouched for by another installation, and the trust graph stays acyclic ([Boundaries](/concepts/boundaries/)).

## Integration kinds

| Kind | What it does |
|---|---|
| `Upstream` | `adapter-upstream-oidc` begins and completes a sign-in as an OpenID relying party: it exchanges the code at the provider and returns the verified assertion to `signin`. |
| `WorkloadVerifier` | Peer sluis only: `exchange` verifies the peer's tokens inbound ([0022](/decisions/0022-workload-identity-and-host-enrolment/#proofs-and-the-exchange)). |

## Connections

| Whose provider | Kind | Class | Made by |
|---|---|---|---|
| The operator's own (workforce, a partner the operator agreed with) | `Upstream` and `UpstreamSecret` | Config and secret | Hand or IaC |

- **A tenant has no connection of its own here.** The upstreams are the operator's config, and a customer's own provider is configured in the trusted context realm ([0032](/decisions/0032-customer-identity-installations/#realms-follow-boundaries)).
- **The client secret is the operator's.** It is an `UpstreamSecret` in the secret source, and the adapter keeps nothing at rest. A `private_key_jwt` key is refused, because it would be an authority key ([0025](/decisions/0025-integrations/#the-upstream-contract)).

## What sluis asks the vendor for

Only to be a registered client. sluis asks for `openid email profile` and holds one client id and one client secret per connection. It reads the provider's discovery document and key set from public URLs.

Every returned ID token is checked strictly: issuer and audience match, `nonce` matches the one sent, the signature verifies against the provider's keys, the configured subject claim is present, and the claim matches the one pinned for the issuer. Anything else is refused with `INVALID`; there is no lenient fallback ([0025](/decisions/0025-integrations/#the-upstream-contract)).

The subject claim is `sub` unless the connection says otherwise (`subject_claim`). Microsoft Entra needs `oid`, because its `sub` is different for each application. A person is found by the pair of the issuer and that claim's value, never by e-mail address ([0019](/decisions/0019-person-and-upstream-link/#linking-an-upstream-account)). The tests sign in through Keycloak on every pull request, and through Google Cloud Identity Free and Microsoft Entra ID Free every night ([0019](/decisions/0019-person-and-upstream-link/)).

## What a connection can do

Beyond sign-in, a connection supports what a CIAM product or any strict provider needs:

- **Claim mapping per connection**, from the allow-listed claims of the provider to the facts sluis uses. The customer, the organisation and the product are not among the claims of a sluis token ([0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/)).
- **`acr` and `amr` passthrough**, so policy sees how strongly a person signed in. A provider that sends them only on request, as Google does, needs a configured `claims` request parameter ([#1154](https://github.com/truvity/sluis/issues/1154)).
- **Step-up (RFC 9470).** When a grant needs a stronger sign-in than the session has, sluis sends the person back with a challenge for a higher `acr`, or asks for a passkey ([0017](/decisions/0017-identity-and-sign-in/#step-up)).
- **Sessions and step-up for customers.** The upstream holds the long single-sign-on session; sluis keeps the 24-hour limit of a human session and authenticates the person at the upstream again, silently while the upstream's session is valid. A customer's step-up is a re-authentication at the upstream with the `acr_values` and `max_age` that policy asked for, and sluis asks no customer for a passkey ([0018](/decisions/0018-sign-in-sessions/#customers)).
- **Back-channel logout** from the upstream is planned, with RP-initiated logout ([0017](/decisions/0017-identity-and-sign-in/#optional-openid-flows)).
- **Rate limits** per connection, kept in sluis state ([0036](/decisions/0036-volumetric-abuse/#inside-sluis)).

The sign-in-only slice of the generic connector is built ([#1106](https://github.com/truvity/sluis/issues/1106)). The rest of it, [#916](https://github.com/truvity/sluis/issues/916), is the exchange of a trusted realm's token for a sluis token for an infrastructure audience ([0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/)). A SAML identity provider and SCIM provisioning for AWS IAM Identity Center are Later ([#918](https://github.com/truvity/sluis/issues/918)).

The trusted context realm holds the tenants' identity providers and brokers their social login and SAML ([0032](/decisions/0032-customer-identity-installations/#realms-follow-boundaries)). If it cannot broker SAML for the organisations that need it, the SAML requirement ([REQ-FED-009](/requirements/fed/#REQ-FED-009)) is reopened.

An upstream declares the e-mail domains it serves, carries a `trust` level (`internal`, `partner` or `customer`) that limits what a sign-in through it may reach, and an `assurance` policy that limits how strong it counts ([Kinds](/reference/kinds/#upstream)). Both are set by the operator under review. A client may send `idp_hint` to choose among the upstreams it is allowed.

## What policy controls

- **Which upstreams exist, and for which domains.**
- **What a connection's claims are worth**: only the allow-listed claims are read, and the provider's groups and roles never are.
- **Admission.** A sign-in proves an identity; the client's `requires` and the policy decide what it reaches.
- **For a peer:** which peer issuers are trusted, and the audiences each may exchange for.

## What is audited

- Each sign-in through a connection: the connection, the class of sign-in session and the outcome.
- Each refusal, with its reason in the record (not shown to the caller): a missing subject claim, a contradicted pin, a bad `nonce`.
- The connection's lifecycle: created, changed, disconnected.
- For a peer, each inbound verification and outbound exchange.

## Setup

See [the generic OIDC pattern](/integrations/connecting/#generic-oidc-upstream); names and keys are [illustrative](/integrations/#examples-are-illustrative).

Commit the `Upstream` and put the secret in the secret source:

```yaml
# Upstream, a config kind (example); its secret is an UpstreamSecret in the secret source
upstreams:
  - id: partner
    issuer: https://idp.example.org
    client_id: sluis-partner
    subject_claim: sub
    secret: upstream-partner         # the name of the client secret in the secret source; store@ serves it as UpstreamSecret
    domains: [partner.example]       # the e-mail domains this upstream serves, declared by the operator
    trust: partner                   # what this sign-in may reach
    assurance:                       # how strong this sign-in counts
      ceiling: urn:example:acr:2
      max_age: 12h
```
