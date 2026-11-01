---
title: Install sluis on a single board
description: Install sluis on one arm64 single-board computer with a YubiKey (bare-metal, one node), sign in, verify a token against the JWKS and read the audit trail.
status: target
sidebar:
  order: 1
---

:::caution
This tutorial describes the intended experience. It can't be run yet, because sluis v2 is not released. The commands, outputs and file names are examples of what the finished product shows. Only `sluis doctor`, `sluis config check` and `sluis config explain` are named in the design; the other `sluis` subcommands below are illustrative. Every command is a subcommand of the one `sluis` binary, for operators and for users alike.
:::

You install sluis on one arm64 single-board computer with a YubiKey: the [boards](/runtimes/installations/#boards) installation reduced to one node, on the [bare-metal](/runtimes/bare-metal/) runtime. One board is a possible installation under the same rules as the three-board cluster, not a tested one; the steps are the ones you repeat on each board of the cluster. You finish with a running sluis at `https://sluis.example.com`, a token you have verified yourself, and the audit record of your own sign-in.

## 1. Prerequisites

- An arm64 single-board computer with 2 GB of RAM and eMMC storage, running the vendor's Ubuntu 22.04. This is a declared interim exception; see [Platforms](/distribution/platforms/).
- A YubiKey 5 with firmware 5.7 or newer, which includes the EUCLEAK fix, plugged into the board.
- A domain you control, here `example.org`, on Cloudflare, and a Cloudflare tunnel for `sluis.example.com`. You use the tunnel in step 9.
- An upstream identity provider you can sign in with, here an OIDC provider at `https://idp.example.org`, with a client ID and a client secret you can register a redirect URI for.
- A second machine with a browser and `curl`, `gpg` and Python 3.

Log in to the board and become root for the install steps.

## 2. Add the apt repository

Packages come from `https://distribution.sluis.id/apt/`. The release OpenPGP public key is in `keys/`. Fetch it and check its fingerprint before you trust it.

```sh
# Example
curl -fsSLo sluis-release.asc https://distribution.sluis.id/keys/sluis-release.asc
gpg --show-keys --fingerprint sluis-release.asc
```

```text
# Example output (the first of the release primary keys)
pub   nistp384 2026-10-01 [SC]
      3F2A 91C4 7D0E 55B8 A6C1  0E49 7B2D 8E3F 14A0 C9D2
uid           sluis release signing <release@sluis.example.org>
sub   nistp384 2026-10-01 [S] [expires: 2027-04-01]
```

The release identity is a dedicated sluis identity with two primary keys that ritueel holds. Releases are signed by a subkey that a primary bound; it expires after about 13 months, and a new one ships in a new keyring package. Verify with the keyring, which carries both primaries ([Release signing](/distribution/release-signing/#the-openpgp-release-key)). Compare the primary key's fingerprint with the copy attached to the GitHub release, which is a different channel from the distribution site.

:::note[Proposed]
The channels that publish the fingerprint (keys.openpgp.org, a Web Key Directory on sluis.id, DNS, `SECURITY.md`, the GitHub release and the site) are under research; see [Release signing](/distribution/release-signing/#the-openpgp-release-key). Compare on at least two of them.
:::

Install the key and the repository for the board's architecture.

```sh
# Example
gpg --dearmor < sluis-release.asc > /usr/share/keyrings/sluis.gpg
echo "deb [arch=arm64 signed-by=/usr/share/keyrings/sluis.gpg] https://distribution.sluis.id/apt stable main" \
  > /etc/apt/sources.list.d/sluis.list
apt update
```

`apt` verifies the repository's `InRelease` against the key. If it reports `NO_PUBKEY` or `BADSIG`, stop: the key or the repository is not what you checked.

## 3. Install the package

```sh
# Example
apt install sluis
```

The package installs the actors, drivers and the node agent (`sluis-node`) under `/usr/libexec/sluis/{actors,drivers,integrations,node,vendor}/<short-name>`, creates one system user per actor, and installs one systemd unit per actor and driver. It enables nothing yet.

```sh
# Example
ls /usr/libexec/sluis/actors
```

```text
# Example output
audit-indexer  audit-reader  audit-witness  audit-writer  ca  entry  policy  quota  signer
```

## 4. Run `sluis doctor`

`sluis doctor` checks the platform before you create any key.

```sh
sluis doctor
```

```text
# Example output
ok    architecture        arm64
ok    systemd             249 (units use LoadCredential=; LoadCredentialEncrypted= is not available)
warn  kernel randomness   5.10 < 5.18: VM-clone reseeding is not covered by the kernel
ok    hwrng               absent (deliberate on this SoC)
ok    entropy             initialised before key generation
ok    pcscd               running
ok    yubikey             found, PIV, firmware 5.7.4
ok    filesystem          /var/lib/sluis on eMMC, 28 GB free
```

The two lines about systemd 249 and kernel 5.10 are the board exception, and a warning is not a failure. A `fail` line stops the install: fix it, then run `sluis doctor` again.

## 5. Prepare the YubiKey slots

The YubiKey holds the node's keys in PIV slots. It has no symmetric keys, so sealing uses ECDH plus HKDF with a P-384 key in a slot. Each purpose has its own slot, and each signing purpose has a rotation slot that stays empty until the first yearly renewal. The default map is:

| Purpose | Slot | Rotation slot |
|---|---|---|
| Node intermediate (X.509 CA) | `9c` | `82` |
| Token signing | `9a` | `83` |
| SSH user CA | `84` | `85` |
| SSH host CA | `86` | `87` |
| Release subkey | `88` | `89` |
| Audit log key | `8a` | `8b` |
| Usage log key | `8c` | `8d` |
| Mint grant | `8e` | `8f` |
| ECDH sealing and ring wrap | `9d` | `90` to `95` hold older sealing keys |

The token-signing key is not the intermediate: it has a slot of its own, and the intermediate issues its certificate. This slot map is proposed until `key-holder` is built ([ADR 0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/#the-card-slot-map)). Slot `9e` is not used. All keys are EC P-384. Change the default PIN and management key first, then let sluis generate the keys on the device so that they never leave it.

```sh
# Example
sluis keys init --device yubikey
```

```text
# Example output
PIN: ********
slot 9c   generated   ECC P-384   on device
slot 9a   generated   ECC P-384   on device
slot 84   generated   ECC P-384   on device
slot 86   generated   ECC P-384   on device
slot 88   generated   ECC P-384   on device
slot 8a   generated   ECC P-384   on device
slot 8c   generated   ECC P-384   on device
slot 8e   generated   ECC P-384   on device
slot 9d   generated   ECC P-384   on device
node public keys written to /var/lib/sluis/node/public/
```

:::note[Proposed]
The command that provisions the slots is not yet decided. What is fixed is the slot map and that the keys are generated on the device.
:::

Store the PIN where only the `key-holder` can read it.

```sh
# Example
install -m 0600 -o root -g root /dev/stdin /etc/sluis/credentials/yubikey-pin
```

Type the PIN and finish with Ctrl-D. The unit passes it to the `key-holder` with `LoadCredential=`. On this board there is no `LoadCredentialEncrypted=`, so the file mode is the protection at rest.

## 6. Write a minimal config

Configuration has four layers: defaults, the runtime, your install file, and overrides per environment (development, staging, production). The bare-metal runtime already sets the key mode and the local audit spool. Start from the boards example file and change only what is yours, plus the two drivers that differ with one node.

```yaml
# Example: /etc/sluis/install.yaml
runtime: bare-metal
nodes: 1
state:
  driver: embedded     # one node: no Raft
audit:
  index: sqlite        # one node: no replicated index
issuer: https://sluis.example.com
upstreams:
  - id: workforce
    type: oidc
    issuer: https://idp.example.org
    client_id: sluis-example
    client_secret:
      credential: idp-client-secret   # referenced, never inlined
```

The client secret is referenced from declared configuration: the config names a credential, and the secret itself is provided as a file.

```sh
# Example
install -m 0600 /dev/stdin /etc/sluis/credentials/idp-client-secret
```

:::note[Proposed]
The location and the exact key names of the install file are not yet decided, and the rule that secrets are referenced is agreed. The four layers (defaults, runtime, install file, environment overrides) are proposed; an environment is development, staging or production.
:::

## 7. Check and explain the config

```sh
sluis config check /etc/sluis/install.yaml
```

```text
# Example output
ok    /etc/sluis/install.yaml validates against the schema (unevaluatedProperties: false)
ok    drivers satisfy the runtime and the node count (bare-metal, 1 node)
warn  audit.witness: none; with no witness a compromised writer can get a rewritten checkpoint signed
```

The warning is expected on one node: a witness is optional, and the peer witnesses of the cluster need two or more nodes.

The schema is strict: an unknown key is an error, so a typo does not silently do nothing. See what sluis will really use and where each value came from.

```sh
sluis config explain
```

```text
# Example output
runtime                bare-metal                  install
installation           boards                      install
nodes                  1                           install
keys.mode              yubikey                     installation
state.driver           embedded                    install
audit.transport        spool                       installation
audit.index            sqlite                      install
issuer                 https://sluis.example.com   install
upstreams[0].issuer    https://idp.example.org     install
```

## 8. Start the units

Actors and drivers are systemd units. Start the target that groups them.

```sh
# Example
systemctl enable --now sluis.target
systemctl list-units 'sluis-*' --no-legend
```

```text
# Example output
sluis-key-holder.service    loaded active running  sluis key-holder driver
sluis-state.service         loaded active running  sluis state driver
sluis-signer.service        loaded active running  sluis signer
sluis-policy.service        loaded active running  sluis policy
sluis-ca.service            loaded active running  sluis ca
sluis-audit-writer.service  loaded active running  sluis audit-writer
sluis-audit-indexer.service loaded active running  sluis audit-indexer
sluis-audit-reader.service  loaded active running  sluis audit-reader
sluis-quota.service         loaded active running  sluis quota
sluis-entry.service         loaded active running  sluis entry
sluis-node.service          loaded active running  sluis node agent
```

Each unit runs as its own user. sluis opens no TCP listener here: `entry`'s public API is a Unix socket, and `entry` runs with `PrivateNetwork=yes`, so it has no network at all. Every other actor and driver listens on a Unix socket under `/run/sluis/<actor>/` too.

```sh
# Example
ss -ltn
ls -l /run/sluis/entry/
```

```text
# Example output
State   Recv-Q  Send-Q  Local Address:Port  Peer Address:Port

srw-rw---- 1 sluis-entry-prod sluis-edge 0 Oct 30 09:12 prod-public.sock
```

On a single node with the embedded state driver there is no Raft port either, so `ss` lists no sluis listener at all. The socket's group is the edge connector's, generated from the manifest.

## 9. Connect cloudflared to the entry listener

`cloudflared` forwards to `entry`'s Unix socket as a `unix:` origin and makes outbound connections only, so the board opens no inbound port. Install `cloudflared` from Cloudflare's own repository, create the tunnel, run it in the group of `entry`'s public socket, and point the ingress at the socket. The path follows the naming rule, `/run/sluis/entry/<boundary>-public.sock`; `prod` here is an example.

```yaml
# Example: /etc/cloudflared/config.yaml
tunnel: <tunnel-id-from-cloudflare>
credentials-file: /etc/cloudflared/credentials.json
ingress:
  - hostname: sluis.example.com
    service: unix:/run/sluis/entry/prod-public.sock
  - service: http_status:404
```

```sh
# Example
systemctl enable --now cloudflared
curl -s https://sluis.example.com/.well-known/openid-configuration | head
```

```text
# Example output
{
  "issuer": "https://sluis.example.com",
  "jwks_uri": "https://sluis.example.com/.well-known/jwks.json",
  ...
```

## 10. Sign in

On the other machine, sign in through the upstream provider.

```sh
# Example
sluis login --issuer https://sluis.example.com
```

A browser opens at the upstream provider. sluis relies on the provider's multi-factor authentication for now. Passkeys are planned: they will be the fast path, with a full sign-in at the provider still required every N days and for high-privilege grants. After you sign in, sluis maps the provider's facts to grants through its policy and issues a token. The command prints where it stored the token.

```text
# Example output
Signed in as you@example.org
token stored in ~/.cache/sluis/token.jwt  (expires in 1h)
```

## 11. Verify a token against the JWKS

A sluis token is a standard JWS. Any JOSE library verifies it against the published JWKS, and sluis needs no client library. This example uses Python with PyJWT.

```python
# Example
import jwt  # pip install "pyjwt[crypto]"

token = open("token.jwt").read().strip()
jwks = jwt.PyJWKClient("https://sluis.example.com/.well-known/jwks.json")
key = jwks.get_signing_key_from_jwt(token)
claims = jwt.decode(
    token,
    key.key,
    algorithms=["ES384"],
    issuer="https://sluis.example.com",
    audience="example-service",
)
print(claims["sub"], claims["iss"])
```

ES384 (P-384) is sluis's default, so the algorithm here is `ES384`. A client that needs another algorithm of the FAPI 2.0 set (PS256, ES256 or EdDSA) can get it where the key driver supports it; PS256 on a YubiKey takes about 2.3 s per signature. The JWKS lists only public keys. The signing key is in the YubiKey and the `signer` can use it only through the `key-holder`.

## 12. Read the audit trail

Every success is audited. Your sign-in produced a record that went through the spool transport, then into a sealed batch. The writer asked the `key-holder` to sign the checkpoint, and there is no notary.

```sh
# Example
sluis audit tail --last 5
sluis audit verify
```

```text
# Example output
2026-10-28T09:14:02Z  token.issued  sub=you@example.org  actor=signer  batch=pending
...
chain ok: 1 batch, checkpoint signed by the log key, 0 gaps
```

`audit verify` walks the chain from the sealed batches in the blob store. It never trusts the index or the state store, so a damaged index can't hide a missing record. See [Audit](/concepts/audit/).

## 13. Next steps

- Read what you installed: the [actors](/concepts/actors/), the [drivers](/concepts/drivers/) and the [keys](/concepts/keys/).
- Add a second and a third board, a YubiKey each, and move to the tested [boards installation](/runtimes/installations/#boards).
- Understand the packages and how to verify what you installed: [Packages](/distribution/packages/) and [Release signing](/distribution/release-signing/).
- Keep the blob store off the board if you want the audit chain to outlive it; see [Limits and residual risk](/runtimes/bare-metal/#limits-and-residual-risk).
- Check the [open decisions](/open-decisions/) that may change the commands in this tutorial.
