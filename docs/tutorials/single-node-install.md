---
title: Install sluis on a single node
description: Install sluis on one arm64 single-board computer with a YubiKey, sign in, verify a token against the JWKS and read the audit trail.
status: target
sidebar:
  order: 1
---

:::caution
This tutorial describes the intended experience. It can't be run yet, because sluis v2 is not released. The commands, outputs and file names are examples of what the finished product shows. Only `sluis doctor`, `sluis config check` and `sluis config explain` are named in the design; the other `sluis` subcommands below are illustrative. Every command is a subcommand of the one `sluis` binary, for operators and for users alike.
:::

You install sluis on one arm64 single-board computer with a YubiKey, the [single-node shape](/shapes/single-node/) (T1). You finish with a running sluis at `https://sluis.example.com`, a token you have verified yourself, and the audit record of your own sign-in.

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
# Example output
pub   nistp384 2026-10-01 [SC]
      3F2A 91C4 7D0E 55B8 A6C1  0E49 7B2D 8E3F 14A0 C9D2
uid           sluis release signing <release@sluis.example.org>
sub   nistp384 2026-10-01 [S] [expires: 2027-04-01]
```

The release identity is a dedicated sluis key. Its primary key is kept offline on a pair of YubiKeys. The subkey that CI signs the repository metadata with expires, and a new one is published under the same primary key. Compare the primary key's fingerprint with the copy attached to the GitHub release, which is a different channel from the distribution site.

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

The package installs the roles, drivers and the node agent (`sluis-node`) under `/usr/libexec/sluis/{roles,drivers,integrations,node,vendor}/<short-name>`, creates one system user per role, and installs one systemd unit per role and driver. It enables nothing yet.

```sh
# Example
ls /usr/libexec/sluis/roles
```

```text
# Example output
audit-read  audit-writer  ca  entry  notary  policy  quota  signer
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

The YubiKey holds the node's keys in PIV slots. It has no symmetric keys, so sealing uses ECDH plus HKDF with a P-384 key in a slot. The plan is:

| Slot | Purpose |
|---|---|
| `9c` | Node intermediate and signing |
| `9d` | ECDH sealing and ring wrap |
| `82` | SSH CA |
| `83` to `95` | Retired sealing keys |

Slot `9e` is avoided. All keys are EC P-384. Change the default PIN and management key first, then let sluis generate the keys on the device so that they never leave it.

```sh
# Example
sluis keys init --device yubikey
```

```text
# Example output
PIN: ********
slot 9c   generated   ECC P-384   on device
slot 9d   generated   ECC P-384   on device
slot 82   generated   ECC P-384   on device
node public keys written to /var/lib/sluis/node/public/
```

:::note[Proposed]
The command that provisions the slots is not yet decided. What is fixed is the slot plan and that the keys are generated on the device.
:::

Store the PIN where only the `key-holder` can read it.

```sh
# Example
install -m 0600 -o root -g root /dev/stdin /etc/sluis/credentials/yubikey-pin
```

Type the PIN and finish with Ctrl-D. The unit passes it to the `key-holder` with `LoadCredential=`. On this board there is no `LoadCredentialEncrypted=`, so the file mode is the protection at rest.

## 6. Write a minimal config

Configuration has four layers: defaults, the shape profile, your install, and the environment. The single-node shape profile already sets the key mode, the state store and the local audit spool, so the install layer holds only what is yours.

```yaml
# Example: /etc/sluis/install.yaml
shape: single-node
issuer: https://sluis.example.com
upstreams:
  - id: workforce
    type: oidc
    issuer: https://idp.example.org
    client_id: sluis-example
    client_secret:
      credential: idp-client-secret   # referenced, never inlined
```

The client secret is an operator secret. The config names a credential, and the secret itself is provided as a file.

```sh
# Example
install -m 0600 /dev/stdin /etc/sluis/credentials/idp-client-secret
```

:::note[Proposed]
The location and the exact key names of the install file are not yet decided. The layers and the rule that secrets are referenced are.
:::

## 7. Check and explain the config

```sh
sluis config check /etc/sluis/install.yaml
```

```text
# Example output
ok    /etc/sluis/install.yaml validates against the schema (unevaluatedProperties: false)
```

The schema is strict: an unknown key is an error, so a typo does not silently do nothing. See what sluis will really use and where each value came from.

```sh
sluis config explain
```

```text
# Example output
shape                  single-node                 install
keys.mode              yubikey                     shape profile
state.driver           single-node store           shape profile
audit.outbox           local spool                 shape profile
audit.index            sqlite                      shape profile
issuer                 https://sluis.example.com   install
upstreams[0].issuer    https://idp.example.org     install
```

## 8. Start the units

Roles and drivers are systemd units. Start the target that groups them.

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
sluis-notary.service        loaded active running  sluis notary
sluis-audit-writer.service  loaded active running  sluis audit-writer
sluis-audit-read.service    loaded active running  sluis audit-read
sluis-quota.service         loaded active running  sluis quota
sluis-entry.service         loaded active running  sluis entry
sluis-node.service          loaded active running  sluis node agent
```

Each unit runs as its own user. One TCP listener exists: `entry`'s public API, bound to `127.0.0.1:8080` (the port is an example). Every other role and driver listens on a Unix socket under `/run/sluis/<role>/`.

```sh
# Example
ss -ltn
ls -l /run/sluis/signer/
```

```text
# Example output
State   Recv-Q  Send-Q  Local Address:Port  Peer Address:Port
LISTEN  0       4096    127.0.0.1:8080      0.0.0.0:*
```

On a single node that is the only TCP listener, and it is bound to the loopback address only.

## 9. Connect cloudflared to the entry listener

`cloudflared` connects to `entry`'s loopback port and makes outbound connections only, so the board opens no inbound port. Install `cloudflared` from Cloudflare's own repository, create the tunnel, and point the ingress at the loopback port. The port, `8080` here, is an example.

```yaml
# Example: /etc/cloudflared/config.yaml
tunnel: <tunnel-id-from-cloudflare>
credentials-file: /etc/cloudflared/credentials.json
ingress:
  - hostname: sluis.example.com
    service: http://127.0.0.1:8080
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

A browser opens at the upstream provider. sluis relies on the provider's multi-factor authentication for now; passkeys are a later roadmap feature. After you sign in, sluis maps the provider's facts to grants through its policy and issues a token. The command prints where it stored the token.

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

Tokens are signed with EC P-384, so the algorithm is `ES384`. The JWKS lists only public keys. The signing key is in the YubiKey and the `signer` can use it only through the `key-holder`.

## 12. Read the audit trail

Every success is audited. Your sign-in produced a record that went to the local spool, then into a sealed batch whose head the `notary` signed.

```sh
# Example
sluis audit tail --last 5
sluis audit verify
```

```text
# Example output
2026-10-28T09:14:02Z  token.issued  sub=you@example.org  role=signer  batch=pending
...
chain ok: 1 batch, head signed by notary key 9c, 0 gaps
```

`audit verify` walks the chain from the sealed batches in the blob store. It never trusts the index or the state store, so a damaged index can't hide a missing record. See [Audit](/concepts/audit/).

## 13. Next steps

- Read what you installed: the [roles](/concepts/roles/), the [drivers](/concepts/drivers/) and the [keys](/concepts/keys/).
- Add a second board and a YubiKey each, and move to the [cluster shape](/shapes/cluster/).
- Understand the packages and how to verify what you installed: [Packages](/distribution/packages/) and [Release signing](/distribution/release-signing/).
- Keep the blob store off the board if you want the audit chain to outlive it; see [Limits and residual risk](/shapes/single-node/#limits-and-residual-risk).
- Check the [open decisions](/open-decisions/) that may change the commands in this tutorial.
