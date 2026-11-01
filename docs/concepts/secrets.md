---
title: Secrets and export
description: "What secrets sluis keeps (its own and its integrations'), the secrets and export ports, sealed state on any runtime, and age-encrypted drops."
status: target
sidebar:
  order: 10
---

sluis holds secrets, because it has to: a GitHub App key that the vendor issued through a manifest flow, a chat token, a credential it minted, a generated client secret. This page says which secrets sluis keeps, which it publishes, and which it refuses to keep.

## The scope

The scope is agreed: **sluis stores its own secrets and its integrations' secrets, and publishes a small, typed set of values.** Two ports carry that:

| Port | What it is for |
|---|---|
| `secrets` | Whole values under addresses: sluis's own secrets and the secrets of its integrations |
| `export` | A small set of typed, versioned documents that something outside sluis is allowed to read |

There is **no general secrets engine**. sluis does not offer arbitrary key-value storage, dynamic secrets, leases as a service or transit as a service to other people. Out of scope: a general-purpose secrets product for others. Where a caller needs a credential for a vendor, sluis **mints a scoped credential** (see [Integrations](/concepts/integrations/)) instead of storing the caller's own secret.

The line between the ports is the one in [Ports and drivers](/concepts/drivers/):

- `keys` holds material that never leaves the device: it signs, wraps and unwraps.
- `state` holds records, with one writer per record kind.
- `secrets` holds values that are read. A listing returns names and never values, and a value is never logged.

## Where a secret comes from

A secret's origin decides who wrote it and whether it is backed up ([Data](/concepts/data/#origins-and-sensitivity)):

| Origin | Example | Lives in | In a backup |
|---|---|---|---|
| declared, by reference | a vendor root token the operator keeps in their own store | the operator's store, read through a source | no; sluis only names it |
| operator-set | a secret put with `sluis secrets put` or pasted on the console | sluis's own store | yes, sealed |
| issued by a vendor | a GitHub App key from the manifest flow, a chat bot token | sluis's own store | yes, sealed |
| issued by sluis | a generated client secret, a minted credential | sluis's own store, or not stored if short-lived | durable ones yes, sealed |

A secret is never in an audit record (a fingerprint and a revision only), in telemetry or in a log line.

## The secrets port

:::note[Proposed]
The interface and the capability flags below are the working design. The scope above is agreed; the details are not.
:::

Interface (illustrative): `get`, `put` with an expected revision, `delete` and `list`, where `list` returns addresses only. A value has revisions, so that a rotated secret can overlap with its predecessor for a grace period.

Drivers are of two kinds, and the port says so in its capability flags:

- **Stores** are writable. An installation has exactly one. Examples are a parameter store on AWS and `sealed-state`.
- **Sources** are read-only. They deliver the secrets that declared configuration references and the bootstrap secrets, such as a YubiKey PIN: a systemd credential, a mounted file, an environment variable (development only).

The bootstrap rule is that **the credentials of the store come from a source, never from the store**.

| Flag | Why it matters |
|---|---|
| `writable` | A vendor-issued secret, or one an operator puts at run time, cannot be written to a source |
| `encryption` | `none`, `backend` or `envelope`. `sluis config check` refuses `none` outside a local installation |
| `versioned` | Overlapping rotation needs at least the previous revision |
| `consumer-readable` and `per-path-grants` | Needed if the store is also where an export is read from |

### Sealed state

A **sealed-state** driver stores values as records in `state`, encrypted under a key that the `keys` port holds. On [boards](/runtimes/installations/#boards) it is the bootstrap, because there is no cloud store to hold vendor-issued secrets. With a YubiKey the sealing is an ECDH and HKDF exchange against the public half of a key in the card's sealing slot (see [Keys](/concepts/keys/#yubikey-layout)). Sealing needs no hardware; only opening does.

Sealed state is **allowed on every runtime when it is done properly** (agreed). This replaces the stricter v1 rule on sealed state. A store the cloud provides is still the usual choice where there is one, but it is no longer the only permitted one.

:::note[Proposed]
A dedicated decision record is still to come. It has to fix:

- the encryption context and the additional authenticated data (AAD);
- a re-seal path, for when the sealing key rotates;
- recovery when a sealing key is lost;
- one immutable item per version, with compare-and-set on the record;
- how sealed values behave in a backup beyond the rule already agreed: they are archived sealed, with an escrow ceremony for the sealing key ([ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/)).

Until it is written, the driver is a design, not a promise. `sealed-state` is for sluis's internal values. Nobody outside sluis can read it, so an exported value on such an installation goes out through a drop (below). The PIN of the card itself comes from a source, a systemd credential.
:::

## The export port

:::note[Proposed]
The port shape is proposed. The age encryption of drops is agreed.
:::

Exports are **typed and versioned**: a document has a kind, a schema and a version, and a breaking change is a new version written beside the old one for a deprecation window. Whether a value may leave sluis is a property of its kind, not an entry on a list of copies.

Four different things can leave sluis, and they go by different channels:

| Class | Example | Channel |
|---|---|---|
| Public material | The key set, CA certificates, trust bundles | Static publication; no secret involved |
| Declared data | Rendered policy, the objects an integration needs at its vendor | Rendered files, applied by the operator |
| Learned data | Directory snapshots, group memberships | An authenticated read with a grant |
| Secrets | A generated client secret, an App key, a minted credential | A drop, or a read from a consumer-readable store |

### Drops are age-encrypted to the consumer

A **file drop** (a file in a directory that a node-local consumer reads) and a **bucket drop** (an object in a bucket) are **age-encrypted to the consumer's own recipient**. The consumer's private key is on its **YubiKey** (an `age-plugin-yubikey` recipient). sluis holds only the **public recipient**.

This has three consequences. A drop in a bucket is safe to leave in a bucket that other people can read. Replacing a consumer's recipient is a configuration change that sluis can make without ever seeing the consumer's key. And a stolen copy of sluis's own state does not open a drop that was already written.

sluis never writes Kubernetes objects. It exports, and clusters pull.

## Copies are not made twice

:::note[Proposed]
This is the proposed rule for the export port; it is not decided.
:::

An exported value is stored once. The consumer is granted the path on its **own** side (its role, its policy), and sluis may render that grant for the operator to apply; sluis never holds the consumer's identity. A scheduled copier that writes to each target was tried in v1 and retired, because every copy existed twice and drifted. A drop is a derived, single-writer artefact of one document, and it is replaced as a whole.
