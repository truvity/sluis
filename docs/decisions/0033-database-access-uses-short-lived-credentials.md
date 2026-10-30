# 0033 — Database access uses short-lived credentials

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

People open database sessions to investigate incidents. Migration jobs and services connect to their databases. In most estates this means shared passwords that are rarely rotated. sluis already knows who a person is and what a workload is, and it holds a certificate authority, so it can hand out a credential that expires by itself.

v1 did not sign database certificates itself. A secret store trusted the v1 issuer and signed the client certificate, and a CLI wrapper ran the database tool (the PostgreSQL client profile page has the details). v2 moves the CA into sluis ([0013](/decisions/0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca/)), and OpenBao as a driver is backlog (ADR 0051).

Three facts about PostgreSQL shape the answer.

- **Certificate authentication trusts the CA file.** With the `cert` method, PostgreSQL accepts any certificate that chains to `ssl_ca_file` and maps a name in it to a role. If the database trusted the same CA as workloads, a workload certificate whose common name equals a database role would log in as that role.
- **PostgreSQL checks revocation only if a CRL is configured.** Without one, a certificate works until it expires.
- **PostgreSQL 14 and later can match the whole subject.** With `clientname=DN` the map sees the full distinguished name, and the server logs the full authenticated identity.

Managed databases differ. RDS and Aurora offer IAM authentication: a client makes a token signed with its AWS credentials, valid for 15 minutes, and the database checks it with AWS.

sluis also uses PostgreSQL itself, as a driver: the `postgres` index driver ([0022](/decisions/0022-search-is-an-index-fed-from-the-log-and-the-index-is-one-driver-with-provisioning-modes/)), and possibly a state driver on Kubernetes, which is a question for the Kubernetes records (ADR 0047). Its own actors must be kept apart inside a shared database.

## Decision

### Self-run PostgreSQL and CNPG: client certificates from `ca`

- **`ca@<boundary>` signs short-lived X.509 client certificates** from a **database-client intermediate**, one per trust domain. The trust domain is the boundary ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)). A database trusts only that intermediate, never the workload or host CA.
- **The names follow one scheme:**
  - the common name (CN) is the **database role**;
  - the organisational unit (OU) is the **person or the workload** that asked;
  - the database matches with `clientname=DN` and a **fixed regular-expression map** from the subject to the role. The full subject is then in the database's log, so a session on a shared role is still attributed to a person or a workload.
- **Issuance is bound to the requester.** `ca` fills the OU from the authenticated requester, never from the request. Policy decides which roles a requester may ask for, and the lifetime of each role under a maximum. (the first sentence is inferred from the rule that issuance is bound to the requester)
- **Some names are reserved in each trust domain.** `ca` never issues a certificate for a replication role, such as CNPG's `streaming_replica`, or for the database's owner role.
- **The private key stays on the client.** The client makes its key and sends a CSR. sluis never sees the key.
- **People and machines use the flows they already have.** A person signs in, and the `sluis` CLI requests a certificate for a key on their machine and starts the database tool with the standard libpq variables. A machine presents its workload proof in a token exchange and receives a certificate ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)).
- **In Kubernetes, certificates reach pods in two steps:**
  1. first a **cert-manager issuer backed by sluis**: the identity is the namespace, and the key is in a Secret. It ships as its own chart;
  2. then the **`sluis agent` sidecar**, for an identity per pod. It ships as an image and a snippet.
- **Short lifetimes are the control.** `ca` publishes a CRL, and a database that configures it checks it. The lifetimes and the profiles are ADR 0031.
- **Every certificate is audited and logged.** The audit record has the requester, the role, the serial, the validity and the public key hash. The certificate is in the issuance log of `ca` ([0013](/decisions/0013-ca-and-ssh-ca-are-the-certificate-authorities-and-ssh-uses-only-sluis-own-ca/)).

### The wiring of a particular database stays outside sluis

- **The `pg_hba` lines, the ident maps, the roles and their grants** of a CNPG cluster, or of any database, live in that database's own configuration, in the repository that deploys it.
- **sluis documents the scheme and prints reference snippets.** The database's own configuration applies them. (inferred, from the way sluis prints reference snippets for relying parties)

### RDS and Aurora: IAM authentication, outside sluis

- **sluis does not make RDS or Aurora IAM tokens for people or workloads.** It holds no database credential for them.
- **A small helper outside sluis** makes the token for the database tool from ordinary AWS credentials.
- **sluis plugs in by providing the AWS credentials**: a token exchange into an AWS role, delivered through `credential_process` in the AWS configuration.

### sluis's own use of PostgreSQL

These rules apply wherever a sluis driver keeps its data in PostgreSQL:

- **Each actor kind has a fixed schema.** Access between kinds is by grants.
- **Instances of one kind are kept apart by row-level security,** based on membership of a group role per boundary: `pg_has_role(current_user, 'boundary_' || boundary, 'member')`. Every such table has `FORCE ROW LEVEL SECURITY`.
- **Actors never own tables.** A migration role owns them, so an actor cannot drop or alter a policy.
- **Policies never trust session variables.**
- **Views are `security_invoker`.** There is no `SECURITY DEFINER` function.
- **The tenant column is part of every unique key.**
- **Each role has its own connection pool.**
- **PostgreSQL 15 or later is required.**
- **At L3 and above, each boundary has its own database,** and row-level security stays on.
- **An actor reaches its database with a client certificate from `ca`, with CN = its role, or with IAM authentication on RDS and Aurora.** Here IAM is sluis's own driver signing in with its actor's AWS credentials. It is not a service sluis offers others.

## Consequences

- No database password is shared or rotated by hand. Leaving a group stops the next request, and an old certificate dies by itself.
- A database trusts one intermediate, so no other certificate from `ca` can log in to it.
- The role is named in the certificate and the person or workload beside it, so the map is fixed and the same everywhere. Reserved names keep a certificate from ever becoming a replication or owner session.
- Revocation is weak unless the database configures the CRL. Lifetimes are kept short for that reason.
- The first Kubernetes path, through cert-manager, gives a namespace identity and a key in a Secret, which anyone who can read the Secret can use. Per-pod identity waits for the sidecar.
- RDS and Aurora users need the separate helper, and sluis has nothing to maintain for them beyond AWS credentials.
- sluis's own data in PostgreSQL is isolated by grants and row-level security as well as by its port library, so a bug in one layer does not open another boundary's rows. The cost is a migration role, a group role per boundary and a pool per role.

## Roads not taken

### The database engine of a secret store

- Pros: dynamic roles with leases, and the v1 path through a secret store.
- Cons: a second system that holds an admin credential for every database. OpenBao as a driver is backlog.
- Why not: `ca` signs certificates, and no secret store is needed for this use.

### Static passwords

- Pros: every client supports them.
- Cons: shared, long-lived and rarely rotated. (inferred)
- Why not: the point of this record is credentials that expire by themselves.

### One CA for databases and workloads

- Pros: one intermediate fewer.
- Cons: any workload certificate whose common name equals a database role logs in as that role.
- Why not: a database trusts only the database-client intermediate.

### The person's name as the common name, mapped to a role

- Pros: the v1 style. The person is the certificate's identity. (inferred)
- Cons: the role comes from a map that must list people or patterns, and differs per database. (inferred)
- Why not: CN = role and OU = person keeps one fixed map, and the full subject is logged.

### PostgreSQL 18 OAuth

- Pros: the database checks a token directly.
- Cons: a validator module on every server, and a device grant for the command line, which is not built yet.
- Why not: not now. It can be weighed again once device login exists (ADR 0048).

### sluis makes RDS IAM tokens itself

- Pros: one command for every database. (inferred)
- Cons: sluis would carry database-specific AWS signing for people and workloads that already have AWS credentials. (inferred)
- Why not: the maintainer put it outside sluis. sluis gives AWS credentials, and a helper does the rest.

### CNPG wiring inside sluis

- Pros: sluis could configure the database it issues for. (inferred)
- Cons: sluis would carry one product's configuration, and would need write access to the clusters. (inferred)
- Why not: the wiring belongs to the database's own repository.

### A session variable as the tenant in row-level security

- Pros: one policy for every role, set by the application. (inferred)
- Cons: any code that runs SQL can change the variable, including through a pooled connection. (inferred)
- Why not: policies trust only role membership, which the database itself holds.
