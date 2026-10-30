# 0048 — Passkeys, step-up and device login

**Status:** Proposed
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
The open point at the end of the decision is tracked in [issue #910](https://github.com/truvity/sluis/issues/910).

## Context

sluis signs people in through an upstream identity provider. Until now it has relied on that provider's multi-factor authentication: a person signs in at the upstream, and sluis takes the result as a fact. That is slow when it happens often, and sluis cannot ask for a fresh second factor on its own terms.

The maintainer wants a fast path that still counts as multi-factor authentication, with a full upstream sign-in from time to time and for high-privilege grants.

The boundary model adds two constraints ([0009](/decisions/0009-one-installation-per-boundary/)).

- **Hosts.** The group issuer is `https://sluis.example.com`. Each environment's issuer is `https://sluis.<env>.example.com`, one origin per host. The boundary hosts are siblings of the group host, not under it.
- **A compromised front must not reach a sensitive boundary.** Each boundary runs its own slim `entry@<boundary>` with no sign-in of its own, and the group issuer is its upstream. If a boundary trusted the front's word for a second factor, a compromised front could act there.

The standards exist: WebAuthn for passkeys, RFC 9470 for a resource to ask for a stronger or fresher sign-in, and RFC 8628 for signing in on a machine with no browser. The standards register lists RFC 8628 as a target in v2 ([#855](https://github.com/truvity/sluis/issues/855)) and passkeys as planned ([#885](https://github.com/truvity/sluis/issues/885)).

## Decision

### A generic WebAuthn relying party

- **sluis runs a generic WebAuthn relying party.** It is reusable for customers later (ADR 0049).
- **Passkeys are the fast sign-in path, and a passkey counts as phishing-resistant multi-factor authentication.**
- **A full upstream sign-in, with the upstream's own multi-factor authentication, is still required** every N days and for high-privilege grants. Policy sets N and the rules.

### One passkey across the sluis hosts

- **The RP ID is the registrable parent domain**, `example.com`, because the boundary hosts are not under the group host. One passkey then works on every sluis host.
- **Every verifier keeps a strict allow-list of origins:** the group host and the boundary hosts, and nothing else. An assertion made on any other origin under the parent domain is refused.
- **Test installations live outside these host names,** so their passkeys never mix with real ones. (the second half is inferred)

### MFA and step-up per client and per environment

- **`policy@<boundary>` decides which clients need multi-factor authentication** in each environment, and how fresh it must be.
- **Step-up follows RFC 9470.** When a token's authentication is too weak or too old for a request, the answer is `insufficient_user_authentication`, with `acr_values` (and `max_age` where freshness is the issue). The client signs in again with what was asked. (`max_age` is inferred from RFC 9470)
- **The upstream's `acr` and `amr` pass through** the generic OIDC upstream. Tokens carry `acr`, `amr` and `auth_time`.
- **`session@<boundary>` keeps the freshness** of each person's authentication for its boundary.

### The boundary checks the passkey itself

- **A boundary that demands fresh MFA verifies the passkey assertion itself,** with its own challenge. It does not take the front's word. A compromised front therefore cannot act in that boundary.
- **Single sign-on is otherwise transparent.** A person taps a passkey only where a boundary's policy demands fresh MFA.
- **The CLI signs in once.** `sluis login` signs in at the group issuer. Tokens for each environment then come by token exchange at that boundary's own token endpoint, without a prompt. The front never sees a boundary's tokens.

### Open point

- **Device login (RFC 8628).** Signing in on a machine with no browser is a target in v2 ([#855](https://github.com/truvity/sluis/issues/855)), and the endpoints on the architecture page are proposed. How it is built is not decided: which actor serves it, how the verification page asks for a passkey or a full sign-in, and whether a tap on the verifying device satisfies a boundary's step-up for the token issued to the device. Tracked in [issue #910](https://github.com/truvity/sluis/issues/910). It keeps this record Proposed.

## Consequences

- Most sign-ins become one passkey tap, and they still count as multi-factor authentication. The upstream sign-in becomes periodic, and for high-privilege grants.
- A person registers one passkey for all sluis hosts, not one per environment.
- Because the RP ID is the parent domain, any page under it could ask a browser for an assertion. The origin allow-list in every verifier is what stops that, so it is a security control and is tested as one.
- A boundary that verifies passkeys keeps its own challenges and the public keys it needs, so a passkey works there even when the front is compromised. (that the boundary keeps the public keys is inferred)
- MFA rules live in each boundary's policy, so production can demand more than development without a change in code.
- The conformance work includes the WebAuthn conformance tool and RFC 9470's challenge format. (inferred)

## Roads not taken

### Rely on the upstream's MFA only

- Pros: nothing to build, and the v1 behaviour.
- Cons: every second factor means a trip to the upstream, and a boundary cannot ask for a fresh one itself.
- Why not: passkeys give a fast path that still counts as MFA, and a boundary can check one itself.

### An RP ID per host

- Pros: each passkey is bound to one host.
- Cons: a person registers a passkey for each environment. (inferred)
- Why not: the maintainer chose one passkey across the sluis hosts, with the parent domain as RP ID and a strict allow-list.

### The front verifies every passkey

- Pros: one verifier, one place to keep public keys. (inferred)
- Cons: a compromised front could claim a fresh second factor in every boundary.
- Why not: a sensitive boundary verifies the assertion itself.

### Passkeys instead of the upstream

- Pros: no upstream sign-in at all. (inferred)
- Cons: sluis would become the provider of record for people's accounts, with recovery and enrolment of its own. (inferred)
- Why not: sluis stays a broker. A full upstream sign-in is still required every N days and for high-privilege grants.

### A fixed interval in code

- Pros: one less policy setting. (inferred)
- Cons: production and development would have to accept the same interval. (inferred)
- Why not: policy sets the interval and the rules.
