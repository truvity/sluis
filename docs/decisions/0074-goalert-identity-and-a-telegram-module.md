# 0074 — GoAlert identity reconciler and a Telegram module

**Status:** Accepted (2026-10-15). Builds on [0071](0071-least-privilege-modules-one-credential-holder-per-function.md)
(one function per credential holder), [0017](0017-the-slack-reconciler-membership-only.md) (membership only) and
[0008](0008-credentials-only-where-we-govern-membership.md).
**Date:** 2026-10-15

## Context

An on-call tool pages people. It needs to know who they are, which role they hold, which rotations they sit in and
where to reach them. That is identity. sluis already owns identity, and it already reconciles membership into GitHub and
Slack. Two additions let sluis feed GoAlert, an open-source on-call tool whose OIDC issuer is sluis:

- a **Telegram module**, `sluis-telegram`, so a person can be reached in Telegram and linked to their sluis identity;
- a **GoAlert reconciler**, which keeps GoAlert's users, roles, rotation participants and contact methods equal to what
  sluis policy says.

The reconciler's design rests on how GoAlert behaves. These facts were checked by reading GoAlert v0.35.0 source on
2026-10-15. A later GoAlert release can change them, and the reconciler's tests pin the behaviour it relies on.

- **Admin API keys.** `createGQLAPIKey{name, role, query, expiresAt}` makes a key bound to one stored GraphQL document.
  GoAlert checks the document's hash on every use. The document may hold several named operations, chosen by
  `operationName`, with free variables. Expiry is mandatory.
- **Sign-in matching.** GoAlert matches an OIDC sign-in only by `sub` (the `auth_subjects` row, provider `oidc`), never
  by email. With `OIDC.NewUsers: true`, a user created ahead of time without a linked subject is duplicated at the first
  sign-in.
- **No disable.** `UpdateUserInput` holds id, name, email and role only. Deleting a user removes them from rotations,
  fixes the active index, and cascades their contact methods, notification rules, auth subjects and sessions. Alert
  logs keep a null user.
- **Rotations are positional.** `updateRotation{id, userIDs, activeUserIndex}` replaces the participant list and names
  the active one by position.
- **Webhook contact methods start disabled.** An administrator cannot enable one. A method is enabled only by
  `sendContactMethodVerification` followed by `verifyContactMethod(code)`. The six-digit code is delivered through the
  webhook itself. The URL must pass `Webhook.AllowedURLs`.

## Decision

### Scope

1. **D571. sluis does identity for GoAlert, and nothing else.** That is users, role, rotation participants, Telegram
   contact methods and the removal of leavers. Schedules, escalation policies, services and "who is on call today" stay
   in GoAlert. Membership flows from the directory (Google Workspace groups) to sluis groups to GoAlert. Policy names
   which sluis group is the GoAlert administrator group and which groups feed which rotation.

### The Telegram module

2. **D572. `sluis-telegram` has the shape of `sluis-github`.** It is a module with its own binary, function and
   credential. An administrator creates each bot by hand with BotFather, because the Bot API cannot create a bot. The
   administrator registers the bot token with sluis, which keeps it in its secrets store. sluis then sets the bot up
   through the Bot API: commands, description and webhook.
3. **D574 a. sluis exports per-bot material to SSM** at `/sluis/<instance>/external/telegram/<bot>`, and the chat ids of
   linked people at `/recipients` under it, for the relay. Kubernetes consumers read these with External Secrets
   Operator. The export goes to SSM, not OpenBao, to repeat the `sluis-github` shape ([storage layout](../reference/sluis/storage-layout.md)).
4. **D575 a. Account linking mirrors GitHub account links.** The sluis console opens a signed deep link,
   `https://t.me/<bot>?start=<nonce>`. The bot's `/start` handler binds the Telegram user id to the sluis identity. A
   nonce is single-use and short-lived.
5. **One bot per purpose**, for example `pager_bot` and a health bot. An existing bot is adopted by registering its token.

### The GoAlert reconciler

6. **D576 a. sluis creates each GoAlert user ahead of sign-in and links it at once.** It calls `createUser`, then
   `addAuthSubject{providerID:"oidc", subjectID:<the person's sluis identity>}`. GoAlert's OIDC issuer is sluis, so sluis
   knows every `sub`. The first sign-in lands on the pre-created user and `NewUsers` creates nothing.
7. **D577 a. A leaver is deleted in GoAlert.** Once sluis removes a person from the feeding groups, the reconciler runs
   `deleteAll` with a `USER` target. The delete takes the person out of every rotation and clears their contact methods,
   rules and sessions.
8. **D578 a. The relay verifies contact methods.** The Telegram relay is a separate service owned by another team. It
   recognises GoAlert's verification message and calls `verifyContactMethod` itself. No person types a code. The relay
   holds its own narrow GoAlert key, whose stored query holds only the verify operation.
9. **D579 a. An administrator mints the reconciler's key once** in GoAlert's admin UI and registers it with sluis, as
   with the GitHub App. sluis generates and pins the stored query document and shows it for the administrator to paste.
   sluis warns before the key expires. A new operation needs a new key, so the document is versioned and a release that
   adds an operation says so in the CHANGELOG.

### The stored query document

The document holds these named operations. Each runs with variables only; the reconciler never sends other text.

| Operation | GoAlert call | Used for |
|---|---|---|
| `Users` | `users` | read every user, role and auth subject |
| `CreateUser` | `createUser` | pre-create a user |
| `LinkSubject` | `addAuthSubject` | bind the OIDC `sub` |
| `SetRole` | `updateUser` | set `user` or `admin` |
| `Rotations` | `rotations` | read rotations and participants |
| `SetRotation` | `updateRotation` | set `userIDs` and `activeUserIndex` |
| `ContactMethods` | `user.contactMethods` | read a user's methods |
| `CreateContactMethod` | `createUserContactMethod` | add a webhook method for the bot |
| `RequestVerification` | `sendContactMethodVerification` | start verification |
| `DeleteUser` | `deleteAll` | remove a leaver |

The relay's key holds `VerifyContactMethod` (`verifyContactMethod`) alone.

### Rotation updates keep the person on call

10. `updateRotation` is positional, so the reconciler computes `activeUserIndex` itself. It reads the rotation, finds the
    active participant by id, builds the new list, and sets the index to that participant's new position. If the active
    participant leaves, it keeps the index at the same position, clamped to the list, and logs the change. The reconciler
    never reorders participants it does not have to move.

## Consequences

- Nobody waits for a first login to be pageable.
- A leaver loses every GoAlert trace at once. Past alert logs show no user.
- The reconciler holds a standing admin key limited to ten operations, with an expiry. Expiry is an operational duty:
  the key is re-minted before it lapses.
- GoAlert becomes the second place a person's contact data lives. sluis stores only the chat id.
- A GoAlert upgrade that changes the GraphQL schema breaks the stored document. The fake in the tests is the first
  place the change shows.

## Alternatives considered

- **GoAlert creates users at first sign-in.** Rejected. Nobody can be paged before their first login.
- **Remove only the auth subject of a leaver.** Rejected. `NewUsers` creates the user again at the next sign-in.
- **People verify codes by hand, or add Telegram themselves.** Rejected. It leaves the contact data to each person and
  makes paging depend on a chore.
- **A sluis-owned service user with a password.** Rejected. It is a standing administrator password. A key is bound to
  a document and expires.
- **OpenBao as the export target for bot material.** Rejected. It would depart from the `sluis-github` shape for no gain.

## Implementation plan

Five pull requests, in this order. Each branches from `master`.

### 1. The sluis-telegram module

- **Shape.** A binary `cmd/sluis-telegram`, built once per platform. On Lambda it is one function with its own role and
  one alias per caller class (`live-issuer`, `live-console`). On Kubernetes it is one Deployment, ServiceAccount and
  Service, called over HTTP with a projected token ([modcall](../../internal/modcall/modcall.go)). Methods: register a
  bot, set a bot up, read status. The webhook is a route on the module and verifies Telegram's secret header.
- **Audit actions.** `roster.telegram_bot.registered`, `.adopted`, `.configured`, `.removed`;
  `roster.telegram_export.written`.
- **Tests.** The Bot API is faked at the HTTP boundary. Cases: token never in a log or a refusal, setup idempotent, export
  paths, webhook secret refused when wrong. No testcontainers.
- **Risks.** The bot token is a bearer secret. It stays in the module's secrets store, and the export is the only copy.
  Telegram retries a webhook, so handlers are idempotent.

### 2. Account linking in the console

- **Shape.** The console asks `sluis-telegram` for a nonce and shows the deep link. The `/start` handler redeems the
  nonce. A nonce is bound to the identity, lives a few minutes and is deleted on use. The caller class `console` may
  request a nonce, and only the webhook may redeem one.
- **Audit actions.** `roster.telegram_link.issued`, `.created`, `.moved`, `.removed`, `.refused`.
- **Tests.** Nonce single use, expiry, replay from another Telegram user, a second link for one identity, removal of the
  link when the person leaves. Telegram faked as above.
- **Risks.** A forwarded link binds the wrong account. The nonce is single-use and short-lived, and the bot confirms the
  sluis identity's display name in its reply so the person can see a wrong binding.

### 3. The GoAlert reconciler module

- **Shape.** A binary `cmd/sluis-goalert`. On Lambda it is a function on a schedule tick, as the other controllers. On
  Kubernetes it is a Deployment that ticks on an interval. It reads groups from the issuer, chat ids from
  `sluis-telegram` through modcall, and writes to GoAlert's GraphQL endpoint with the registered key. Parts: key
  registration and expiry warning, the generated query document, users and linking, role, rotations with
  `activeUserIndex`, contact methods, leaver deletion.
- **Audit actions.** `roster.goalert_key.registered`, `.expiring`; `roster.goalert_user.created`, `.linked`,
  `.role_set`, `.deleted`; `roster.goalert_rotation.updated`; `roster.goalert_contact.created`,
  `.verification_requested`.
- **Tests.** GoAlert is faked at the GraphQL boundary, with the behaviours from the Context: no disable, positional
  rotations, contact methods that start disabled, document hash checked. Cases: the person on call does not change on
  any edit, a pre-created user takes the first sign-in, a leaver is deleted once, a failed call retries without a
  duplicate. No testcontainers.
- **Risks.** A wrong delete removes a person from every rotation. The reconciler deletes only users it linked, refuses
  to delete when the feeding group is empty or unreadable, and caps deletions per pass. A key that expired reads as an
  outage, not as an empty directory.

### 4. The relay's verification, owned elsewhere

- **Shape.** The relay's owners add: recognise GoAlert's verification message, call `verifyContactMethod` with the
  narrow key. sluis supplies the documented operation and the chat ids under `/recipients`. This pull request is not in
  this repository.
- **Audit actions.** None in sluis. GoAlert's own log records the verification.
- **Tests.** The relay's owners' tests. sluis's reconciler test covers a method that stays disabled until verified.
- **Risks.** A relay outage leaves new methods disabled. The reconciler reports methods unverified past a bound and does
  not retry the code.

### 5. Docs and connect guides

- **Shape.** A guide to connect Telegram, one to connect GoAlert, and a concept page on the reconciler, each within the
  [writing limits](../WRITING.md). The reference pages for configuration, storage layout and audit actions gain the new
  rows through `just docs-generate`.
- **Audit actions.** None.
- **Tests.** `just docs-check`.
- **Risks.** Budgets in `hack/docs-budget.tsv` need a raise for the new pages.
