---
title: Slack
description: How sluis keeps Slack channels equal to directory groups, serves Slack Connect channels and sends notifications, with a bot token and least-privilege scopes.
status: target
sidebar:
  order: 4
---

Slack is a chat workspace. sluis uses it as a source of facts about users and as a place to keep channel membership in step with the directory. Notifications use the same bot.

## What it's for

- **A private channel always holds exactly the people of one group.** The `#platform-oncall` channel is bound to the `platform-oncall` group; when someone joins or leaves the group, the channel follows. In a *strict* channel the controller removes members the directory no longer vouches for. In an *extend* channel it only adds.
- **A channel between two companies, kept honest.** A Slack Connect channel between the installation's own workspaces is a record fed by directory groups and addresses. It never loses members by accident.
- **Channels kept from the console.** Some channels are not bound in git; the console manages them as records, and the two kinds are never mixed.
- **A leaver is reported.** A person who left the directory but still sits in a channel is shown in a report, with a breaker that stops a pass which would remove too many.
- **An approver is told that a request waits.** The notifier sends a message to a person or a channel, and handles the reply that comes back through the webhook.

## Integration actors

| Actor | What it does for Slack |
|---|---|
| `Provisioner` | Makes managed channels equal their desired membership: creates or adopts a channel by name, invites, removes. |
| `Notifier` | Sends a message to a person or a place. |
| `FactsSource` | Resolves a person by email, so a directory address maps to a Slack user. |

## What sluis asks the vendor for

One Slack App per workspace, created from a manifest (see [the setup pattern](/integrations/connecting/#slack-app-manifests)), with a bot token. The scopes are the smallest set that covers the jobs you turn on:

| Scope | For |
|---|---|
| `users:read`, `users:read.email` | Looking up a person by address. |
| `channels:read`, `groups:read` | Reading the members of public channels, and of private channels the bot is in. |
| `channels:manage`, `groups:write` | Creating, inviting to, removing from and archiving public and private channels. |
| `channels:join` | Adopting an existing public channel. |
| `conversations.connect:write`, `conversations.connect:manage` | Slack Connect: sending and accepting invitations, and listing them. |
| `chat:write` | Sending notifications. v1's bot does not hold this scope, so it is new. |

A declared App lists its scopes; adding one is a deliberate change, and the console shows *scopes missing* until the owner approves the new set. There is no events or interactivity URL unless the notifier's reply path is turned on.

## What policy controls

- **Which groups map to which channels**, and the channel's mode: `extend` (only adds) or `strict` (also removes).
- **Which workspace is enabled to act.** Everything is a dry run until the workspace is enabled.
- **Which channels the bot never touches.** A channel defined twice, in git and on the console, is *held*, not taken over, until a declared fragment adopts the console record by its revision ([ADR 0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/)).
- **Who may connect or disconnect** a workspace: the operators of the directory that owns it.
- **Breakers.** A pass that would remove too many people stops and waits for a confirmation.

The pinned workspace is a safety rule, not a policy choice: the team recorded at the first install is an issued record (a pin), backed up and restored, and it is the only team the App may be installed into. An install into another team is refused and its token revoked.

## What is audited

- A workspace connected, reconnected, disconnected or refused (an install into the wrong team).
- An App created, installed, or refused.
- Each membership change the controller makes: invited, removed, held, and each leaver reported.
- Slack Connect channel definitions, created or changed from the console.
- Notifications sent: the recipient and the template, not the message body if it carries a secret.

No bot token, client secret or configuration token is ever written to the record.

## Setup

An example; see [the setup pattern](/integrations/connecting/#slack-app-manifests).

1. In Slack, generate an **App Configuration Token** for the owning workspace. It expires in 12 hours and is used once.
2. Run `sluis integrations connect slack --workspace acme`, paste the token when asked. sluis creates the App from its manifest.
3. A workspace owner approves the install in Slack. Slack returns to sluis, which keeps the bot token as a credential.
4. Bind groups to channels in the policy. Leave the workspace out of the enabled list and read the dry-run report first.

```yaml
# /etc/sluis/install.yaml (example)
integrations:
  slack:
    acme:
      actors: [provisioner, notifier, facts]
      app:
        id: sync
        bot_scopes: [users:read, users:read.email, channels:read, groups:read,
                     channels:manage, groups:write, channels:join, chat:write]
      # the bot token is issued by Slack at install and held as cred:sluis-int-slack.acme.bot-token
```

## v1 → v2 notes

- v1 does the channel work today and runs it as a controller in the Slack module, with a console flow for connecting a workspace and a catalogue of Apps. See [Connect a Slack workspace](/v1/guides/sluis/connect/slack-workspace/), [a catalogue of Slack Apps](/v1/guides/sluis/connect/slack-apps-catalogue/), [Slack Connect channels](/v1/guides/sluis/connect/slack-connect-channels/) and [console channels](/v1/guides/sluis/connect/slack-console-channels/).
- The v1 App is created with a configuration token, once; v2 keeps that and describes it as a manifest pattern.
- v1 sends no notifications. The `Notifier` actor and `chat:write` are new.
- Parity issues: [#812](https://github.com/truvity/sluis/issues/812) policy channels, [#813](https://github.com/truvity/sluis/issues/813) console and Slack Connect channels, [#814](https://github.com/truvity/sluis/issues/814) workspace connection and Apps catalogue.

:::note[Proposed]
Which notifications sluis sends, and the reply path (a webhook with a signed request), are not yet decided. The `Notifier` contract exists; the list of events does not.
:::
