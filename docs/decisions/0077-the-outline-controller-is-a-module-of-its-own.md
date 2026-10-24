# 0077 — The Outline controller keeps an Outline wiki's groups, roles and collections, as a module of its own

**Status:** Accepted (2026-10-24). Applies [0017](0017-the-slack-reconciler-membership-only.md) (a reconciler where a
claim cannot be read), [0018](0018-do-not-configure-what-the-product-knows.md) (the product's facts are not policy),
[0024](0024-reconciler-rails-are-shared-pieces-not-a-framework.md) (the shared rails) and
[0071](0071-least-privilege-modules-one-credential-holder-per-function.md) (one credential holder per function).
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
2. **D2. A module of its own, the only holder of the API keys.** The Outline module is a process of its own on
   Kubernetes (`sluis outline`, a Deployment of the chart) and a function of its own on Lambda
   (`sluis-<instance>-outline`, zip `sluis-outline`), with its own document (`sluis-outline/v1`), role, table
   (`sluis-<instance>-outline`), secrets (`internal/outline/workspaces/<workspace>/<ref>`), blob prefix (`outline/`)
   and schedule (one per workspace). The issuer never runs it and its role can read none of these. The issuer's
   document names it by `outline.remote`; without that there are no Outline pages.
3. **D3. The console door is the Slack module's.** The controller reads the console's API with its own proof as the
   roster controllers' workload: a projected ServiceAccount token on Kubernetes, the function role's web identity
   token on Lambda. The console's Outline pages call the module as the caller class `console` with a snapshot of the
   person (`outline.console.*`), exactly as the Slack pages call the Slack module.
4. **D4. A connection record in the module.** An operator gives the URL and an admin's API key on the console. The
   module asks Outline who the key is, refuses a key that is not an admin's or that belongs to another Outline team
   than the workspace was connected to, and keeps the key beside the record. No answer carries it back.
5. **D5. One Outline group per internal group** the workspace names, called by its name and described as managed.
   It is created when missing and filled with the people who hold the group. An Outline group of another name is
   never touched.
6. **D6. People are resolved as the issuer resolves them.** Each account's address is put to the console's
   `Explain`. An authoritative "not found" or "suspended", or an account that reaches none of the client's required
   groups, is suspended. A suspended account the directory has back, reaching the client, is reactivated.
   - An answer the directory cannot vouch for may **add** a person to a managed group, and so to the collections it
     opens.
   - It never **promotes**: nobody becomes a workspace admin on it, and nobody is added to a group that makes its
     holders admins (a `roles.admin` group, or a group granted `admin` on a collection).
   - Removals, demotions and suspensions need an authoritative answer.
7. **D7. Breakers.** A pass holds, until an operator confirms its fingerprint, the removals over half of a managed
   group's members, the revocations over half of a collection's grants and direct members, more than one demotion
   or the last admin's, and more than three suspensions. A confirmation names a breaker and a fingerprint, and lapses
   after 24 hours.
8. **D8. The `directory` guard.** `directory` names a connected directory by its primary domain (`example.com`) and
   nothing else. A collection that names one is held when a bound group's definition reaches outside it: a directory
   group of another directory, an `email` or `email_domain` it does not serve, or a machine matcher. A person whose
   address the directory does not serve is not added to a group bound to that collection.
9. **D9. Reported, never changed:** public sharing, members creating collections or inviting people, a default role
   other than member, a document of a collection that names a directory shared with someone it does not serve, and a
   Slack post integration on such a collection. The shares are read from `documents.memberships` once an hour, not
   from `events.list`.
10. **D10. The key's own user must stay an admin.** Every pass asks `auth.info`, and a key whose user is no longer
    an admin holds the whole workspace. The key's user should be a dedicated account before the controller manages
    more than a pilot; a person's own admin account serves the pilot.

## Consequences

- Outline's permissions are the policy's. A collection manager's share by hand is reverted in a strict collection
  and audited.
- A compromise of the issuer does not yield the Outline keys, and a compromise of the Outline module yields nothing
  of the issuer's or another provider's.
- A person signing in for the first time sees only their own documents until the next pass, which runs every two
  minutes. Outline can send a `users.create` webhook; nothing listens for it yet.
- Every pass calls `auth.info`, which makes Outline re-validate the key owner's SSO sessions once that user has not
  signed in for an hour.
- A share in a collection the key's user is not a member of is not read: Outline does not show it to an admin.
- The `directory` guard checks group definitions and addresses. It cannot see a group a person reaches through the
  vocabulary's role inheritance alone; the per-person check catches that one at addition.
- An installation runs one more function and Deployment, and an estate declares the module's package and schedule.

## Alternatives considered

**Host the controller in the issuer, under 0071's allowance for a provider with no function.** Fewer moving parts,
and the issuer's role would have held the Outline keys beside the signing key's wrapping key. Rejected by the owner
on 2026-10-24 for that.

**Outline's group sync.** A paid feature, and it reads the identity provider's API, not sluis.

**Reading shares from `events.list`.** It cannot list `documents.add_group`, and its audit log is refused on the
Community plan. The scan is slower and complete.

**Granting people directly instead of through groups.** One grant per person and collection, and no group a person
can see in Outline's own pages. Rejected for the noise and the breaker arithmetic.
