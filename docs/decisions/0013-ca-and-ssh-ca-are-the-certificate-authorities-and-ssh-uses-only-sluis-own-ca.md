# 0013 — The ca and ssh-ca actors are the X.509 and SSH certificate authorities, and SSH uses only sluis's own CA

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (the open point of [issue #910](https://github.com/truvity/sluis/issues/910) decided: ACME in `ca` comes after the first release; the record is Accepted).
Rewritten in place on 2026-11-01 (the roots and the break-glass CA moved to the separate product [ritueel](https://ritueel.id); the SSH CA keys are anchors; see [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)).

## Context

sluis signs certificates as well as tokens. X.509 certificates serve hosts, workloads, databases and sluis's own mutual TLS. SSH certificates let people and machines log in to hosts, and let clients trust a host's key.

v1 never signed an SSH key (v1 ADR 0004, on `master`). People reached hosts through opkssh, and machines and hosts used the SSH engine of a secret store (v1 ADR 0011, on `master`). Clients trusted host CAs through a managed known-hosts file (v1 ADR 0016, on `master`). That spread SSH over three systems, each with its own trust and its own audit.

The maintainer has decided that v2 uses sluis's own SSH CA only: no opkssh and no SSH from a secret store. This reverses v1's rule that sluis never signs an SSH key, and this record succeeds v1 ADR 0004, the SSH parts of v1 ADR 0011 and v1 ADR 0016. Target hosts run a stock `sshd` and need no hardware key. That leaves the shape of the model open: what a certificate carries, how a host decides who may log in as which account, what happens when sluis is unreachable, and how the host is told.

Four things shape the answer.

- **A certificate that carries authority leaks it.** If a certificate lists every host group its holder may reach, it grows with the organisation, it shows the topology to every host it touches, and OpenSSH caps a certificate at 256 principals.
- **`sshd` decides per account.** `AuthorizedPrincipalsFile` is read for each account at each login, so the host can hold the authority and the certificate only the identity.
- **Not every device can run a service.** Baseboard controllers and KVM appliances run OpenSSH but nothing else of ours. Some routers and switches do not support OpenSSH certificates at all.
- **The keys are already decided.** The SSH user CA and the SSH host CA are separate keys, anchors that hosts learn from a list ritueel publishes, and X.509 keys are separate for each trust domain ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)). How an intermediate is certified is [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/); the certificate profiles are a later record.

## Decision

### Two authorities, two actors

- **`ca@<boundary>` is the X.509 certificate authority. `ssh-ca@<boundary>` is the SSH certificate authority.** They are separate actors because they are different authorities, with different keys, profiles and relying parties. Both are written in Go.
- **Each boundary has its own.** The SSH CAs are per environment, and each company's principals reach a host through the bundle below ([0009](/decisions/0009-one-installation-per-boundary/)). A host trusts exactly one sluis SSH CA, its environment's, plus the break-glass CA.
- **Issuance is bound to the requester.** The subject of a certificate is the authenticated requester: a person's key from their sign-in, a workload from its exchanged proof, a host from its enrolment ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). A request cannot name someone else.
- **`key-holder` signs only what each purpose allows.** The `ca` purpose signs only a parseable TBSCertificate within its constraints. The `ssh-ca` purpose signs only an OpenSSH certificate body.
- **Serial numbers are random and 128 bits long.** Each CA draws a fresh serial at issuance. No ledger is kept.

### X.509

- **`ca` issues from an intermediate per trust domain.** The trust domain is the boundary ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). Its profiles include the host and workload X.509-SVIDs and the database client certificate ([0033](/decisions/0033-database-access-uses-short-lived-credentials/)). A relying party trusts only the intermediate of its own use: a database trusts only the database-client intermediate.
- **Clusters keep cert-manager and trust-manager.** `ca` signs for cert-manager through an external issuer, shipped as its own chart. The issuer accepts a CSR with an empty subject and takes the identity from the requester. (the last sentence is inferred)
- **Every X.509 certificate is logged.** `ca` writes each certificate it issues into a C2SP `static-ct-api` log, which is witnessed like the audit log ([Audit](/concepts/audit/), [Standards](/standards/)). `mtc-tlog` follows with Merkle Tree Certificates, on the post-quantum track (ADR 0052).
- **Revocation backs short lifetimes.** `ca` publishes a static CRL. Short lifetimes are the main control. The revocation cadence is a later record.

### SSH: identity in the certificate, authority on the host

- **A certificate carries identity only.** A person's certificate carries `u:<person-key>`, the opaque and stable person key. A machine's carries `w:<workload>`, named for the workload and not for the instance.
- **The host's `sluis-host` writes the authority.** It writes `/etc/ssh/principals/<account>` from a signed bundle that Cedar policy computed. Each line has `expiry-time` set to the bundle's expiry, and `from=`, `command=` or `no-pty` where policy says so.
- **Grant principals are for three cases only.** `g:<account>@<group>` goes into a certificate only for a host without the service (static mode, below), for sluis's own hosts, which must not depend on sluis to be reached, and for elevation. Elevation is on request, with fresh MFA, for 15 to 60 minutes.
- **Break-glass is `bg:<scope>`, from an offline CA that ritueel holds.** sluis does not make it; the host gets its public key from the list of SSH CA keys that ritueel publishes. Its lines are written at install and never touched by the bundle.
- **Principals carry a kind prefix**, so kinds cannot collide, and one sluis CA is trusted per host.
- **Policy has two actions.** `ssh.login` is standing access, evaluated when the bundle is built, for each person or workload, host and account. `ssh.certify` is evaluated when a certificate is signed. It decides the lifetime, the extensions, the source address and which `g:` grants the certificate carries.
- **Groups come from declared rules** over attested and declared attributes. The console can only narrow them.
- **Attributes keep their provenance** ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). `attested`, `declared` and `assigned` may grant access. `assigned` attributes are set by an operator in the console and audited ([0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/)). `reported` attributes and plain cloud tags may not grant.
- **Host names are declared for pets and derived for cattle,** plus aliases declared on a group. A host certificate carries the canonical name and its short form, the names the platform attests, and the group's aliases. It never carries a name the host reported about itself. sluis refuses an alias that two groups claim. (the alias rules are inferred)

### Accounts and lifetimes

- **Role accounts are the default:** `ops` with sudo, `ops-ro`, and accounts with a forced command. Per-person accounts are opt-in for each group, with stable UIDs. They are locked when a person leaves, never deleted, so a UID is never reused.
- **People never log in as root.** The key ID appears in the `sshd` log, so a login on a shared account is still attributed.
- **Sudo or elevation, by tier.** Appliances and small lab estates get sudo on `ops`. The data tier and tier 0 get elevation only.
- **Lifetimes are set by policy.** The defaults are 12 hours for people, 15 minutes for elevation and 10 minutes for workloads. The key ID is `<installation>/<subject>/<session>`.
- **A stale bundle locks out normal logins.** After 24 hours without a fresh bundle the lines expire. Each group can change the limit. Grant, elevation and break-glass lines are not in the bundle and keep working.
- **Break-glass differs for pets and cattle.** A pet has the offline CA. Cattle are replaced.

### Hosts without the service

- **Static mode** serves OpenSSH devices that cannot run `sluis-host`. The CA, the principals and a long-lived host certificate are written at install, and the revocation list is updated by hand.
- **Pinned host keys** serve devices whose SSH server has no certificate support, such as some routers and switches.
- Both kinds are still hosts in sluis, so policy and inventory stay complete. (inferred)

### `sluis-host`, the SSH host service

- **`sluis-host` is a separate root service** under systemd, written in Rust (the language is confirmed by its spike before code starts). It keeps SSH out of the node agent and out of the main binary, so each stays small.
- **It never listens on the network.** It fetches a signed bundle, verifies it, writes the files atomically and runs a reload hook.
- **It fails static.** When a fetch fails, it keeps the last good bundle in place, and the lines expire as above. It alerts when the revocation list or the host certificate gets old.
- **It ships as signed deb and rpm packages** (`sluis-host`). There is no NixOS module.
- **`sshd` stays stock.** It uses `TrustedUserCAKeys`, `AuthorizedPrincipalsFile`, `HostCertificate` and `RevokedKeys`. It reads the CA keys, the revocation list and the principals at each login. A renewed host certificate needs a SIGHUP.
- **Clients trust a zone's host CA** through a `@cert-authority` line for that zone, which sluis publishes, not one global wildcard. This replaces the per-host known-hosts file of v1. (inferred)
- **Revocation of SSH certificates** is a key revocation list (KRL), by serial and by key, which `sluis-host` keeps current on the host. Standing access ends when the next bundle drops a line, so the KRL is for stolen keys and certificates.

### To verify when it is built

- The cap of 256 principals per certificate.
- That `sshd` logs the certificate's key ID only at `LogLevel VERBOSE`, which `sluis-host` would then set.
- Cedar's datetime support, for time-boxed grants.
- For each file `sshd` uses, whether it is read at each login or needs a reload.

### ACME comes after the first release

- **`ca` serves ACME (RFC 8555) after the first release** ([#978](https://github.com/truvity/sluis/issues/978)). Hosts then get their TLS certificates with an ordinary ACME client, and `sluis-host` stays only the SSH host service. (the ordinary client is inferred)
- **Until then a host gets a TLS certificate by sending a CSR to `ca`'s issue call**, through the enrolment API of [0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/) or the certificate module of `sluis-host`.

## Consequences

- One certificate works on every host of its environment, because it carries one identity and no topology. It never reaches the 256-principal cap and shows nothing about the estate.
- Standing access is revoked by the next bundle, not by waiting for a certificate to expire, so a working-day certificate does not slow revocation.
- Every target host with standing access runs `sluis-host`. A host that cannot is in static mode, and its revocation list and CA rotation are manual.
- An outage of sluis longer than the staleness limit locks out normal logins. Grant, elevation and break-glass access keep working, and each group can change the limit.
- The break-glass CA is a second trust root on the host. It is offline and held by ritueel, which guards and rotates it.
- Two CA actors mean two sets of keys, profiles and conformance tests, and two things to publish: X.509 anchors and CRLs, SSH CA keys and KRLs.
- Every certificate is audited: the requester, the profile, the serial, the validity and the public key hash. Every X.509 certificate is also in the issuance log.
- `sluis config check` refuses a policy that uses `reported` attributes in a permit, and refuses a `g:` grant outside the three allowed cases. (inferred)
- Threats addressed: T-8, T-9, T-19, T-20 and T-26, see the [threat model](/security/threat-model/).

## Roads not taken

### ACME in the first release

- Pros: hosts would use a standard client from the start, and `sluis-host` would never carry a certificate module.
- Cons: one more protocol surface in `ca` before the first release, with its challenges and account state. (inferred)
- Why not: the maintainer put it after the first release. The CSR path covers hosts until then.

### opkssh as a second SSH path

- Pros: no CA key to protect, and people already use it.
- Cons: a binary and policy files on every host, the host must reach the issuer's key set at each login, there are no host certificates, and the audit of logins stays on each host.
- Why not: the maintainer chose sluis's own CA as the only path.

### The SSH engine of a secret store

- Pros: the v1 path for machines and hosts.
- Cons: a second system with its own trust and audit. OpenBao as a driver is backlog (ADR 0051).
- Why not: the CA moved into sluis.

### One `ca` actor for X.509 and SSH

- Pros: one actor fewer.
- Cons: one process would hold two different authorities.
- Why not: they are different authorities, so they are different actors.

### Grant principals in every certificate

- Pros: hosts need only the CA, the revocation list and their groups. It works without a service on the host.
- Cons: certificates grow with the organisation, leak the topology and hit the 256 cap. Granularity stops at groups, and revocation is only by lifetime or revocation list.
- Why not: identity goes in the certificate and authority on the host. Grant principals stay for the three cases above.

### Bare user names as principals

- Pros: familiar, and they work without principals files.
- Cons: any CA in `TrustedUserCAKeys` can mint any name, and kinds can collide.
- Why not: kind prefixes, and one sluis CA per host.

### Principals prefixed by installation

- Pros: safe when a host trusts several installations.
- Cons: long, and redundant with one CA per host.
- Why not: a host trusts one sluis CA.

### Per-person accounts everywhere

- Pros: the best attribution, like a managed login service.
- Cons: UID allocation, account lifecycles and home directories on cattle.
- Why not: role accounts by default, per-person accounts where a group asks.

### Role accounts only

- Pros: the simplest, and friendly to appliances.
- Cons: no personal home or state.
- Why not: some groups need per-person accounts, so they are opt-in.

### Reported attributes and cloud tags may grant

- Pros: no configuration for a cloud fleet.
- Cons: whoever can tag an instance can move it into a policy. This is a known escalation path.
- Why not: only attested, declared and assigned attributes grant.

### Lines that never expire

- Pros: nobody is ever locked out.
- Cons: a person who was removed keeps access on any host that stopped refreshing.
- Why not: a stale bundle locks out normal logins, with a limit per group.

### `AuthorizedPrincipalsCommand` asking a local service at each login

- Pros: expiry is live.
- Cons: a moving part in the login path.
- Why not: the bundle written as files, with `expiry-time` on each line, gives the same bound.

### An emergency certificate from sluis as break-glass

- Pros: audited and in-band.
- Cons: useless when sluis is down, which is when break-glass is needed.
- Why not: break-glass comes from an offline CA held by ritueel. The in-band path is elevation.

### The host service everywhere, including appliances

- Pros: one behaviour for every host.
- Cons: another binary on firmware, and controllers that are fragile under polling.
- Why not: static mode for OpenSSH devices, pinned keys for the rest.

### One policy action with partial evaluation

- Pros: one policy surface.
- Cons: complex, and Cedar's partial evaluation is still maturing.
- Why not: two actions, each evaluated where its data exists.

### SSH inside the node agent or the main binary

- Pros: one fewer service on a host. (inferred)
- Cons: a larger binary and a larger attack surface in a root process.
- Why not: the maintainer wanted a separate host service, kept small.

### A NixOS module for `sluis-host`

- Pros: declarative setup on NixOS. (inferred)
- Cons: another packaging target to keep in step. (inferred)
- Why not: signed deb and rpm packages only.
