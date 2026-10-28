---
title: Standards register
description: The RFCs, specifications, standards and certifications sluis uses or plans to, grouped by area, with what each is used for and where it stands in v1 and v2.
status: target
sidebar:
  order: 0
---

This is the register of the standards sluis builds on. Each row says which standard, what sluis uses it for, and where it stands. For the plan to prove conformance, see [Conformance](/standards/conformance/).

## How to read the status

| Status | Meaning |
|---|---|
| **Implemented in v1** | The v1 documentation or code shows it. The v1 reference is linked from the notes where useful ([endpoints](/v1/reference/sluis/endpoints/), [what is not served](/v1/concepts/sluis/not-served/)). |
| **Target in v2** | v2 does it; part of parity, or agreed in the design. |
| **Planned** | Intended, not yet scheduled in detail. |
| **Not planned** | Deliberately not served. v1's list of what is not served is the starting point, and a row can change by a decision. |
| **To verify** | The claim is not yet established from the documentation or code, and a person has to check it before it is relied on. |

A row can carry two statuses, for example "implemented in v1; target in v2". Issue numbers link to [github.com/truvity/sluis/issues](https://github.com/truvity/sluis/issues).

## OAuth 2.0 and OpenID Connect

| Identifier | Title | What sluis uses it for | Status | Notes |
|---|---|---|---|---|
| [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749) | The OAuth 2.0 Authorization Framework | The authorization code and refresh grants and client authentication. | Implemented in v1; target in v2 ([#775](https://github.com/truvity/sluis/issues/775), [#776](https://github.com/truvity/sluis/issues/776)) | A code presented twice is `invalid_grant` and revokes what it issued. Implicit, hybrid and client credentials are not served. |
| [RFC 6750](https://www.rfc-editor.org/rfc/rfc6750) | Bearer Token Usage | Presenting the access token at `userinfo` and at resource servers. | Implemented in v1; target in v2 | A bearer in the URL query is refused. |
| [RFC 7636](https://www.rfc-editor.org/rfc/rfc7636) | Proof Key for Code Exchange (PKCE) | Binding a code to the client that asked. | Implemented in v1; target in v2 | S256 only; `plain` is refused. Required for public clients. |
| [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) | Authorization Server Metadata | Discovery for OAuth clients, including MCP clients. | Implemented in v1; target in v2 ([#774](https://github.com/truvity/sluis/issues/774)) | Served at the path-inserted URL too. |
| [RFC 8628](https://www.rfc-editor.org/rfc/rfc8628) | Device Authorization Grant | A person signs in on a machine with no browser. | Target in v2 ([#855](https://github.com/truvity/sluis/issues/855)) | **Not served in v1**; v1 reasoned that headless CI and workloads use token exchange. New in v2 for people on headless machines. |
| [RFC 8693](https://www.rfc-editor.org/rfc/rfc8693) | Token Exchange | A CI job or a workload trades its platform token for a sluis token, and a person trades a sign-in for another audience. | Implemented in v1; target in v2 ([#782](https://github.com/truvity/sluis/issues/782)) | Access tokens only; an actor token is refused. sluis defines two token types for minting downstream credentials. |
| [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707) | Resource Indicators | A client asks for a token for a declared resource, such as an MCP server. | Implemented in v1; target in v2 ([#791](https://github.com/truvity/sluis/issues/791)) | Resources are matched literally and have their own `requires`. |
| [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728) | Protected Resource Metadata | A resource server advertises sluis as its authorization server. | Implemented in v1; target in v2 ([#822](https://github.com/truvity/sluis/issues/822), [#823](https://github.com/truvity/sluis/issues/823)) | Served by the Go library and the sidecar. |
| [RFC 9207](https://www.rfc-editor.org/rfc/rfc9207) | Issuer Identification | The `iss` parameter in authorization responses. | Implemented in v1; target in v2 | Required by some clients, and by FAPI 2.0. |
| [RFC 7009](https://www.rfc-editor.org/rfc/rfc7009) | Token Revocation | A client revokes its own refresh and access tokens. | Implemented in v1; target in v2 ([#778](https://github.com/truvity/sluis/issues/778)) | A client can revoke only tokens it was issued. |
| [RFC 8252](https://www.rfc-editor.org/rfc/rfc8252) | OAuth 2.0 for Native Apps | The command-line sign-in on a loopback port. | Implemented in v1; target in v2 | A loopback redirect may differ from the registered one in the port only. |
| [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) | Best Current Practice for OAuth 2.0 Security | The security checklist for the authorization server. | To verify | v1 applies parts of it and cites them (PKCE, no token in the URL). A full pass against the checklist has not been done. |
| [RFC 9068](https://www.rfc-editor.org/rfc/rfc9068) | JWT Profile for OAuth 2.0 Access Tokens | The `at+jwt` type and claims for access tokens, so an ID token is never taken for an access token. | Planned; to verify | v1 issues access tokens as JWTs. Whether they meet the `typ` and claim rules of the profile is to verify. |
| [RFC 9126](https://www.rfc-editor.org/rfc/rfc9126) | Pushed Authorization Requests (PAR) | Authorization requests sent by the back channel. | Planned | Not served in v1. A dependency of FAPI 2.0. |
| [RFC 9101](https://www.rfc-editor.org/rfc/rfc9101) | JWT-Secured Authorization Request (JAR) | Signed request objects. | Planned | v1 answers `request_not_supported`. Needed for FAPI 2.0 Message Signing. |
| [RFC 9449](https://www.rfc-editor.org/rfc/rfc9449) | DPoP | Sender-constrained access tokens. | Planned | Not served in v1. A dependency of FAPI 2.0. |
| [OIDC Core §9](https://openid.net/specs/openid-connect-core-1_0.html#ClientAuthentication) | `private_key_jwt` client authentication | Confidential clients authenticate with a signed assertion. | Planned | A dependency of FAPI 2.0. |
| [RFC 7662](https://www.rfc-editor.org/rfc/rfc7662) | Token Introspection | Resource servers that cannot validate JWTs. | Planned (later) | **Not served in v1**: tokens are JWTs verified offline against the key set. A customer-identity deployment may need it. |
| [RFC 7591](https://www.rfc-editor.org/rfc/rfc7591) | Dynamic Client Registration | Registering clients over the wire. | Not planned | Refused in v1: an endpoint that mints trust. [Client ID Metadata Documents](https://datatracker.ietf.org/doc/draft-ietf-oauth-client-id-metadata-document/) (an IETF draft) admit self-describing clients from allow-listed origins instead ([#792](https://github.com/truvity/sluis/issues/792)); implemented in v1. |
| [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523) | JWT Bearer Grant | | Not planned | Token exchange with a different spelling. |
| [OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html) | OpenID Connect Core | Sign-in, ID tokens, UserInfo and the code flow. | Implemented in v1; target in v2 | Query response mode only. Implicit and hybrid flows, the `claims` parameter and request objects are not served. |
| [OpenID Connect Discovery 1.0](https://openid.net/specs/openid-connect-discovery-1_0.html) | Discovery | The configuration document and key set every relying party reads first. | Implemented in v1; target in v2 | Advertises only what is served. |
| [OpenID Connect Dynamic Registration 1.0](https://openid.net/specs/openid-connect-registration-1_0.html) | Dynamic Registration | | Not planned | As RFC 7591. `registration` answers `registration_not_supported`. |
| [OpenID Connect RP-Initiated Logout 1.0](https://openid.net/specs/openid-connect-rpinitiated-1_0.html) | RP-Initiated Logout | `end_session`, with `id_token_hint` and registered post-logout redirects. | Implemented in v1; target in v2 ([#779](https://github.com/truvity/sluis/issues/779)) | Asks the person first unless the hint names the signed-in person. |
| [OpenID Connect Back-Channel Logout 1.0](https://openid.net/specs/openid-connect-backchannel-1_0.html) | Back-Channel Logout | A signed `logout+jwt` to clients that opt in. | Implemented in v1; target in v2 ([#780](https://github.com/truvity/sluis/issues/780)) | Two-minute expiry, no `nonce`. |
| [OpenID Connect Front-Channel Logout 1.0](https://openid.net/specs/openid-connect-frontchannel-1_0.html) | Front-Channel Logout | | Not planned (decision pending) | Never served in v1. Depends on browser behaviour that is weakening; back-channel is the supported path. See [Conformance](/standards/conformance/). |
| [OpenID Connect Session Management 1.0](https://openid.net/specs/openid-connect-session-1_0.html) | Session Management | | Not planned | A browser iframe mechanism. |
| [OAuth 2.0 Form Post Response Mode](https://openid.net/specs/oauth-v2-form-post-response-mode-1_0.html) | Form Post | | Not planned (decision pending) | v1 serves query mode only. |
| [FAPI 2.0 Security Profile](https://openid.net/specs/fapi-security-profile-2_0-final.html) | Financial-grade API 2.0 | A high-assurance profile: PAR, `private_key_jwt`, DPoP, issuer identification. | Planned | Needs the four rows above. See [Conformance](/standards/conformance/). |
| [FAPI 2.0 Message Signing](https://openid.net/specs/fapi-message-signing-2_0-final.html) | FAPI 2.0 Message Signing | Signed requests and responses. | Planned | Builds on the Security Profile and JAR. |
| [CIBA Core 1.0](https://openid.net/specs/openid-client-initiated-backchannel-authentication-core-1_0.html) | Client-Initiated Backchannel Authentication | A service asks for a person's approval on another device. | Not planned (decision pending) | Device login covers part of the need. |

## JOSE

| Identifier | Title | What sluis uses it for | Status | Notes |
|---|---|---|---|---|
| [RFC 7515](https://www.rfc-editor.org/rfc/rfc7515) | JSON Web Signature (JWS) | Every token sluis issues is a JWS. | Implemented in v1; target in v2 | Tokens are standard JWTs, so any language verifies them with a standard library against the JWKS. The v2 JOSE package ([#869](https://github.com/truvity/sluis/issues/869)) sits behind sluis's own package. |
| [RFC 7517](https://www.rfc-editor.org/rfc/rfc7517) | JSON Web Key (JWK) | The published key set. | Implemented in v1; target in v2 | |
| [RFC 7518](https://www.rfc-editor.org/rfc/rfc7518) | JSON Web Algorithms (JWA) | ES384 by default; RS256 for specific audiences. | Implemented in v1; target in v2 | EC P-384 everywhere in v2; RS256 only as the per-audience exception and for the Entra federation key; ES256 for GCP. |
| [RFC 7519](https://www.rfc-editor.org/rfc/rfc7519) | JSON Web Token (JWT) | ID tokens, access tokens, workload tokens. | Implemented in v1; target in v2 | |
| [RFC 7638](https://www.rfc-editor.org/rfc/rfc7638) | JWK Thumbprint | The `kid` of a signing key. | Implemented in v1; target in v2 | |
| [RFC 7516](https://www.rfc-editor.org/rfc/rfc7516) | JSON Web Encryption (JWE) | | Not planned | Tokens are signed, not encrypted. |
| [RFC 8037](https://www.rfc-editor.org/rfc/rfc8037) | CFRG curves in JOSE (EdDSA) | | Not planned | v2 uses EC P-384, not Ed25519, in JOSE. |
| [RFC 8725](https://www.rfc-editor.org/rfc/rfc8725) | JWT Best Current Practices | Algorithm pinning, `typ`, audience checks. | To verify | Not checked against the checklist yet. |
| [RFC 9964](https://www.rfc-editor.org/rfc/rfc9964) | ML-DSA for JOSE and COSE | Post-quantum token signatures (`ML-DSA-65`). | Planned | Needs a JOSE library with ML-DSA and an HSM or KMS that can sign it. The AWS IAM OIDC provider is not documented to accept ML-DSA tokens. |

## PKI and certificates

| Identifier | Title | What sluis uses it for | Status | Notes |
|---|---|---|---|---|
| [RFC 5280](https://www.rfc-editor.org/rfc/rfc5280) | X.509 Public Key Infrastructure Certificate and CRL Profile | The `ca` role's certificates; the public keys in KMS are exported as SubjectPublicKeyInfo. | Target in v2 | v1 has no CA: it relies on OpenBao for certificates. |
| [RFC 5280 §5](https://www.rfc-editor.org/rfc/rfc5280#section-5) | Certificate Revocation Lists | A static, published CRL. | Target in v2 | Short lifetimes are the main control; revocation lists back them. |
| [RFC 6960](https://www.rfc-editor.org/rfc/rfc6960) | OCSP | An online responder. | To verify | Not designed. Whether sluis runs a responder, or relies on short lifetimes and CRLs alone, is not decided. |
| [OpenSSH PROTOCOL.certkeys](https://github.com/openssh/openssh-portable/blob/master/PROTOCOL.certkeys) | SSH certificate format | User and host certificates from the `ca` role. | Target in v2 | v1 does not sign SSH keys. A key revocation list is published statically. |
| [RFC 9881](https://www.rfc-editor.org/rfc/rfc9881) | ML-DSA in X.509 | Post-quantum certificate signatures. | Planned | |
| [RFC 9935](https://www.rfc-editor.org/rfc/rfc9935) | ML-KEM in X.509 | Post-quantum key establishment keys in certificates. | Planned | |
| [RFC 7929](https://www.rfc-editor.org/rfc/rfc7929) | DANE Bindings for OpenPGP (OPENPGPKEY) | Publishing the release key in DNS. | Planned | Backlog research on how users find release keys ([#889](https://github.com/truvity/sluis/issues/889)); needs DNSSEC on the zone. |

## TLS

| Identifier | Title | What sluis uses it for | Status | Notes |
|---|---|---|---|---|
| [RFC 8446](https://www.rfc-editor.org/rfc/rfc8446) | TLS 1.3 | Raft peer traffic over mutual TLS; the edge connector's outbound connection. | Target in v2 | Go's TLS stack. Public TLS terminates at the edge. The minimum version is to verify. |
| [RFC 10024](https://www.rfc-editor.org/rfc/rfc10024) | Post-quantum/traditional hybrid key agreement for TLS 1.3 | `X25519MLKEM768` and the other hybrids. | Planned | Follows what Go's TLS offers; in FIPS mode plain X25519 is not offered but the hybrids are. |

## Signing and supply chain

| Identifier | Title | What sluis uses it for | Status | Notes |
|---|---|---|---|---|
| [RFC 9580](https://www.rfc-editor.org/rfc/rfc9580) | OpenPGP | Signing the apt and dnf repository metadata with the release key. | Target in v2 | The primary key is generated on two YubiKeys, with an expiring CI signing subkey. Whether apt (`gpgv`) and rpm accept ECDSA P-384 signatures, and which key version, is to verify ([#888](https://github.com/truvity/sluis/issues/888)). |
| [Sigstore cosign](https://www.sigstore.dev/) | Keyless signing | Signing `SHA256SUMS` with the build's GitHub identity. | Target in v2 | See [Release signing](/distribution/release-signing/). |
| [SLSA provenance](https://slsa.dev/spec/v1.0/provenance) | SLSA build provenance | GitHub build provenance attestations for each artifact. | Target in v2 | Which SLSA level is claimed is to verify. |
| [in-toto](https://in-toto.io/) | Attestation framework | The statement format of the attestations. | To verify | Provenance attestations use it; whether sluis publishes in-toto layouts of its own is not decided. |

## Crypto and compliance

| Identifier | Title | What sluis uses it for | Status | Notes |
|---|---|---|---|---|
| [FIPS 140-3](https://csrc.nist.gov/pubs/fips/140-3/final) | Security Requirements for Cryptographic Modules | The default build runs Go's FIPS 140-3 module in `fips140=on` mode. | Target in v2 ([#868](https://github.com/truvity/sluis/issues/868)) | A separate build pins the certified module (module v1.0.0, [CMVP certificate 5247](https://csrc.nist.gov/projects/cryptographic-module-validation-program/certificate/5247)). **sluis uses a validated module; sluis itself is not a validated module.** See [Keys](/concepts/keys/). |
| [NIST SP 800-90A](https://csrc.nist.gov/pubs/sp/800/90/a/r1/final), [90B](https://csrc.nist.gov/pubs/sp/800/90/b/final), [90C](https://csrc.nist.gov/pubs/sp/800/90/c/final) | Random bit generation | Go's `crypto/rand` (the OS random source); `sluis doctor` checks the platform. | Target in v2 | SP 800-90C does not require mixing in KMS or YubiKey randomness, so sluis does not. |
| [FIPS 203](https://csrc.nist.gov/pubs/fips/203/final), [FIPS 204](https://csrc.nist.gov/pubs/fips/204/final) | ML-KEM, ML-DSA | The algorithms behind RFC 9964, 9881, 9935 and 10024. | Planned | Go's FIPS module v1.0.0 does not include ML-DSA. |
| [NIST IR 8547](https://csrc.nist.gov/pubs/ir/8547/final) | Transition to Post-Quantum Cryptography Standards | The timeline for the post-quantum plan. | Planned | A planning input: RSA, ECDSA and EdDSA are deprecated after 2030 and disallowed after 2035. |
| [CNSA 2.0](https://media.defense.gov/2022/Sep/07/2003071834/-1/-1/0/CSA_CNSA_2.0_ALGORITHMS_.PDF) | NSA Commercial National Security Algorithm Suite 2.0 | A secondary reference for parameter choices (ML-DSA-87, ML-KEM-1024). | To verify | Read through secondary sources only; the NSA documents were not reachable when the register was written. |

## Other

| Identifier | Title | What sluis uses it for | Status | Notes |
|---|---|---|---|---|
| [W3C WebAuthn](https://www.w3.org/TR/webauthn-3/) | Web Authentication | Passkeys held by sluis, and step-up for high-privilege roles. | Planned ([#885](https://github.com/truvity/sluis/issues/885)) | Until it ships, sluis relies on the upstream provider's MFA. A WebAuthn conformance tool run is part of the plan. |
| [RFC 9180](https://www.rfc-editor.org/rfc/rfc9180) | Hybrid Public Key Encryption (HPKE) | Sealing to a hardware key (ECDH plus HKDF on a P-384 slot). | To verify | The design calls it HPKE-style. Whether it follows RFC 9180 exactly is not decided. |
| [RFC 2945](https://www.rfc-editor.org/rfc/rfc2945), [RFC 5054](https://www.rfc-editor.org/rfc/rfc5054) | SRP-6a | Password sign-in for the recovery and conformance accounts only. | Implemented in v1; target in v2 | The password never crosses the wire. |
| [RFC 7643](https://www.rfc-editor.org/rfc/rfc7643), [7644](https://www.rfc-editor.org/rfc/rfc7644) | SCIM | | Not planned | No issue exists. Provisioners use the vendor's own API. A vendor that offered only SCIM would need a new integration. |
| [RFC 6962 §2.1](https://www.rfc-editor.org/rfc/rfc6962#section-2.1) | Merkle hash trees | The tree over audit records and inclusion proofs. | Implemented in v1; target in v2 | The audit trail's hash chain and notary are in [Audit](/concepts/audit/). |
| [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785) | JSON Canonicalization Scheme | Canonical audit records. | Implemented in v1; target in v2 | |
| [RFC 5905](https://www.rfc-editor.org/rfc/rfc5905) | Network Time Protocol | The audit clock check. | Implemented in v1; target in v2 | |
| [RFC 3161](https://www.rfc-editor.org/rfc/rfc3161) | Time-Stamp Protocol | Anchoring the audit chain head with a trusted time stamp. | To verify | Not built in v1; some compliance profiles ask for it. |

## What is not on this page

Standards that sluis only passes through, such as the protocols of GitHub, Slack or AWS, are not listed: see the [integration pages](/integrations/). The standards that a feature touches are named on that feature's page.

:::note[Proposed]
This register is maintained with the code. A standard moves from **Planned** to **Target in v2** when an issue schedules it, and to **Implemented** when a conformance test passes. A row marked **To verify** is turned into a decision or a fact by the person who checks it.
:::
