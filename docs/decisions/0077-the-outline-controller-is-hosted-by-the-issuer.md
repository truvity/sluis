# 0077 — The Outline controller keeps an Outline wiki's groups, roles and collections, hosted by the issuer

**Status:** Proposed. Applies [0017](0017-the-slack-reconciler-membership-only.md) (a reconciler where a claim cannot
be read), [0018](0018-do-not-configure-what-the-product-knows.md) (the product's facts are not policy) and
[0024](0024-reconciler-rails-are-shared-pieces-not-a-framework.md) (the shared rails); uses the allowance of
[0071](0071-least-privilege-modules-one-credential-holder-per-function.md) point 5 for a provider with no function of
its own.
**Date:** 2026-10-24

## Context

A self-hosted Outline wiki (Community edition) signs people in through sluis by OIDC, and creates an account at a
person's first sign-in. Its own group sync is a paid feature that reads Google's or Microsoft's API, never an OIDC
claim. So nothing in Outline follows the policy: who is an admin, which groups exist, who is in them and which
collections they open are set by hand. An installation that runs one wiki for several companies needs a company's
collections closed to the others, and wants that written in git beside the GitHub and Slack bindings.

These facts were read in Outline v1.12.0's source (`server/routes/api`, `server/policies`) on 2026-10-24. A later
release can change them; the controller's tests pin the behaviour it relies on.

- **API keys** belong to a user and act with that user's role, checked on every call. Outline has no service
  account. A suspended user's key is refused.
- **Lists** page by `offset` and `limit`. A limit over 100 is refused, not clamped.
- **Admins** list every account, suspended ones too, and every collection except other people's personal ones (with
  `includeListOnly`). An admin may suspend another admin but not itself, and may not change its own role. The last
  admin cannot be demoted.
- **Group names** are unique without regard to case. `groups.add_user` without a permission resets a group admin to
  member.
- **`events.list`** cannot list group changes, and its audit log is refused on the Community plan. A document's shares
  are read with `documents.memberships`, which answers only someone who may edit the document.
- **`integrations.list`** shows a Slack post integration's channel name and never its Slack workspace.
- **The rate limiter** is on by default: 1000 calls a minute per address and key, and lower limits on some methods.
  A refused call is `429` with `Retry-After`.

## Decision

1. **D1. A policy table.** `outline.workspaces.<key>` names a workspace by a key we choose. It holds `roles.admin`
   (the internal groups whose holders are admins; every other managed account is a member), `ignore` (addresses
   never demoted, removed or suspended, the API key's owner among them), an optional `client` (the sluis client the
   wiki signs in through) and `collections.<name>` with `read`, `read_write` and `admin` groups, `mode` (`extend`
   adds, `strict` also removes), an optional `directory` and an optional `adopt`. A URL, a key or an owner on a
   workspace is refused at load. `controllers.outline.enabledWorkspaces` lists the workspaces it changes; every other
   one is a dry run.
2. **D2. A connection record on the console.** An operator gives the URL and an admin's API key. The console asks
   Outline who the key is, refuses a key that is not an admin's or that belongs to another Outline team than the
   workspace was connected to, and keeps the key beside the record. Only the controller reads it.
3. **D3. The issuer hosts the controller** until it has a function of its own. On Kubernetes it is a loop in the
   issuer's process; on Lambda a schedule ticks the issuer's function per workspace. Its records, gates and key are
   the issuer's: kinds `outline-workspace` and `outline-gate` of the `oidc` module, the key at
   `internal/oidc/outline/<workspace>/<ref>`, the reports under the `oidc/` blob prefix. The issuer's role needs no new
   grant. The controller asks the console in its own process, as the roster controllers' workload, through the same
   handlers a controller elsewhere calls over the network.
4. **D4. One Outline group per internal group** the workspace names, called by its name and described as managed.
   It is created when missing and filled with the people who hold the group. An Outline group of another name is
   never touched.
5. **D5. People are resolved as the issuer resolves them.** Each account's address is put to the console's
   `Explain`. An authoritative "not found" or "suspended", or an account that reaches none of the client's required
   groups, is suspended. A suspended account the directory has back, reaching the client, is reactivated. An answer
   the directory cannot vouch for changes nothing for the person: no removal, demotion or suspension.
6. **D6. Breakers.** A pass holds, until an operator confirms its fingerprint, the removals over half of a managed
   group's members, the revocations over half of a collection's grants and direct members, more than one demotion
   or the last admin's, and more than three suspensions. A confirmation names a breaker and a fingerprint, and lapses
   after 24 hours.
7. **D7. The `directory` guard.** A collection that names a directory is held when a bound group's definition reaches
   outside it: a directory group of another directory, an `email` or `email_domain` it does not serve, or a machine
   matcher. A person whose address the directory does not serve is not added to a group bound to that collection.
8. **D8. Reported, never changed:** public sharing, members creating collections or inviting people, a default role
   other than member, a document of a collection that names a directory shared with someone it does not serve, and a
   Slack post integration on such a collection. The shares are read from `documents.memberships` once an hour, not
   from `events.list`.
9. **D9. The key's own user must stay an admin.** Every pass asks `auth.info`, and a key whose user is no longer an
   admin holds the whole workspace.

## Consequences

- Outline's permissions are the policy's. A collection manager's share by hand is reverted in a strict collection
  and audited.
- A person signing in for the first time sees only their own documents until the next pass. The pass runs every two
  minutes. Outline can send a `users.create` webhook; nothing listens for it yet.
- Every pass calls `auth.info`, which makes Outline re-validate the key owner's SSO sessions once that user has not
  signed in for an hour.
- The issuer's role holds the Outline keys, as it held the providers' credentials before their functions existed.
  Moving the controller to a function of its own is a migration of two kinds and one prefix.
- A share in a collection the key's user is not a member of is not read: Outline does not show it to an admin.
- The `directory` guard checks group definitions and addresses. It cannot see a group a person reaches through the
  vocabulary's role inheritance alone; the per-person check catches that one at addition.

## Alternatives considered

**A module and function of its own now.** The right end state, and a Pulumi role, table, deny-matrix rows and a chart
Deployment more than the first release needs. Deferred under 0071's allowance.

**Outline's group sync.** A paid feature, and it reads the identity provider's API, not sluis.

**Reading shares from `events.list`.** It cannot list `documents.add_group`, and its audit log is refused on the
Community plan. The scan is slower and complete.

**Granting people directly instead of through groups.** One grant per person and collection, and no group a person
can see in Outline's own pages. Rejected for the noise and the breaker arithmetic.
