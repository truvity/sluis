---
title: Approved libraries
description: "The cryptography and parser libraries sluis may use in Go, Rust and TypeScript, the ones it refuses, and how a library is added."
status: target
sidebar:
  order: 6
---

sluis implements no cryptographic primitive and no protocol parser of its own ([REQ-SEC-018](/requirements/sec/#REQ-SEC-018)): they come from approved, maintained libraries ([REQ-SEC-008](/requirements/sec/#REQ-SEC-008)). This page is the list, per language, with the record that needs each capability; a library is approved by the review of the pull request that lists it. The list itself is data, [`docs/reference/approved-libraries.yaml`](https://github.com/truvity/sluis/blob/main/docs/reference/approved-libraries.yaml), so that a CI check can compare it with the dependencies once there is code ([#1039](https://github.com/truvity/sluis/issues/1039)).

The rules behind it are in [ADR 0012](/decisions/0012-cryptography-and-authority-keys/#algorithms) and [ADR 0014](/decisions/0014-the-language-follows-the-executable/#the-rules): only the approved algorithms, a nonce from the library and never from the caller, the operating system's random generator only, no library marked experimental or unaudited for a security function, and no FIPS build in 2.0.

## Go

| Library | For | Needed by |
|---|---|---|
| `crypto/...` (standard library) | ECDSA P-384, AES-256-GCM, SHA-2, HKDF, HMAC, X.509, TLS, and `crypto/rand` | [ADR 0012](/decisions/0012-cryptography-and-authority-keys/#algorithms) |
| `golang.org/x/crypto/ssh` | OpenSSH keys and certificates in Go components and the CLI | [ADR 0008](/decisions/0008-certificate-authorities-for-x509-ssh-and-databases/#ssh) |
| `github.com/go-piv/piv-go/v2` | YubiKey PIV in `key-holder`; attestation verification | [ADR 0014](/decisions/0014-the-language-follows-the-executable/#per-actor-table) |
| `github.com/ProtonMail/go-crypto` | OpenPGP packets: the subkey request, the back-signature, detached signatures | [ADR 0013](/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api/#the-hand-off) |
| `filippo.io/age` | Backup archives and export drops, a named exception | [ADR 0012](/decisions/0012-cryptography-and-authority-keys/#named-exceptions) |
| `github.com/santhosh-tekuri/jsonschema/v6` | Configuration against the JSON Schema | [ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#pkl-and-json-schema) |
| `connectrpc.com/connect`, `google.golang.org/protobuf` | The Connect protocol and Protobuf | [ADR 0002](/decisions/0002-the-design-model-actors-ports-drivers-and-contracts/#contracts) |

Go keeps the standard library's defaults: `GOFIPS140` and `fips140` are unset ([ADR 0012](/decisions/0012-cryptography-and-authority-keys/#algorithms)).

## Rust

| Library | For | Needed by |
|---|---|---|
| `aws-lc-rs` | AES-256-GCM with `RandomizedNonceKey`, ECDSA P-384, HKDF, SHA-2, and its random generator | [ADR 0012](/decisions/0012-cryptography-and-authority-keys/#algorithms) |
| `rustls` | TLS, with the `aws-lc-rs` provider and no FIPS pin | [ADR 0014](/decisions/0014-the-language-follows-the-executable/#the-rules) |
| `cedar-policy` | Cedar policy in `policy` | [ADR 0009](/decisions/0009-policy-is-cedar-in-a-rust-actor/) |
| `jsonschema` | Configuration against the JSON Schema | [ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#pkl-and-json-schema) |
| `connectrpc` | The Connect protocol over Unix sockets, once spike S1 passes | [ADR 0014](/decisions/0014-the-language-follows-the-executable/#the-spikes-are-gates) |
| `zeroize` | Wiping secrets from memory | [ADR 0014](/decisions/0014-the-language-follows-the-executable/#context) |
| The AWS SDK for Rust | AWS calls from the AWS-backed drivers; its SigV4 and checksums are outside any FIPS module | [ADR 0014](/decisions/0014-the-language-follows-the-executable/#the-rules) |

## TypeScript

| Library | For | Needed by |
|---|---|---|
| `ajv`, its draft 2020-12 build | Configuration against the JSON Schema | [ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#pkl-and-json-schema) |
| WebCrypto and WebAuthn, the browser's | Cryptography and passkeys in the console's single-page application | [ADR 0006](/decisions/0006-identity-issuers-audiences-and-sign-in/#passkeys) |
| `@connectrpc/connect` (connect-es) | The Connect client of the public API | [ADR 0017](/decisions/0017-audit-records-the-usage-stream-and-quota/#quota) |

## Refused

| Library | Why | Decided by |
|---|---|---|
| The `yubikey` crate | Its own notice says it is unaudited and experimental (rule R7) | [ADR 0014](/decisions/0014-the-language-follows-the-executable/#roads-not-taken) |
| `ykcs11` through `cryptoki`, for `key-holder` | It resets the card for every client when a second one appears, and keeps a PIN copy that cannot be wiped | [ADR 0014](/decisions/0014-the-language-follows-the-executable/#roads-not-taken) |
| `gpg` on a node | sluis builds the OpenPGP packets itself and the card signs | [ADR 0013](/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api/#the-hand-off) |
| A Go plugin or a WebAssembly module in an actor | Plugins run out of process, always | [ADR 0004](/decisions/0004-drivers-listeners-and-boundary-enforcement/#drivers-are-platform-started-services) |

## Adding a library

A dependency that provides a cryptographic primitive, or parses a protocol or a format that crosses a trust boundary, must be on this list under the language of the executable that links it. To add one:

1. Open a pull request that adds an entry to the data file and a row here: the name, what it is for, and why this library and not one already listed.
2. Say whether it is maintained (releases in the last year, a security policy), whether it has had an audit, and whether it is marked experimental; an experimental library is refused for a security function.
3. Add a security note, as for any change that touches keys or signing ([Review](/contributing/workflow/#review)).

Versions are not pinned here: the lockfiles pin them.

## Open items

:::note[Proposed]
**Libraries not chosen yet.** Each is chosen by the pull request of the component that first needs it, and added here then: JOSE for Go and the verification demos ([#869](https://github.com/truvity/sluis/issues/869)); the embeddable OpenID Connect provider of `session` and `exchange` ([#959](https://github.com/truvity/sluis/issues/959)); the C2SP signed-note, tlog and static-ct-api libraries ([#969](https://github.com/truvity/sluis/issues/969)); the YAML reader of the install file ([#864](https://github.com/truvity/sluis/issues/864)); OpenSSH certificates in Rust for `ssh-ca` ([#925](https://github.com/truvity/sluis/issues/925)). The CI check against home-made primitives and unlisted libraries is built with the first code ([#1039](https://github.com/truvity/sluis/issues/1039)).
:::
