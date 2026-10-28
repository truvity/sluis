---
title: GoAlert and Telegram
description: How sluis feeds the identity side of an on-call tool, with users, roles and rotation participants, and reaches people on Telegram after they link their account.
status: target
sidebar:
  order: 11
---

An on-call tool pages people. To do that it needs to know who they are, which role they hold, which rotation they sit in and where to reach them. That is identity, and sluis already owns identity. This page covers two small integrations that give an on-call tool exactly that. They follow the same rules as the others: sluis does identity, and schedules, escalation policies and "who is on call today" stay in the on-call tool.

## What it's for

- **Nobody waits for a first login to be pageable.** A new engineer in the on-call group is created in the on-call tool ahead of time and linked to their sluis identity, so the first sign-in lands on the existing user.
- **A leaver leaves every rotation.** When sluis removes a person from the feeding groups, the user is deleted in the on-call tool, which takes them out of rotations and clears their contact methods and sessions.
- **The rotation follows the group.** A rotation's participants are the members of a directory group, in order, and the person who is on call stays on call when the list changes.
- **A person can be reached on Telegram.** They link their Telegram account to their sluis identity once, with a single-use link, and the on-call tool's contact method is verified without anyone typing a code.

## Integration roles

| Integration | Role | What it does |
|---|---|---|
| GoAlert | `Provisioner` | Makes users, roles, rotation participants and contact methods equal the desired state. Assigns the administrator role from a named group, which is an authority mapping and so comes from the decision point, never from the integration. |
| Telegram | `Notifier`, plus the account-link flow of the base `Integration` contract | Delivers a message to a person or a place, and receives interactions through its webhook. |

## What sluis asks the vendor for

- **GoAlert:** an **administrator API key**, bound to one stored GraphQL document, with a mandatory expiry. The document holds a small fixed list of operations (read users and rotations, create a user, link the sign-in subject, set a role, set a rotation, manage a contact method, delete a user), each run with variables only. The key is minted once by a GoAlert administrator and registered with sluis, which warns before it expires. A narrower key, which can only verify a contact method, belongs to the Telegram relay.
- **Telegram:** a **bot token**. The Bot API cannot create a bot, so an administrator creates each bot with the platform's own bot-management bot and registers its token with sluis. sluis then sets up commands, description and webhook through the Bot API. One bot per purpose.

Both are operator secrets, referenced and never inlined.

## What policy controls

- Which sluis group is the on-call tool's administrator group.
- Which group feeds which rotation.
- Which people may link a Telegram account, and to which bot.
- Nothing else about schedules or escalation: sluis does not model them.

## What is audited

Each user created, linked, given a role or deleted; each rotation change; each Telegram link made, with the identity and the bot, and each message sent. Tokens and keys are never recorded.

## Setup

An example; see [the setup patterns](/integrations/connecting/). Neither vendor has a manifest flow, so both are connected with a credential the administrator creates by hand.

1. In GoAlert, an administrator creates the API key with the document `sluis` prints.
2. Run `sluis integrations connect goalert --url https://oncall.example.org`, which takes the key as a credential reference.
3. Create the Telegram bot with the platform's bot-management bot, then run `sluis integrations connect telegram --bot pager`.
4. Declare the groups in the policy and read the dry run before enabling.

## v1 → v2 notes

- v1 accepted the design as [ADR 0074](/v1/decisions/0074-goalert-identity-and-a-telegram-module/). The parity list taken from release 1.79.3 does not include a GoAlert or Telegram item, so these two integrations have no parity issue yet.
- The rotation update keeps the active participant: the update call is positional, so the integration computes the active index itself.

:::note[Proposed]
Both integrations are part of the v2 contract set but are not scheduled. Their configuration keys, and whether Telegram stays a separate `Notifier` or becomes a delivery mode of another role, are not decided.
:::
