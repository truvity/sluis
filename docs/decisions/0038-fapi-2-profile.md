# 0038 — FAPI 2.0 is a profile a client declares, with PAR, DPoP, private-key client authentication and its own signing algorithm, and sender constraint is declared per audience

**Status:** Accepted; amended by 0044
**Date:** 2026-11-18

Proposal: [issue #1025](https://github.com/truvity/sluis/issues/1025).

> Superseded on 2026-11-12 by [0045](/decisions/0045-customers-sign-in-upstream-and-the-sluis-token-carries-customer-org-and-product/) for customer-facing use: the customer-facing FAPI 2.0 profile is the upstream's, and sluis serves no customer sign-in (D1 B, D7 A, 2026-11-12). Revived on 2026-11-18 for the core issuer (decisions A2 and H1, [0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/#amendment-of-2026-11-18-the-order-after-the-core-the-shared-blocks-hardening-the-console-and-kv)): see the amendment below.

## Context

Some relying parties of a customer-identity installation, a regulated party's among them, need the FAPI 2.0 Security Profile: authorization by pushed request, tokens bound to a key, clients that authenticate with a key, the issuer in the response, and algorithms the profile allows. The work is split over [#912](https://github.com/truvity/sluis/issues/912) (the features) and [#929](https://github.com/truvity/sluis/issues/929) (the conformance plans); this record was decided in [#1025](https://github.com/truvity/sluis/issues/1025), which is closed. The board holds the two open issues in the customer-identity milestone, and the roadmap does not list FAPI under "Later". [0017](/decisions/0017-identity-and-sign-in/#optional-openid-flows) says FAPI 2.0 comes after the parity milestone, which is where the customer-identity milestone sits.

Two statements disagree on binding. Threat T-1 says tokens "are sender-constrained"; [0017](/decisions/0017-identity-and-sign-in/#subjects) and [0020](/decisions/0020-grant-vocabulary-and-token-claims/#binding) say an audience that declares no binding gets a bearer token, because AWS web identity, the Kubernetes API server and Argo CD accept bearer JWTs only. FAPI 2.0 also does not allow ES384, which is sluis's default algorithm ([0006](/decisions/0006-keys-and-sealing/#algorithms)); [#912](https://github.com/truvity/sluis/issues/912) lists PS256, ES256 and EdDSA as the ones to support.

## Decision

### Binding is declared per audience

- **Each audience declares its binding: none, DPoP or mutual TLS.** None gives a bearer token without `cnf`, the default; DPoP gives a token with `cnf.jkt`; mutual TLS gives `cnf.x5t#S256`. A DPoP proof sent by a client whose profile does not use DPoP is refused ([REQ-TOKEN-012](/requirements/token/#REQ-TOKEN-012)).
- **T-1 is read that way.** A token is sender-constrained where its audience declares a binding; a bearer audience has the token's short lifetime as its only bound, and the threat model says so.

### The profile

A client or an audience may declare `profile: fapi2`. An installation client declares it in config. A tenant client declares it only when its client template allows the profile ([0021](/decisions/0021-clients/#the-client-declaration)). Then `sluis config check` requires, and the token and authorization endpoints enforce:

- **Pushed authorization requests (RFC 9126).** The authorization endpoint accepts only a `request_uri` from a PAR call; a `request_uri` is single-use and short-lived. A "PAR required" switch exists for the client and for the installation.
- **PKCE with S256, the authorization code flow only,** exact redirect URI matching, and the `iss` response parameter of RFC 9207.
- **Client authentication by `private_key_jwt`** or mutual TLS, never a client secret. The client's public key is in its client declaration; sluis holds no key of the client, and the assertion rules of [REQ-CLIENT-017](/requirements/client/#REQ-CLIENT-017) to [REQ-CLIENT-019](/requirements/client/#REQ-CLIENT-019) (audience, lifetime of at most 60 seconds, a `jti` accepted once) are enforced as MUST for this profile.
- **A sender-constrained access token: DPoP first,** with the server-provided nonce the plan requires. Mutual TLS follows its own consumer ([REQ-CLIENT-014](/requirements/client/#REQ-CLIENT-014)).
- **Its own signing algorithm: ES256 first.** The profile's audience signs with an ES256 key of the boundary, a dedicated key beside the ES384 token key, in the key set under its own `kid`. `signer` signs with it through key@, and on a board it has slot `8c` of the default card slot map ([0006](/decisions/0006-keys-and-sealing/#the-slot-plan)). PS256 and EdDSA are added when a client needs them and the key store offers them. The default stays ES384 ([REQ-TOKEN-019](/requirements/token/#REQ-TOKEN-019)). Algorithm keys are per boundary, never per tenant ([0006](/decisions/0006-keys-and-sealing/#key-purposes)).
- **Signed request objects (RFC 9101) are accepted** from a client of the profile that sends them. Response signing and signed introspection, the parts of FAPI 2.0 Message Signing, are built with the first relying party that requires them; until then discovery does not advertise them and a request object is refused.
- **Details the plan checks,** such as the exact nonce behaviour, are implemented as the pinned revision of the plan needs and recorded in the standards register.

### Claims of conformance

sluis claims a FAPI 2.0 plan only when it passes ([Conformance](/standards/conformance/)). The first plan is the FAPI 2.0 Security Profile OP plan for the variant above (DPoP, `private_key_jwt`, PAR); Message Signing follows with its relying party. sluis is not strong customer authentication ([REQ-CIAM-009](/requirements/ciam/#REQ-CIAM-009)).

### The roadmap and the board

The FAPI 2.0 Security Profile is in the customer-identity milestone: its done-when names the profile for clients that declare it, [#912](https://github.com/truvity/sluis/issues/912) and [#929](https://github.com/truvity/sluis/issues/929) stay in that milestone, and the roadmap's "Later" list does not carry FAPI.

### Amendment of 2026-11-18: the profile is revived for the core issuer

2026-11-18 (maintainer decisions A2 and H1; [0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/#amendment-of-2026-11-18-the-order-after-the-core-the-shared-blocks-hardening-the-console-and-kv)). [0045](/decisions/0045-customers-sign-in-upstream-and-the-sluis-token-carries-customer-org-and-product/) superseded this record on 2026-11-12 for customer-facing use, and that stands: sluis hosts no customer sign-in, and a customer's own profile is the upstream's. The profile itself is revived as core security hardening, "to get things done properly even in the first milestone", and is built in the core phase.

- **The decision above stands as written,** for the core issuer and for any relying party that declares the profile: binding declared per audience (none, DPoP or mutual TLS), PAR, PKCE with S256, private-key client authentication, DPoP with the server-provided nonce, a signing algorithm per client, and `iss` in the response. The words "customer-identity installation" in the Context read "installation".
- **The profile is a choice of the client.** A relying party that cannot do PAR or DPoP is not required to; the default clients keep ES384 and bearer tokens.
- **The OpenID Foundation FAPI 2.0 conformance suite runs in CI beside the Basic suite** ([Conformance](/standards/conformance/)), against the local installation first.
- **The roadmap and the board:** the profile is in the core phase, not in the customer-identity milestone, and [#912](https://github.com/truvity/sluis/issues/912) and [#929](https://github.com/truvity/sluis/issues/929) move with it. This replaces the section "The roadmap and the board" above.
- **The requirement rows** that the cancellation withdrew ([REQ-CLIENT-035](/requirements/client/#REQ-CLIENT-035) to [REQ-CLIENT-040](/requirements/client/#REQ-CLIENT-040), [REQ-TOKEN-040](/requirements/token/#REQ-TOKEN-040) and [REQ-TOKEN-041](/requirements/token/#REQ-TOKEN-041)) come back in the pull request that builds the profile, not in this one.

### Amendment of 2026-11-18: the core offers DPoP binding only (W4 A)

2026-11-18 (maintainer decision W4 A; [0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/#amendment-of-2026-11-18-the-order-after-the-core-the-shared-blocks-hardening-the-console-and-kv), decision H1). The sender constraint that the core offers a client or an audience is none or DPoP. Mutual-TLS binding (`cnf.x5t#S256`) is not offered in the core. It comes later, with a design for how the client certificate reaches sluis through the Cloudflare edge.

- **Why.** Every production shape sits behind the Cloudflare edge (E1), which ends the client's TLS connection, so `entry` sees a client certificate only if the edge forwards it. That forwarding has no design yet, and DPoP works behind any edge: it is the road "Mutual TLS first" of this record, for the reason it already gives.
- **Where the text says "none, DPoP or mutual TLS"** (Binding is declared per audience, the amendment above and the claims of [0020](/decisions/0020-grant-vocabulary-and-token-claims/#binding)), the core reads "none or DPoP". The profile the core runs is the first plan of "Claims of conformance": DPoP, `private_key_jwt` and PAR. Mutual-TLS binding stays with [#912](https://github.com/truvity/sluis/issues/912) as its later part.
- **The ES256 key "certified like the other token keys"** is certified only when the token certificate is on, which it is not in the core ([0006](/decisions/0006-keys-and-sealing/#amendment-of-2026-11-18-the-token-certificate-is-optional-and-off-in-the-core)).

## Consequences

- A regulated relying party gets the profile it asks for by declaring it, and nobody else pays for it: the default clients keep ES384 and bearer tokens.
- The profile adds an ES256 key per boundary that uses it, certified like the other token keys ([0006](/decisions/0006-keys-and-sealing/#the-token-signing-key)), so a yearly re-signing item per boundary that declares the profile.
- Clients of the profile hold keys, and a lost client key is that client's problem to rotate by a changed client declaration.
- Requirements: [REQ-CLIENT-035](/requirements/client/#REQ-CLIENT-035), [REQ-CLIENT-036](/requirements/client/#REQ-CLIENT-036), [REQ-CLIENT-037](/requirements/client/#REQ-CLIENT-037), [REQ-CLIENT-038](/requirements/client/#REQ-CLIENT-038), [REQ-CLIENT-039](/requirements/client/#REQ-CLIENT-039), [REQ-CLIENT-040](/requirements/client/#REQ-CLIENT-040), [REQ-TOKEN-040](/requirements/token/#REQ-TOKEN-040) and [REQ-TOKEN-041](/requirements/token/#REQ-TOKEN-041) state these rules.

## Roads not taken

- **Sender-constrained tokens for every audience.** AWS web identity, the Kubernetes API server and Argo CD accept bearer tokens only; they would stop working.
- **FAPI 2.0 as an installation-wide mode.** Every client would pay for the strictest profile, and the bearer relying parties would break.
- **ES384 under the profile.** The profile does not allow it, and a plan would fail.
- **Mutual TLS first.** It needs the edge to pass the client certificate to sluis, which a Cloudflare tunnel or a plain gateway may not; DPoP works behind any edge.
- **Message Signing now.** No relying party asks for signed responses, so it would be code without a consumer.
- **A key per client for the signing algorithm.** It is a key per tenant in effect; the key is per boundary.

## Follow-ups

- The profile, PAR, DPoP and `private_key_jwt` support: [#912](https://github.com/truvity/sluis/issues/912).
- The FAPI 2.0 conformance plans: [#929](https://github.com/truvity/sluis/issues/929).
