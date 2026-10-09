# Why is the policy shaped this way?

This page explains [the policy reference](../../reference/sluis/policy.md): one file, flat groups, declared clients and
a few per-client exceptions. Which groups a token carries is in [groups in a token](groups-in-a-token.md).

## What does the one file answer?

The policy answers four questions. Who is in which internal group? What does a group add to a token? How long does a
token live? Which client may be issued one? It is a file in git, and nothing is merged under it at run time.

`git log` is the complete history of access. The console shows the policy and writes nothing into it. It cannot attach a
directory group to an internal group.

Two things stay outside the file. The hold window is the service's `lifetimes.hold`. The clusters whose ServiceAccount
tokens count, and the AWS accounts, are deployment configuration in the policy document's `exchange` section.

## Why the token is built this way

`groups` carries all of the authorization, as [trust](trust.md) sets out. It is flat, one string per internal group, with
no roles claim beside it. Relying parties bind those strings as they are. `claims` is for a relying party that reads
something that is not a group. When provenance must travel, put it in the string as a workspace id prefix.

`sub` is the email. A rename becomes a new `sub` whose old sessions end. A ServiceAccount carries its cluster. An AWS
identity is the role, never the session, and the matcher requires the account.

Attributes exist only in matchers, because a cluster role binding cannot read one.

Two groups that set one claim key to different values fail the policy load. Lifetime is a property of the privilege,
never a function of group and workspace, group and client, or group and person. For an exception, add a new internal group.

A client is declared in one row with `requires` mandatory. No console or workload registers one. A GitHub team or Slack
channel binds to internal groups the way `requires` does.

## Why a resource is not a client

A client's id used to be the audience, because the client and the thing reached were one object. A Model Context
Protocol client is somebody's editor, and the token is for a service elsewhere. So you declare a resource, a client names
it (RFC 8707), and `aud` is the resource.

Both gates apply, and the shorter cap wins. Checking only the client would let anyone who may use an editor reach every
service it can name. A resource indicator matches as written.

A resource the policy does not know is refused, never ignored. The session remembers the resource, because a refresh
carries a token and nothing else. Without it the renewed token would name the client and skip the resource's own gate.

## Why a groups delimiter exists

It is an interop shim for one relying party. opkssh splits `oidc:groups:<value>` on every `:` and compares the last
segment. A name shaped `<scope>:<thing>:<role>` never matches.

`groups_delimiter: "."` mints `devel.ssh.user` for that one audience. The load rewrites every concrete group the policy
could put in a token and refuses the delimiter if two become the same string. The check covers the groups this
installation declares, when a row asks for it. See [ADR 0015](../../decisions/0015-a-per-audience-groups-delimiter-for-opkssh.md).

## Why an audience can pin a signing algorithm

The chart default is ES384. EKS's OIDC identity provider and Kargo's verifier accept RS256 only. One row pins RS256 for
that audience and every other audience keeps the default.

The audience decides, because the audience verifies the token. A caller authenticating as `local-dev` to exchange for
`eks-cluster` gets `eks-cluster`'s algorithm. A pin with no matching key is refused at start, because otherwise the
issuer would keep signing ES384.

## Why a self-described client is proportionate

Registration is not the authorization decision. Reach is decided by the groups a caller holds. A client sluis has never
seen cannot widen anything: it can only ask a person to consent to reach that person already has. The document's
`kind`, `ttl_cap` and `requires` are not read.

The threat is phishing: a hostile client persuading somebody to sign in and taking the token away. The guard is an
allow-list of origins. A declared client always wins, so nothing is fetched for one already in the file.

The cached copy is short-lived and transport-only. The display name is bounded and stripped. The audit trail records
the URL, which is the identity.

## Decided in

- [ADR 0009](../../decisions/0009-a-default-signing-algorithm-and-per-audience-exceptions.md): a default signing algorithm and per-audience exceptions
- [ADR 0015](../../decisions/0015-a-per-audience-groups-delimiter-for-opkssh.md): a per-audience groups delimiter for opkssh
- [ADR 0004](../../decisions/0004-ssh-opkssh-and-the-secret-stores-ca.md): opkssh and the secret store's CA
- [ADR 0011](../../decisions/0011-ssh-people-opkssh-machines-and-hosts-openbao.md): SSH for people, machines and hosts
