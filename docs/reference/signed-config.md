---
title: Signed config
description: "What sluis supplies to signed configuration: who signs, how payloads are read, what a set must satisfy, the declarations, and the test keys."
status: target
sidebar:
  order: 8
---

Config reaches store@ as signed documents, one per config kind, read from a config source and loaded as a whole ([0005](/decisions/0005-the-store/#configuration-loading)). The document format, the layers and the loading rules belong to atoom: the `state` block (planned, step B7 of atoom's [plan](https://github.com/truvity/atoom/blob/master/docs/plan.md); [record 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)), which reads documents through the [source port](https://github.com/truvity/atoom/blob/master/source/docs/source-port.md), and the signed bundle and loading rules of Cedar ([record 0011](https://github.com/truvity/atoom/blob/master/docs/decisions/0011-cedar-as-a-library.md)). Open: the block has no page yet, so store@ and `kits/go/signedconfig` implement the format and share the vectors named [below](#test-keys-and-vectors). This page says what sluis supplies as the host.

## The document

- **One document holds one config kind,** for one boundary, with all the items of the kind. Its header names the kind by the registry name (`KeyPlan`, [Kinds](/reference/kinds/)), and the verifier requires the kind it asked for, so a document signed for one kind cannot be served as another's.
- **The config kinds are the `config` rows of the registry.** The `file` driver reads `<kind>.jws` (the kebab-case name of the kind) from `/etc/sluis/config/<boundary>/` on a host and in the local installation; a Lambda layer carries the same files.
- **The format is the state block's:** a JWS in the general JSON serialisation with a protected header of `alg` (`ES384`), `kid`, `seq` and `kind`, and one or more signatures over the payload. Nothing else is read.
- **The signature proves who published these bytes.** That the commit was reviewed is the branch protection of the repository ([0005](/decisions/0005-the-store/#configuration-loading)).

## Who signs

- **A publisher signs.** A publisher key is held by the publishing pipeline, not by sluis: no actor of sluis holds one, and key@ has no purpose for it. The public keys are pinned in the [bootstrap file](/reference/bootstrap-file/#publishers), and a key is never added by a config document.
- **A regulated party co-signs.** A regulated customer's policy (`RegulatedPartyPolicy`, planned) carries the publisher's signature and the party's over the same payload and `seq`, and store@ requires both ([0007](/decisions/0007-policy-and-sealed-grants/#a-regulated-partys-policy)). Which kinds need a second signature is the `co_signs` list on the party's pinned key, so that config cannot waive it. Neither party can change the document alone.
- **The same keys sign a declaration** ([below](#declarations)).

## The payload

- **The payload is the file as it was reviewed, byte for byte.** A reviewer matches it to `sha384sum` of the file at the commit.
- **The format depends on the kind.** A policy kind (`Policy`, `RegulatedPartyPolicy`) is Cedar text, and `GrantVocabulary` is the vocabulary file itself, which `policy` parses. Every other kind is YAML or JSON read by the strict profile ([Bootstrap file](/reference/bootstrap-file/#format)) into the kind's Protobuf message, with durations written with a unit ([Durations](/reference/bootstrap-file/#durations)).
- **A YAML or JSON payload is a mapping from item id to item.** A singleton kind has one item, with the id `default` (`Boundary`, `KeyPlan`, `TokenLifetime`, `ExchangeTrust`, `RateBound`, `Policy`). A keyed kind uses the name of each item as its id, for example `workforce` for an `Upstream` and `sluis-cli` for an `InstallationClient`.
- **The message is the schema** ([0004](/decisions/0004-the-data-model/#schemas-are-protobuf)). store@ validates the payload against the generated JSON Schema, decodes it, and validates the message with protovalidate. An unknown key is an error that names its path, a retired field is refused, and an error never shows a value. The schemas are committed under `schema/<kind>/v1/` with the `$id` `https://sluis.id/schema/<kind>/v1/<kind>.schema.json`; `GrantVocabulary` and `Policy` have none (a policy is Cedar text and the vocabulary's schema is not generated yet).
- **A schema states the form of a value and is never stricter than the loaders.** A plain scalar is typed by the strict profile before the schema sees it, so an unquoted slot `84` is a number and is refused as one (write `"84"`). The vectors are [`yaml-vectors.json`](https://github.com/truvity/sluis/blob/main/kits/conformance/config/yaml-vectors.json) and [`domain-vectors.json`](https://github.com/truvity/sluis/blob/main/kits/conformance/config/domain-vectors.json).
- **A signed document is at most 3 MiB.** store@ refuses a larger one before it reads the payload, and the Go verifier refuses to buffer more, because a reader that verifies for itself reads from a store@ it does not trust.

## Verifying a document

store@ verifies a document before it parses a byte of the payload, and stops at the first failure; the steps are the state block's. sluis supplies the inputs:

- **The keys are the pinned publishers** of the bootstrap file. A document needs a valid signature of one publisher key that has no `co_signs`, and of every key whose `co_signs` names the kind. A signature that does not verify, or a key that is not in the list, refuses the document.
- **A running store takes only a growing sequence.** A higher `seq` replaces the one in force; the same `seq` with the same digest changes nothing; the same `seq` with another digest, and a lower `seq`, are refused.
- **A store that starts takes the latest validly signed document the source gives it,** whatever its `seq`. Nothing is persisted between starts, so the residual is stated once in [0005](/decisions/0005-the-store/#configuration-loading): a source that serves an older signed document is believed until it serves a newer one, and the audit fact of the start is how it is found.
- **A second verifier checks the same steps in Go.** A reader that must not rely on store@ reads the signed bytes through [`GetSignedConfig`](/reference/store-api/#getsignedconfig) and verifies them itself (`kits/go/signedconfig`). key@ does so for the `KeyPlan` and the `Boundary`, so a compromised store@ cannot choose who receives a data key ([0006](/decisions/0006-keys-and-sealing/#who-may-receive-a-wrap)). The two verifiers are one contract and not shared code, and the vectors hold them to it.
- **Each start and each reload is an audit fact** that names, for each document, its kind, its `seq` and the SHA-384 of its payload ([audit](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md)). A monitor compares them with the head of the reviewed branch.

## The snapshot

store@ loads the documents of its boundary as one set and swaps it atomically; the all-or-nothing mechanics are the block's. sluis decides what the set must satisfy:

1. **Every declared kind has a document.** A boundary declares the config kinds its actors read. A declared kind with no document is a failure, with no default in its place, and a document for an undeclared kind is refused. Planned: store@ cannot detect an undeclared document yet, because the source port has no call that lists the documents of a source.
2. **Each document passes its schema,** then the checks that span kinds and layers:
   - links resolve, the config bounds sit inside the built-in bounds, and a template that a link names exists;
   - every grant name is in the vocabulary ([Policy](/reference/policy/#the-grant-vocabulary));
   - a `Boundary` that turns the token certificate on (`token_certificate: true`) has a `KeyPlan` that lists an ES384 `x509-intermediate` and an ES384 `token-signing` key ([0006](/decisions/0006-keys-and-sealing/#the-token-certificate));
   - every `acr` value the set asks for is one the `Boundary` lists in `acr_values_supported`: the `passkey_acr` of `SignInMethods` and the `acr_values` of each `@step_up` annotation of the `Policy` ([Step-up](/reference/policy/#step-up), [REQ-AUTHN-009](/requirements/authn/#REQ-AUTHN-009)). `signin` ignores an unlisted value, so a set that asks for one would look right and not work. store@ reads the annotations with a small lexer, not a Cedar parser; `policy` is the parser;
   - the `registration_client` of `SignInMethods` names an `InstallationClient` that signs people in (public or confidential), because a passkey registration is admitted for it ([0017](/decisions/0017-identity-and-sign-in/#passkeys)). The refusal is the same whether the client is missing or is an exchange client. The client's `requires` grants decide who may register a passkey, so an operator pins a dedicated registration client.
3. **An installation-wide document has the same content in every boundary's source,** and store@ reads its own boundary's only ([0008](/decisions/0008-boundaries-and-tenants/#installation-wide-settings)).

`sluis config check` runs the same validation offline, as CI does before a document is published, and refuses what store@ refuses; `sluis config explain` shows the effective value of a setting with its provenance ([Commands](/reference/cli/#sluis-config-check-and-explain)). An error names the document and the place, never the value.

## The config sources sluis selects

The `file` driver (a directory, a mounted ConfigMap or a Lambda layer) is built; `appconfig` (an AWS AppConfig profile) is planned. Both are atoom's source drivers; the facts of each, such as how a newer version is signalled, are on atoom's [source port](https://github.com/truvity/atoom/blob/master/source/docs/source-port.md) page. A Lambda layer cannot change under a running function, so a new layer is a new function version, and a config that must change without a deploy comes from AppConfig ([Serverless](/runtimes/serverless/)).

## Declarations

A declaration is a signed document in the same format that is not config: an operator's act that one actor takes once, and not a state that store@ loads. There is one kind, `KeyRevocationDeclaration`, with which an operator revokes keys ([0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/#the-declaration)).

- **The format, the header and the signature checks are a config document's.** The signature is a publisher key's, and the `co_signs` rules apply. The `kind` names the declaration, which is not a kind of the registry, so store@ refuses it as config and a config document is never taken as a declaration (`VerifyDeclaration` in `kits/go/signedconfig`).
- **The sequence admits a retry and nothing older.** key@ takes a declaration with a `seq` above the highest it took, and keeps that `seq` and the digest of the payload in a mark of its own state directory, `keyrevocation.mark`. The same `seq` with the same payload is a retry of the last declaration, which completes it; the same `seq` with another payload, and a lower one, are refused.
- **The payload is YAML under the strict profile,** read into `sluis.key.v2alpha1.KeyRevocationDeclaration`: `boundary`, `kids` (1 to 64 RFC 7638 thumbprints, each once), `reason` (`compromise`, `suspicion`, `lost-board` or `drill`) and, when needed, `allow_unknown: true`, which revokes keys that key@ does not know. `sluis key revoke` writes it:

  ```yaml
  boundary: dev
  kids:
    - NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs
  reason: compromise
  ```

- **key@ refuses** a declaration that does not verify (`declaration`), an old `seq` or another payload under the last one (`seq`), a payload that is not one (`schema`, with the path), another boundary (`other-boundary`), a wrapping key (`wrapping-key`, with the path), and, without `allow_unknown`, a key it does not know (`unknown-kid`). It knows a signing key it holds, a token key that `SigningRing` lists for any node, and a key that a `KeyCertificate` of a signing purpose certifies. A refused declaration writes nothing and does not move the mark. A declaration that a store@ failure stopped half way is refused as `partial` (unavailable), with the keys in the detail, and the same declaration completes it.

## Test keys and vectors

The vectors use two test key pairs whose private halves were discarded after signing, so the keys guard nothing. The first is pinned by the [local example](/reference/bootstrap-file/#the-example-for-local).

| Key | `kid` | `spki_sha384` | `co_signs` |
|---|---|---|---|
| Publisher | `publisher-1` | `d4c36e17a4d13238c8131db63e69bc2a70bd8797357fd39fea5b0d7e9e5d1919163152a3292201de5362906ad9afcbb4` | none |
| Regulated party | `party-1` | `788ce45e1127152dd8ca7bb5d2f2a1bcd9fc208d1bcffd629672c57224827d626b7d863891238bfd67c969e079a6469f` | the kind of the vectors |

```text
-----BEGIN PUBLIC KEY-----
MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAEEUKd6CE58PyZQlFf8Lr7OWUalh4lNPvr
rA7mNmdNt8fOYSPky3w9E9KZmFNWOE+0TJJXt1i3+P5C4nYax8bkHrnS8/lGRv18
eizHflv/jwhvtlX6XUZQsM7aMGQp7ZCQ
-----END PUBLIC KEY-----
```

```text
-----BEGIN PUBLIC KEY-----
MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAENcY8rxK8EKeYUMn0cewM5s+NePDm4Ud/
sHupp7zMuXn59VOyZLHsa/gkMqTwfoXh1TuHkYKdDLGSmWGf5YhED3sVqqfD7cqU
gRKwViO6TrYXCCq+TZVFl0XZTamJNr0E
-----END PUBLIC KEY-----
```

The documents are in [`tests/testdata/signed-config/vectors.json`](https://github.com/truvity/sluis/blob/main/tests/testdata/signed-config/vectors.json): a document signed by the publisher alone, one signed by the publisher and the party, about forty refused variants (a wrong algorithm, a header with an extra member, a changed byte, the other serialisations, a duplicate `kid`) and the sequence cases. store@ (Rust) and `kits/go/signedconfig` (Go) run the same file and must give the same verdict, step for step. [`keyplan-payloads.json`](https://github.com/truvity/sluis/blob/main/tests/testdata/signed-config/keyplan-payloads.json) and [`boundary-payloads.json`](https://github.com/truvity/sluis/blob/main/tests/testdata/signed-config/boundary-payloads.json) do the same for the payloads of a `KeyPlan` and a `Boundary`, which key@ reads from the bytes it verifies. The payload of a vector is a stand-in, because the signature layer does not look into it.
