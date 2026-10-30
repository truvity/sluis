---
title: SSH
description: How people and robots log in to hosts with short-lived SSH certificates from the ssh-ca actor, with principals files written by sluis-host from policy.
status: target
sidebar:
  order: 9
---

SSH is a **client profile** like [PostgreSQL](/integrations/postgresql/): the **`ssh-ca` actor** holds two SSH certificate authority keys, one for user certificates and one for host certificates (slots 82 and 83 on a YubiKey, separate ECC keys on KMS), and sluis calls nothing outside. `ssh-ca` is an actor of its own, separate from the X.509 `ca`, because it is a different authority. There is an SSH CA per environment ([Boundaries](/runtimes/boundaries/#the-reference-federation)). The record is [ADR 0013](/decisions/0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca/).

## What it's for

- **An engineer logs in to a host** with a certificate that expires the same day. `sshd` trusts the CA, and the host decides which account the person may use; there is no `authorized_keys` to edit, and a leaver's access ends when the certificate or the bundle does.
- **A robot logs in to a host** from CI with a certificate for its job, valid for minutes.
- **A client trusts a host without a first-connection prompt.** The host's key is signed by the host CA, and the client trusts the CA for a domain with one `known_hosts` line.
- **Break-glass access** for administrators comes from an offline CA, and normal logins never depend on it.

## Integration actors

None; this is a client profile. The actors it uses are [`ssh-ca`](/concepts/actors/) (signs) and `policy` (decides who may log in where), and the host service `sluis-host` (below).

## The certificate carries identity only

A user certificate says **who** is logging in, not **where** they may go (agreed, PM-1 and PM-2):

| Principal | Meaning | Used for |
|---|---|---|
| `u:<person-key>` | A person, by the same opaque person key as `sub` in a token | Every person |
| `w:<workload>` | A machine identity | CI jobs and workloads |
| `g:<account>@<group>` | A grant | Only for hosts without `sluis-host` (a BMC, a KVM over IP), for sluis's own hosts, and for elevation |
| `bg:<scope>` | Break-glass | Signed by an offline CA in a root ceremony, written at install, never touched by the bundle |

Each prefix is a kind, and each host trusts one sluis CA. The key id is `<install>/<subject>/<session>`, and `sshd` logs it.

**Which account a person may use is decided on the host's side.** `sluis-host` writes `/etc/ssh/principals/<account>` from Cedar policy, delivered in a **signed bundle**. Each line carries `expiry-time` equal to the bundle's expiry, and `from=`, `command=` or `no-pty` where the policy says so. Groups come from declared rules over attested and declared host attributes; the console can only narrow them.

## sluis-host

`sluis-host` is a **separate host service**, run as root by systemd, and mainly about SSH. It is small on purpose, to keep the binary and the attack surface small.

- It **never listens on the network.** It fetches the signed bundle, verifies it, writes the files atomically, runs a reload hook and keeps the last good bundle.
- It **fails static**: if sluis is unreachable, the last good bundle stays in force until it expires. It alerts when the revocation list or the host certificate gets old.
- **A stale bundle locks out normal logins** after 24 hours by default, adjustable per group. Break-glass still works.
- It ships in the signed `.deb` and `.rpm` packages, as `sluis-host`. There is no NixOS module.
- `sshd` reads the CA keys, the revocation list and the principals files at each login. Renewing the host certificate needs a `SIGHUP`.
- A host joins with the [machine identity](/concepts/machine-identity/#joining-a-host) flow, and its host certificate follows from that identity.

## What policy controls

- **Two Cedar actions.** `ssh.login` is evaluated when the bundle is built (person × host × account); `ssh.certify` when a certificate is signed.
- **Accounts.** Role accounts by default: `ops` with sudo, `ops-ro`, and forced-command accounts. Per-person accounts are opt-in per group, with stable uids, locked and never deleted. People never log in as root.
- **Elevation.** sudo is the default for appliances and small estates; for the data tier and tier 0, root access is **elevation only**: requested, with fresh MFA, for 15 to 60 minutes, as a `g:` principal.
- **Lifetimes**, by policy. The defaults are 12 hours for people, 15 minutes for elevation and 10 minutes for workloads.
- **Names.** A pet host has a declared name; cattle get a derived name; both may have aliases.
- **Attributes that may grant.** Attested, declared and assigned attributes (assigned on the console, and audited) may grant access; reported attributes and plain cloud tags may not.
- **Break-glass.** For a pet host, the offline CA; for cattle, replace the host.
- **Hosts without the service.** OpenSSH devices that cannot run `sluis-host` use a static mode with `g:` principals; devices with their own SSH (some routers and switches) use pinned keys.

## What sluis asks the vendor for

Nothing. The operator sets up `sshd` to trust the user CA and to read the principals files, and signs the host keys with the host CA:

```text
# /etc/ssh/sshd_config on a host (example)
TrustedUserCAKeys        /etc/ssh/sluis_user_ca.pub
AuthorizedPrincipalsFile /etc/ssh/principals/%u
RevokedKeys              /etc/ssh/sluis_krl
HostCertificate          /etc/ssh/ssh_host_ed25519_key-cert.pub
LogLevel                 VERBOSE
```

```text
# a client's known_hosts, one line per domain (example)
@cert-authority *.example.org ecdsa-sha2-nistp384 AAAA...
```

The CA public keys and the key revocation list are published statically, so a host fetches them from sluis and a client's `known_hosts` line can be written by a command. The certificate format is OpenSSH's own (see [Standards](/standards/)). The roots certify each SSH CA public key with an X.509 certificate, as provenance; `sshd` never checks it ([Keys](/concepts/keys/#the-key-rule)).

## What is audited

Every certificate: the requester, the principals, the serial number, the validity, and the key fingerprint. Every refusal. Every bundle built, and the key revocation list publication.

## Setup

An example.

1. Declare the role accounts, the groups and the `ssh.login` rules in the policy.
2. Install `sluis-host` on each host, and enrol the host ([Machine identity](/concepts/machine-identity/)).
3. Configure `sshd` as above.
4. On the client: `sluis login` once, then:

```text
$ sluis ssh login                      # example command shape
$ ssh ops@build.example.org
```

People get user certificates after they sign in at sluis, if policy allows. Machines get user certificates through their workload identity. sluis's own SSH CA is the only SSH path; target machines need no YubiKey and run stock `sshd`.

:::note[Proposed]
To be verified in the `ssh-ca` record: the cap of 256 principals per certificate, that `sshd` logs the key id only at `LogLevel VERBOSE`, which files `sshd` rereads without a `SIGHUP`, and the date and time support Cedar needs for bundle expiry.
:::

## v1 → v2 notes

- **v1 never signs an SSH key.** People log in with `opkssh` against sluis's OpenID provider, and machines and hosts get certificates from OpenBao's SSH CA. See [Connect SSH for people and machines](/v1/guides/sluis/connect/ssh/) and [SSH hosts](/v1/guides/sluis/connect/ssh-hosts/). The v1 commands are `sluisctl bao ssh …` and `sluisctl ssh known-hosts`.
- In v2 the `ssh-ca` actor is an SSH CA, so sluis signs both user and host certificates. `opkssh` is not carried into v2: the sluis SSH CA is the only SSH path. People get user certificates after sign-in, if policy allows.
- Parity issues: [#825](https://github.com/truvity/sluis/issues/825) v1 `opkssh` sign-in (not carried into v2), [#824](https://github.com/truvity/sluis/issues/824) OpenBao certificates, [#829](https://github.com/truvity/sluis/issues/829) the wrappers.

:::note[Proposed]
Post-quantum SSH certificates are not offered: the Go SSH library has no ML-DSA algorithm, so a Go SSH CA cannot issue them without custom code. The command shape `sluis ssh login` is an example.
:::
