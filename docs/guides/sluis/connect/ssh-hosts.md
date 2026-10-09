# Connect SSH hosts with OpenBAO host certificates

Sign each host's key with OpenBAO's SSH CA so clients trust the CA, not every host key. For people and machines, see [SSH](ssh.md).

Create a host signing role, separate from the user roles.

```
bao write ssh/roles/host-devel \
    key_type=ca \
    cert_type=host \
    allowed_domains="build-worker.devel.example,router.devel.example" \
    allow_bare_domains=true \
    allow_subdomains=true \
    ttl=720h \
    max_ttl=720h
```

A host proves itself before it asks for a certificate:

| Host | Proves itself with | Delivered |
|---|---|---|
| cloud VM | AWS auth method | nothing; the instance IAM role is the proof |
| bare metal with a device identity | cert auth | the device certificate, minted once out of band |
| bare metal without one | AppRole | a one-time bootstrap secret, consumed on first boot |

To renew, run the OpenBAO Agent beside `sshd` with auto-auth and a template that writes the certificate and reloads `sshd`. Or run a `systemd` timer well inside the role's `ttl`:

```sh
bao write ssh/sign/host-devel cert_type=host valid_principals=build-worker.devel.example \
    public_key=@/etc/ssh/ssh_host_ed25519_key.pub
```

```
# /etc/ssh/sshd_config
HostKey         /etc/ssh/ssh_host_ed25519_key
HostCertificate /etc/ssh/ssh_host_ed25519_key-cert.pub
```

Clients trust the CA once per domain, with the key from `bao read ssh/config/ca`.

```
# known_hosts
@cert-authority *.devel.example ssh-ed25519 AAAA...
```

`sluisctl ssh known-hosts` writes this line for every configured CA; `sluisctl login` refreshes it ([wrapper reference](../../../reference/sluis/sluisctl-wrappers.md#ssh-known-hosts-trust-configured-ssh-host-cas-before-the-first-connect)).

## Verify

```sh
ssh-keygen -L -f /etc/ssh/ssh_host_ed25519_key-cert.pub
ssh ops@build-worker.devel.example
```

The first prints the certificate's principals and validity. The second connects without a host-key prompt.

Decided in: [ADR 0011](../../../decisions/0011-ssh-people-opkssh-machines-and-hosts-openbao.md), [ADR 0016](../../../decisions/0016-a-managed-known-hosts-file-for-ssh-host-cas.md).
