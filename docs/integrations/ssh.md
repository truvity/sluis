---
title: SSH
description: How people and robots log in to hosts with short-lived SSH certificates from the sluis ca role, and how clients trust a host CA instead of each host key.
status: target
sidebar:
  order: 9
---

SSH is a **client profile** like [PostgreSQL](/integrations/postgresql/): the `ca` role holds an SSH certificate authority key (slot 82 on a YubiKey, an ECC key on KMS), and sluis calls nothing outside.

## What it's for

- **An engineer logs in to a host** with a certificate that expires the same day. `sshd` trusts the CA and the principals in the certificate; there is no `authorized_keys` to edit, and a leaver's access ends when the certificate does.
- **A robot logs in to a host** from CI with a certificate for its job, valid for minutes, with the principal its role allows.
- **A client trusts a host without a first-connection prompt.** The host's key is signed by the host CA, and the client trusts the CA for a domain with one `known_hosts` line.
- **Break-glass access** for administrators is a separate principal with a shorter lifetime and an audit record.

## Integration roles

None; this is a client profile. The roles it uses are the [`ca`](/concepts/roles/) role (signs) and the `policy` role (decides who may have which principals).

## What sluis asks the vendor for

Nothing. The operator sets up `sshd` to trust the user CA and, for hosts, signs the host keys with a host CA:

```text
# /etc/ssh/sshd_config on a host (example)
TrustedUserCAKeys /etc/ssh/sluis_user_ca.pub
HostCertificate   /etc/ssh/ssh_host_ed25519_key-cert.pub
```

```text
# a client's known_hosts, one line per domain (example)
@cert-authority *.example.org ecdsa-sha2-nistp384 AAAA...
```

The CA public keys and the key revocation list are published statically, so a host fetches the CA key from sluis and a client's `known_hosts` line can be written by a command. The certificate format is OpenSSH's own (see [Standards](/standards/)).

## What policy controls

- **Which groups get which principals.** A static, reviewed role per principal set: names, allowed principals, lifetimes. "Members of `staging-ssh-user` may log in as `ops`."
- **Lifetimes**, with a maximum no request exceeds, and whether a certificate may carry extensions such as agent forwarding.
- **Host certificates.** A host proves itself before it asks for one: by the platform's identity on a cloud instance, by a device certificate, or by a one-time bootstrap secret consumed on first boot. Which proof a host class accepts is configuration.

## What is audited

Every certificate: the requester, the principals, the serial number, the validity, and the key fingerprint. Every refusal. The key revocation list publication.

## Setup

An example.

1. Declare the principal roles and the groups in the policy.
2. Distribute the user CA key to hosts (`TrustedUserCAKeys`).
3. For hosts, enrol each host and sign its key; renew with a timer well inside the certificate's lifetime.
4. On the client: `sluis login` once, then:

```text
$ sluis ssh login --role user          # example command shape
$ ssh ops@build.example.org
```

For people who have no `sluis` command on the machine they work from, the alternative is **SSH sign-in with OpenID Connect**: the `opkssh` tool signs a person in at sluis, and the server verifies the ID token. It needs the verifier to accept sluis's signing algorithm, so pin ES256 or RS256 on that client's row, and set a group delimiter if the tool splits on `:`.

## v1 → v2 notes

- **v1 never signs an SSH key.** People log in with `opkssh` against sluis's OpenID provider, and machines and hosts get certificates from OpenBao's SSH CA. See [Connect SSH for people and machines](/v1/guides/sluis/connect/ssh/) and [SSH hosts](/v1/guides/sluis/connect/ssh-hosts/). The v1 commands are `sluisctl bao ssh …` and `sluisctl ssh known-hosts`.
- In v2 the `ca` role is an SSH CA, so sluis signs both user and host certificates. `opkssh` sign-in remains a supported client profile.
- Parity issues: [#825](https://github.com/truvity/sluis/issues/825) `opkssh` sign-in, [#824](https://github.com/truvity/sluis/issues/824) OpenBao certificates, [#829](https://github.com/truvity/sluis/issues/829) the wrappers.

:::note[Proposed]
Post-quantum SSH certificates are not offered: the Go SSH library has no ML-DSA algorithm, so a Go SSH CA cannot issue them without custom code. The command shape `sluis ssh login` is an example.
:::
