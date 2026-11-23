---
title: PostgreSQL
description: "How people and workloads open PostgreSQL sessions with short-lived client certificates from the ca actor, not with passwords."
status: target
sidebar:
  order: 8
---

PostgreSQL is a **client profile**, not an integration: sluis calls nothing outside for it. It has no adapter, no connection and no credential of a vendor. It belongs to the PKI add-on. The `ca` actor signs a short-lived X.509 client certificate from the boundary's one intermediate, under the **database client profile**, after `policy` sealed a grant for the request, and the database maps the certificate's subject to a role ([0023](/decisions/0023-certificate-authorities/#database-client-certificates)). `ca` holds no key; it asks key@ to sign in place. Only `ca` v0, which issues the token certificate and nothing else, is built; the database client profile is planned ([#926](https://github.com/truvity/sluis/issues/926)).

It covers PostgreSQL that you run yourself, including CloudNativePG (CNPG). **Amazon RDS and Aurora use IAM authentication, outside sluis** (below). SSH, the other certificate path, is on [its own page](/integrations/ssh/).

## What PostgreSQL access is for

- **An engineer opens a database session** to investigate an incident. One command checks their sign-in and groups, gets a certificate for a key made on their machine, and starts the session. Nothing needs rotating.
- **A migration job connects** from CI with a certificate for its workload identity, valid for minutes, for the role it is entitled to.
- **No database password is shared.** The certificate's subject names the role and who asked.
- **Access follows the directory.** Leave the group, and the next request is refused; the old certificate dies by itself.

## Kinds of PostgreSQL integration

None. A client profile has no process and no credential of its own. The certificate is signed by [`ca`](/concepts/families/), whose intermediate key stays on a card or in a KMS key behind key@. The client generates its own P-384 key, which never leaves the machine, and sends a certificate signing request.

## The database client profile

| Field | Holds |
|---|---|
| Issuer | The boundary's intermediate, the same one that issues the boundary's other leaf certificates |
| Common name (CN) | The **database role** |
| Organisational unit (OU) | The **person or workload** that asked, filled by `ca` from the authenticated requester, never from the request |
| Usage | Client authentication only, with a fixed subject form no other profile can produce |

Because one intermediate issues every leaf of the boundary, the separation is in the database's map: the server matches the full subject with `clientname=DN` and a **fixed regular-expression map**, so a certificate of any other profile matches no role, and PostgreSQL 14 and newer log the full subject, not only the role.

- **Reserved roles are never issued**: a replication role (such as CNPG's `streaming_replica`) and the database's owner role.
- **Short lifetimes are the main control.** PostgreSQL checks revocation only when a CRL is configured; `ca` publishes a static CRL, and a database that configures it checks it. The database client profile lasts at most 12 hours, and the CRL is re-signed every 24 hours with a `nextUpdate` of 7 days ([0023](/decisions/0023-certificate-authorities/#leaf-profiles)).
- **Every certificate is in `ca`'s issuance log** and in the audit record ([0023](/decisions/0023-certificate-authorities/#issuance-log-and-revocation)).

## What sluis asks PostgreSQL for

Nothing. The operator configures the database to trust the boundary's intermediate and to map subjects to roles. The wiring of a particular database (`pg_hba` lines, ident maps, roles and grants) lives in that database's own repository; sluis documents the scheme and prints reference snippets like these:

```text
# postgresql.conf
ssl = on
ssl_ca_file  = '/etc/postgresql/sluis-ca.pem'     # the boundary's intermediate and roots
ssl_crl_file = '/etc/postgresql/sluis-ca.crl'     # ca's static CRL, refreshed
# pg_hba.conf: match the full subject, so the log shows who it was
hostssl orders all 0.0.0.0/0 cert clientname=DN map=sluis
# pg_ident.conf: the fixed map; CN = role, OU = person or workload
sluis   "/^CN=([a-z_]+),OU=[^,]+,O=sluis-database$"   \1
```

sluis publishes its CA certificates and CRLs as static documents, so a server fetches them rather than copying by hand. The server's own TLS certificate may come from another CA; clients name its root in `sslrootcert`.

**CNPG** keeps its built-in per-cluster CA for replication between instances. The server certificates applications verify come from cert-manager through the sluis-backed issuer, and trust-manager distributes the trust bundle to the pods.

## Certificates in pods

1. **The cert-manager issuer backed by `ca`**, first: an identity per namespace, with the key in a Secret, readable by anyone who can read the Secret. The issuer has its own chart ([Packages](/distribution/packages/)).
2. **The `sluis agent` sidecar**, next: an identity per pod, with the key kept in the pod.

## RDS and Aurora

IAM database authentication on RDS and Aurora is **outside sluis**. sluis makes no RDS token and holds no database credential. A small helper makes the token from ordinary AWS credentials, which sluis provides through `credential_process` in the AWS configuration ([AWS](/integrations/aws/)).

## What policy controls

- **Who may ask for which role**: a group per database role, such as "members of `staging-db-orders-client` may get a certificate for the `orders` role".
- **The lifetime**, per role, under a maximum no request exceeds.
- **The subject** is always the authenticated requester; a request cannot name someone else.

## What is audited

Every certificate issued (the requester, the profile, the role, the serial, the validity and the public key hash) and every refusal with its reason. The private key never reaches sluis.

## Setup

Names are [illustrative](/integrations/#examples-are-illustrative).

1. Declare the database role and the groups that may ask for it.
2. Install the CA certificates and CRL on the database and add the map (above).
3. Give the engineer a committed, secret-free service file and the command.

```ini
# pg_service.conf, committed; no secret in it
[orders]
host=db.example.org
port=5432
dbname=orders
sslmode=verify-full
```

```text
$ sluis pg --service orders -- psql
```

The command uses the cached sign-in, requests a certificate, writes the key, certificate and CA to a private directory, and starts `psql` with the standard libpq variables (`PGSSLCERT`, `PGSSLKEY`, `PGUSER`, `PGSSLROOTCERT`). A machine presents its workload proof in a token exchange instead.

PostgreSQL 18's native OAuth is not used: it needs a validator module on every server and device login, which is not built yet ([#855](https://github.com/truvity/sluis/issues/855)).

