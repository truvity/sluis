# 0038 — FAPI 2.0 is a profile a client declares, with PAR, DPoP, private-key client authentication and its own signing algorithm, and sender constraint is declared per audience

**Status:** Accepted
**Date:** 2026-11-18

Proposal: [issue #1025](https://github.com/truvity/sluis/issues/1025).

## Context

Some relying parties of an installation, a regulated party's among them, need the FAPI 2.0 Security Profile: authorization by pushed request, tokens bound to a key, clients that authenticate with a key, the issuer in the response, and algorithms the profile allows. The profile is part of the core issuer's security hardening. sluis hosts no sign-in for a customer's own people, so the profile here is the issuer's, for the clients that declare it. The work is split over [#912](https://github.com/truvity/sluis/issues/912) (the features) and [#929](https://github.com/truvity/sluis/issues/929) (the conformance plans).

Two statements disagree on binding. Threat T-1 says tokens "are sender-constrained". [0017](/decisions/0017-identity-and-sign-in/) and [0020](/decisions/0020-grant-vocabulary-and-token-claims/) say an audience that declares no binding gets a bearer token, because AWS web identity, the Kubernetes API server and Argo CD accept bearer JWTs only. FAPI 2.0 also does not allow ES384, which is sluis's default algorithm ([0006](/decisions/0006-keys-and-sealing/)); [#912](https://github.com/truvity/sluis/issues/912) lists PS256, ES256 and EdDSA as the ones to support.

## Decision

The profile is planned. Nothing of it is built yet: the token endpoint accepts only the client authentication methods of RFC 6749, and the authorization endpoint refuses `dpop_jkt`. The rules below are the design the code follows. See the [roadmap](/roadmap/).

### Binding is declared per audience

- **Each audience declares its binding: none or DPoP.** None gives a bearer token without `cnf`, the default. DPoP gives a token with `cnf.jkt`. A DPoP proof sent by a client whose profile does not use DPoP is refused.
- **The core offers DPoP binding only.** Binding by mutual TLS (`cnf.x5t#S256`) is not offered: every production shape sits behind an edge that ends the client's TLS connection, so `entry` sees a client certificate only if the edge forwards it, and that forwarding has no design yet ([#1903](https://github.com/truvity/sluis/issues/1903)). `sluis config check` refuses a client or an audience that declares it (REQ-TOKEN-057). DPoP works behind any edge.
- **T-1 is read that way.** A token is sender-constrained where its audience declares a binding. A bearer audience has the token's short lifetime as its only bound, and the threat model says so.

### The profile

A client or an audience may declare `profile: fapi2`. An installation client declares it in config. A tenant client declares it only when its client template allows the profile ([0021](/decisions/0021-clients/)). Then `sluis config check` requires, and the token and authorization endpoints enforce:

- **Pushed authorization requests (RFC 9126).** The authorization endpoint accepts only a `request_uri` from a PAR call; a `request_uri` is single-use and short-lived. A "PAR required" switch exists for the client and for the installation.
- **PKCE with S256, the authorization code flow only,** exact redirect URI matching, and the `iss` response parameter of RFC 9207.
- **Client authentication by `private_key_jwt`,** never a client secret. The client's public key is in its client declaration; sluis holds no key of the client. A client assertion must name the client as `iss` and `sub` and the issuer as `aud`, live at most 60 seconds, and carry a `jti` that is accepted once.
- **A sender-constrained access token: DPoP,** with the server-provided nonce the profile requires.
- **Its own signing algorithm: ES256 first.** The profile's audience signs with an ES256 key of the boundary, a dedicated key beside the ES384 token key, in the key set under its own `kid`. `signer` signs with it through key@, and on a board it has slot `8c` of the default card slot map. PS256 and EdDSA are added when a client needs them and the key store offers them. The default stays ES384 (REQ-TOKEN-019). Algorithm keys are per boundary, never per tenant ([0006](/decisions/0006-keys-and-sealing/)). The key carries a certificate only when the token certificate is on, which it is not in the core.
- **Signed request objects (RFC 9101) are accepted** from a client of the profile that sends them. Response signing and signed introspection, the parts of FAPI 2.0 Message Signing, are built with the first relying party that requires them; until then discovery does not advertise them and a request object is refused.
- **Details the plan checks,** such as the exact nonce behaviour, are implemented as the pinned revision of the plan needs and recorded in the standards register.

### The profile is a choice of the client

A relying party that cannot do PAR or DPoP is not required to. The default clients keep ES384 and bearer tokens.

### Claims of conformance

sluis claims a FAPI 2.0 plan only when it passes ([Conformance](/standards/conformance/)). The first plan is the FAPI 2.0 Security Profile OP plan for the variant above (DPoP, `private_key_jwt`, PAR); Message Signing follows with its relying party. sluis is not strong customer authentication.

The OpenID Foundation conformance suite runs in CI today with the Basic and Form Post plans, as a public client, against the local installation (`.github/workflows/conformance.yaml`). The FAPI 2.0 plan is planned to run beside them, against the local installation first. It does not run yet.

## Consequences

- A regulated relying party gets the profile it asks for by declaring it, and nobody else pays for it: the default clients keep ES384 and bearer tokens.
- The profile adds an ES256 key per boundary that uses it. With the token certificate off, the key has no certificate to re-sign each year.
- Clients of the profile hold keys, and a lost client key is that client's problem to rotate by a changed client declaration.
- Requirements: REQ-AUTHN-113 to REQ-AUTHN-115, REQ-CLIENT-052 to REQ-CLIENT-059, REQ-TOKEN-052 to REQ-TOKEN-057, REQ-ISO-122 and REQ-TEST-019 state these rules; REQ-CLIENT-059 holds mutual-TLS client authentication until the edge forwards the certificate.

## Roads not taken

- **Sender-constrained tokens for every audience.** AWS web identity, the Kubernetes API server and Argo CD accept bearer tokens only; they would stop working.
- **FAPI 2.0 as an installation-wide mode.** Every client would pay for the strictest profile, and the bearer relying parties would break.
- **ES384 under the profile.** The profile does not allow it, and a plan would fail.
- **Mutual TLS first.** It needs the edge to pass the client certificate to sluis, which a tunnel or a plain gateway may not; DPoP works behind any edge.
- **Message Signing now.** No relying party asks for signed responses, so it would be code without a consumer.
- **A key per client for the signing algorithm.** It is a key per tenant in effect; the key is per boundary.

## Follow-ups

- The profile, PAR, DPoP and `private_key_jwt` support: [#912](https://github.com/truvity/sluis/issues/912).
- The FAPI 2.0 conformance plans in CI: [#929](https://github.com/truvity/sluis/issues/929).
- Open: how the client certificate reaches sluis through the edge, so that binding by mutual TLS can be offered: [#1903](https://github.com/truvity/sluis/issues/1903).
