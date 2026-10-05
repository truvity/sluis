# Why the policy is shaped this way

The reasoning behind [the policy reference](../reference/policy.md): one file, flat groups, declared clients, and the
few places where a client or an audience may ask for something different. The rule for which groups a token carries has
its own page, [groups in a token](groups-in-a-token.md).

## One file that answers four questions

The policy answers who is in which internal group, what a group adds to a token, how long a token lives, and which
client may be issued one, and no others. Everything that shapes a token is derivable from it by reading it. It
supersedes an earlier flat rules list, which survives only as the matchers inside machine groups.

It is a file in git, and nothing is merged under it at run time. Who is in which internal group is this file and the
directory, so `git log` is the complete history of access to infrastructure. The console reads the policy and shows it,
and writes nothing into it: it removes sessions, keeps connections and Apps, and keeps the records of console and Slack
Connect channels, none of which names an internal group. It cannot attach a directory group to an internal one; that
is an edit in git. A policy that reads correctly and behaves differently is the failure worth catching, which is why
the console's person, group and client pages show what the policy does to a live account.

Some things are deliberately not in it. The **hold window** (how long a signed-in identity keeps its last granted role
while the directory cannot be vouched for) is a property of the service, not a statement about who may do what; it is
the service's `lifetimes.hold`. The clusters whose ServiceAccount tokens count, and the AWS accounts, are where a
signature is checked: deployment configuration, carrying no secret, in the policy document's `exchange` section.

## Why the token is built this way

**`groups` is the whole of the authorization**, as [trust](trust.md) sets out: flat, one string per internal group, never
a structured roles claim beside it. Every relying party binds those strings as they are (a `ClusterRoleBinding`
subject, an ArgoCD `g,` line, a `requires`) and nothing re-maps them. A group's name is therefore the whole of what it
usually adds, and `claims` is for the rare relying party that reads something that is not a group. Where provenance
must travel (which company's directory vouched for this) it goes into the string, as `<workspace id>:access-roster:viewer`,
where every consumer keeps working.

**`sub` is the email.** It is readable in every audit log, needs no second lookup, and is what the service already
keys by. A rename becomes a new `sub` whose old sessions end, which for a controlled directory is acceptable, arguably
correct. A ServiceAccount carries the cluster, so the same namespace and name on two clusters are two subjects. The
earlier *workspace id plus the backend's user id* is retired.

**Matchers sit at the front door.** Attributes exist only in matchers: relying parties are role-based systems, and a
cluster role binding cannot read an attribute. A token describes one account, never a person; linking accounts is a
consumer's concern.

**Conflicts fail the load.** Two groups setting one claim key to different values is refused when the policy loads, so a
bad edit fails a rollout and never produces a token whose shape depends on who is looking. For the same reason lifetime
is a property of the privilege, never of the identity provider: nothing may make it a function of group and workspace,
group and client, or group and person. The answer to an exception is a new internal group, one reviewed line.

**Clients are declared**, one row each, with `requires` mandatory. Nothing creates one in a console or lets a workload
register one, so the set of them is answerable by reading the repository. `redirects` and `signed_out` are two lists
because putting somebody on a redirect URI after signing out begins the login they just ended.

**Teams and channels are consumers.** A GitHub team or a Slack channel is bound to internal groups exactly as a client's
`requires` is, so everything the policy already does (holders from two workspaces, a matcher for the day before a group
exists, the naming convention, the console's holders view) applies to it for free. The table lives in this file for
the reason that decides every question like it: a reader of the access model sees every team's source without opening
another file, and `git log` says who was in what. `people` only links addresses and never says who holds a group.

**An AWS identity is the role**, never the session, function or instance running as it: the minted subject does not
change per invocation. The account is required in the matcher because a role name means nothing without it, and a
pattern there would admit a role of that name in any account.

## Why a resource is not a client

A client's id was the audience because the client and the thing a person reached were one object: sign in to Argo CD,
and the token is for Argo CD. That stops holding when they differ. A Model Context Protocol client is somebody's editor;
what it wants a token for is a service elsewhere. Minting `aud` as the editor's id states something untrue and useless,
and a service pinning `aud` would have to pin the name of every editor that might call.

So a resource is declared, a client names it (RFC 8707), and `aud` is the resource. **The two gates compose**: checking
only the client would let anybody who may use an editor reach every service that editor can name. **Both caps apply and
the shorter wins**, because each was written by somebody saying *not longer than this*. A resource indicator is matched
exactly, because the alternative is somebody comparing two URLs by eye.

**The refusal is the point.** The parameter was once ignored (the library's decoder drops what it does not model), so a
client asking for a token scoped to one service was handed one scoped to itself and told nothing. A token that fails
elsewhere later, for a reason nobody connects to this request, is the expensive version of that mistake. The session
remembers the resource, because a refresh carries a token and nothing else: without it the renewed token would be
minted for the client while the original named a resource, and the resource's own gate would go unchecked for the rest
of the session.

## Why a groups delimiter exists

It is a temporary interop shim for one relying party. opkssh's server-side policy line, `oidc:groups:<value>`, splits
its argument on every `:` and compares only the last segment against a held group, so it can never match a name shaped
`<scope>:<thing>:<role>`, however the value is quoted. `groups_delimiter: "."` mints `devel.ssh.user` for that one
audience, which opkssh reads as one segment. Upstream fixing its parser removes the need.

The load-time checks follow from one worry: a delimiter drawn from the alphabet a name is written in is a
separator-collision mistake. `.` is the documented example because nothing this codebase declares uses it, but that is
not a proof no group name ever will, since a `groups` key need not fit the grammar. So the load does not stop at the
alphabet: it rewrites every concrete group the policy could put in a token and refuses the delimiter if two become the
same string. The guarantee is that it does not collide for the groups this installation has declared, checked when a
row asks for it. [ADR 0015](../decisions/0015-a-per-audience-groups-delimiter-for-opkssh.md);
opkssh's adoption: [0004](../decisions/0004-ssh-opkssh-and-the-secret-stores-ca.md) and
[0011](../decisions/0011-ssh-people-opkssh-machines-and-hosts-openbao.md).

## Why an audience can pin a signing algorithm

OIDC Core 15.1 expects a provider to be able to sign with RS256, and not every relying party keeps up: EKS's associated
OIDC identity provider and Kargo's verifier accept RS256 and nothing else. Rather than pin the whole installation to the
slowest relying party, which is what changing `signingKey.certificate` does, one row asks for it and every other
audience keeps the default (ES384 in the chart). [ADR 0009](../decisions/0009-a-default-signing-algorithm-and-per-audience-exceptions.md)
is the decision, including why it partly supersedes [ADR 0005](../decisions/0005-es384-signing-algorithm.md). The
audience decides rather than the client asking, because the token is verified by the audience: a caller authenticating
as `local-dev` to exchange for `eks-cluster` gets `eks-cluster`'s algorithm. A pin with no key is refused at start,
because otherwise `signing_alg: RS256` on a row nobody minted an RS256 key for would silently keep signing ES384.

## Why a self-described client is proportionate

Every declared client is reviewable, and its `requires` is where *who may obtain a token* is decided. That does not fit
software this installation does not deploy and cannot enumerate: somebody's editor, a hosted assistant. Registration is
not the authorization decision here. Reach is decided by the groups a caller holds, so **a client sluis has never seen
cannot widen anything**: it can only ask a person to consent to the reach that person already has. That is why a
document's `kind`, `ttl_cap` and `requires` are not read.

So the threat is not escalation, it is **phishing**: a hostile client persuading somebody to sign in to it and taking
the token away. That is why the guard is an allow-list of origins rather than a refusal of unknown clients, and why
`requires` is mandatory. A declared client always wins, so nothing is fetched for a client already in the file. The
stale copy window is bounded and transport-only because the copy passed every check when it was fetched, access is
still decided by the person's groups and not by the document, and the window is short. The display name is text chosen
by whoever served it and shown before anybody has authenticated, so it is bounded and stripped, and the audit trail
records the URL, which is the identity.
