---
title: Outline
description: How sluis keeps an Outline wiki's admins, groups, collections and grants in step with directory groups, with a dry run first and breakers against mass removals.
status: target
sidebar:
  order: 10
---

Outline is a team wiki. sluis does not host or read the wiki's documents; it keeps **who may see what** equal to what the directory says.

## What it's for

- **A new employee can read the company wiki on day one**, and only the collections their group is bound to.
- **A leaver is suspended** in the wiki when the directory no longer vouches for them, without anyone remembering to do it.
- **Collections follow teams.** A private collection for the finance team is readable by exactly the finance group; the grant is removed from anything the policy does not name when the collection is *strict*.
- **Risky settings are reported.** Public sharing turned on, members allowed to create collections, a document in a restricted collection shared with someone the directory does not serve: each is a finding, reported rather than silently changed.

## Integration actors

| Actor | What it does for Outline |
|---|---|
| `Provisioner` | Pulls the desired state core publishes (admins, groups, members, collections, grants) and reconciles Outline to it. |

## What sluis asks the vendor for

An **API key of an Outline admin**. Outline has no service accounts, so the key belongs to a person; put that person's address on the ignore list, so the controller never demotes or suspends the account it runs as. Use a dedicated admin account before the controller manages more than a pilot. The key is a credential, referenced and never inlined.

The controller refuses a key whose user is not an admin. It calls only Outline's own API: users (list, role, suspend, activate), groups (list, create, memberships, add or remove a user) and collections (list, memberships, group memberships, grants). The wiki's URL must be reachable over HTTPS from the integration; loopback and link-local addresses are refused.

## What policy controls

- **Who is an admin**: the internal groups whose holders are workspace admins. Everyone else managed is a member.
- **Which groups hold which permission on which collection**: read, read and write, or admin. A group named at two levels gets the higher.
- **Collection mode.** `extend` only adds. `strict` also removes grants and direct members the policy does not name.
- **Which directory** every bound group must come from, so a collection for one company cannot fill from another.
- **Which accounts are never touched** (the ignore list).
- **Whether the controller acts at all.** Every workspace is a dry run until it is enabled.
- **Breakers.** A pass that would remove over half of a group's members or a collection's grants, demote more than one admin (or the last), or suspend more than three accounts stops and waits for an operator to confirm. A confirmation lapses after a day.

## What is audited

Every change the controller makes: a group created, a member added or removed, a role changed, a collection created or adopted, a grant made or revoked. Each held change and each finding. The API key is never recorded.

## Setup

An example; see [the setup patterns](/integrations/connecting/).

1. Create an API key as an Outline admin. There is no manifest flow here: Outline has none.
2. Run `sluis integrations connect outline --url https://wiki.example.org`, which asks for the key (as a credential reference, never on the command line) and checks that its owner is an admin.
3. Declare the workspace, its collections and their groups in the policy.
4. Read the dry-run report: accounts to suspend, groups to create, grants to add or remove. Resolve the held rows.
5. Add the workspace to the enabled list.

```yaml
# policy (example)
outline:
  workspaces:
    wiki:
      roles: { admin: [wiki-admins] }
      ignore: [key.owner@example.org]
      collections:
        General: { mode: strict, read_write: [everyone], admin: [wiki-admins] }
        Finance: { mode: strict, directory: example.org, read_write: [finance] }
```

## v1 → v2 notes

- v1 does this as its own module, with a dry run, four breakers and findings. See [Connect and enable an Outline workspace](/v1/guides/sluis/enable-outline-workspace/) and the [Outline reference](/v1/reference/sluis/outline/).
- In v1 the policy carries an `outline` block that the controller reads itself. In v2 the integration loads no policy: core publishes the desired state by digest, and the integration only pulls and reconciles.
- Parity issue: [#815](https://github.com/truvity/sluis/issues/815).
