---
title: PostgreSQL
description: How an engineer or a service opens a PostgreSQL session with a client certificate from the sluis ca actor that expires the same day, in place of a password.
status: target
sidebar:
  order: 8
---

PostgreSQL is a **client profile**, not an integration: sluis calls nothing outside for it. The `ca` actor signs a short-lived X.509 client certificate under the boundary's one intermediate, with the **database client profile**, and the database trusts only that intermediate. This page is here because "connect a database" is a use case every operator asks about. The record is [ADR 0008](/decisions/0008-certificate-authorities-for-x509-ssh-and-databases/).

It covers PostgreSQL that you run yourself, including CloudNativePG (CNPG). **IAM authentication on Amazon RDS and Aurora is outside sluis** (below).

## What it's for

- **An engineer opens a database session** to investigate an incident. They run one command; sluis checks their sign-in and groups, signs a certificate for a key generated on their machine, and the session starts. The certificate expires the same day, and nothing needs to be rotated or revoked.
- **A migration job connects** from CI with a certificate minted for the job, valid for minutes, with the database role it is entitled to.
- **A database password is no longer shared.** The common name of the certificate is the identity, and the database maps the name to a role.
- **Access follows the directory.** Leave the group, and the next request is refused; the old certificate dies by itself.

## Integration actors

None. A client profile has no process and no credential of its own. The certificate is signed by the [`ca`](/concepts/actors/) actor, whose key sits in a hardware key or a KMS key. The client generates its own ECDSA P-384 key, so the private key never leaves the machine, and sends only a certificate signing request.

## The certificate's name

| Field | Holds |
|---|---|
| Issuer | The intermediate of the [trust domain](/concepts/machine-identity/#spiffe-ids), one per boundary; the database client profile keeps these certificates apart from every other use ([ADR 0008](/decisions/0008-certificate-authorities-for-x509-ssh-and-databases/#database-client-certificates)) |
| Common name (CN) | The database **role** |
| Organizational unit (OU) | The **person** (their person key) or the **workload** |

The server maps the certificate with `clientname=DN` and a fixed regular-expression map, so PostgreSQL 14 and newer log the full identity, not only the role. Some names are **reserved** in each trust domain and are never issued, such as `streaming_replica` and the owner role. The person and machine flows are the same apart from the proof: a person's sign-in, or a [machine identity](/concepts/machine-identity/).

## What sluis asks the vendor for

Nothing. The operator configures the database to trust the CA and to map a certificate's common name to a database role. PostgreSQL does not check revocation unless a revocation list is configured, so a certificate works until it expires: keep the maximum lifetime short.

On the server, in the configuration (example):

```text
# postgresql.conf
ssl = on
ssl_ca_file = '/etc/postgresql/sluis-client-ca.pem'
# pg_hba.conf: map on the full subject, so the log shows who it was
hostssl orders all 0.0.0.0/0 cert clientname=DN map=sluis
# pg_ident.conf: subject (CN = role, OU = person or workload) to database role
sluis   "/^CN=([a-z_]+),OU=.+$"   \1
```

The CA's public certificate is published by sluis (static publication of CA certificates and revocation lists), so a server can fetch it rather than copy it by hand. The server's own certificate may come from a different CA; name its root in `sslrootcert`.

**CNPG.** The CNPG-specific wiring (the `pg_hba` rules, the maps and the roles) lives with the CNPG deployment, outside sluis. sluis supplies the certificates and the intermediate.

CloudNativePG keeps its built-in per-cluster CA for the replication certificates between the instances of a cluster. The server certificates that applications verify come from cert-manager, with an issuer backed by sluis's X.509 intermediate, and trust-manager distributes the trust bundle to the pods.

## Certificates in pods

A workload in Kubernetes gets its certificate in two steps:

1. **A cert-manager issuer backed by sluis**, first. It gives an identity per namespace, and the key is in a Secret. The issuer has a Helm chart of its own ([Packages](/distribution/packages/#helm-charts)).
2. **The `sluis agent` sidecar**, next, for an identity per pod, with the key kept in the pod.

## RDS and Aurora

IAM database authentication on Amazon RDS and Aurora is **entirely outside sluis**. A small helper, separate from sluis, opens a `psql` session with ordinary AWS credentials, and sluis plugs in only as the source of those credentials, through `credential_process` in the AWS configuration. sluis mints no database credential for RDS or Aurora itself.

## What policy controls

- **Who may ask.** A group per database role: "members of `staging-db-orders-client` may get a client certificate for the `orders` role".
- **What the certificate says.** The subject is the requester's identity unless the role fixes it; the certificate is for client authentication only, and the key usage and extended key usage are fixed by the role, not by the request.
- **The lifetime**, per role, and a maximum that no request exceeds.
- **Reviewed roles.** The certificate roles (names, subject alternative names, lifetimes) are static, reviewed configuration.

## What is audited

- Every certificate issued: the requester, the role, the serial number, the validity and the public key hash. The private key never reaches sluis.
- Every refusal with its reason.

## Setup

An example.

1. Declare the certificate role and the groups that may use it.
2. Install the CA certificate on the database and map the names (above).
3. Give the engineer a committed, secret-free service file, and the command.

```ini
# pg_service.conf, committed; no secret in it
[orders]
host=db.example.org
port=5432
dbname=orders
sslmode=verify-full
```

```text
$ sluis pg --service orders -- psql        # example command shape
```

The command signs in with the cached sign-in, requests a certificate, writes the key, the certificate and the CA to a private directory, and starts `psql` with the standard libpq variables set (`PGSSLCERT`, `PGSSLKEY`, `PGUSER`, `PGSSLMODE=verify-full`, `PGSSLROOTCERT`). It reuses a certificate while it has minutes of life left.

## v1 → v2 notes

- v1 does not sign the certificate itself. OpenBao trusts the issuer on a JWT auth mount and signs the database client certificate; `sluisctl psql` and `sluisctl pg` authenticate to OpenBao and run the tool. See [Connect PostgreSQL with short-lived client certificates](/v1/guides/sluis/connect/postgresql/) and [Connect OpenBao](/v1/guides/sluis/connect/openbao/).
- In v2 the `ca` actor signs the certificate, under the boundary's intermediate, with the database client profile. No secrets manager is needed for this use case, and sluis stays out of secrets storage.
- **PostgreSQL 18's native OAuth does not apply today:** v1 serves no device authorization grant for it, and v2 plans one for the command line later; it is postponed ([#855](https://github.com/truvity/sluis/issues/855)).
- Parity issues: [#824](https://github.com/truvity/sluis/issues/824) certificates, [#829](https://github.com/truvity/sluis/issues/829) the `psql` and `pg` wrappers.

:::note[Proposed]
Beyond certificates, a **PostgreSQL credential minter** that creates a short-lived database role, and other engines after it, is planned and on the [Replace v1 + SSH & DB view](https://github.com/orgs/truvity/projects/9/views/3) of the project board. It would be a `CredentialMinter` integration, with a shared minter test (create, use, expire, sweep). It is not designed in detail, and the command shown above is an example.
:::
