---
title: Integrations
description: Every system sluis connects to, the integration role it plays, the use case it serves, and how to connect it. One page per integration, plus the setup patterns.
status: target
sidebar:
  order: 0
---

An integration connects sluis to something outside it. [Concepts: Integrations](/concepts/integrations/) explains the model: six integration roles, each integration its own process holding only its own vendor credential, and one rule, **integrations return facts, or carry out effects that core has already authorized; they never decide**. This section is the other half: what each integration is *for*, what it asks the vendor for, and how an operator connects it.

## How to read a page

Every integration page has the same seven parts, in this order.

| Part | Answers |
|---|---|
| What it's for | The real use cases, written generically: "a CI job publishes a package", "an engineer opens a database session". |
| Integration roles | Which of the six roles the integration plays. |
| What sluis asks the vendor for | The credentials, scopes and permissions sluis needs, at least privilege. |
| What policy controls | Which decisions stay in the reviewed policy, and which limits live in core. |
| What is audited | Which events leave a record. A credential value is never recorded. |
| Setup | The operator steps, linking to the [setup patterns](/integrations/connecting/). |
| v1 → v2 notes | What v1 does today, and the parity issue that tracks it. |

## The integrations

The six roles are `Upstream` (sign-in providers), `WorkloadVerifier` (verifies workload tokens), `FactsSource` (directory facts), `CredentialMinter` (downstream credentials), `Provisioner` (reconciles an external system to a desired state) and `Notifier` (sends a message).

| Integration | Roles | The use cases it serves | Page |
|---|---|---|---|
| GitHub | `FactsSource`, `Provisioner`, `CredentialMinter`, `WorkloadVerifier` | A CI job publishes a package or opens a release with a token that lives for minutes and covers one repository. A new engineer joins the organisation's teams because the directory says so. A job proves where it runs with its Actions identity token. | [GitHub](/integrations/github/) |
| Google Workspace | `Upstream`, `FactsSource` | People sign in with the account they already have. Policy says "members of this group" and the group lives in the company directory. | [Google Workspace](/integrations/google-workspace/) |
| Generic OIDC and peer sluis | `Upstream`, `FactsSource` (claims), `WorkloadVerifier`, `CredentialMinter` (peer) | A customer or partner signs in with their own OpenID Connect provider. A second sluis installation vouches for its own people. | [Generic OIDC and peer sluis](/integrations/oidc-upstream/) |
| Slack | `Provisioner`, `Notifier`, `FactsSource` | A private channel always holds exactly the people of one group. An approver gets a message when a request waits. | [Slack](/integrations/slack/) |
| Cloudflare | `CredentialMinter` | A release job gets a DNS-edit token for one zone, or an object-store credential for one bucket, that expires within the hour. | [Cloudflare](/integrations/cloudflare/) |
| AWS | `WorkloadVerifier`, plus accounts that trust sluis | An engineer assumes a role in an AWS account without an identity-centre product. A CI job deploys with a role. A Lambda function proves itself to a service. | [AWS](/integrations/aws/) |
| Kubernetes | `WorkloadVerifier`, plus clusters that trust sluis | An engineer runs `kubectl` with their group mapped to RBAC. A service in one cluster calls a service in another with its ServiceAccount identity. | [Kubernetes](/integrations/kubernetes/) |
| PostgreSQL | Client profile on the `ca` role | An engineer opens a database session with a client certificate that expires the same day, in place of a password. | [PostgreSQL](/integrations/postgresql/) |
| SSH | Client profile on the `ca` role | An engineer or a robot logs in to a host with an SSH certificate; a client trusts a host's CA, not each host key. | [SSH](/integrations/ssh/) |
| Outline | `Provisioner` | A wiki's admins, groups and collection grants follow the directory, and a leaver loses access. | [Outline](/integrations/outline/) |
| GoAlert and Telegram | `Provisioner`, `Notifier` | The on-call tool knows who is on which rotation, and a person can be reached on a chat app linked to their identity. | [GoAlert and Telegram](/integrations/oncall/) |
| Applications and gateways | Client profiles | A deployment console, an MCP server or an application with its own sign-in trusts sluis as its issuer. cert-manager asks for a certificate. | [Applications and gateways](/integrations/applications/) |

## Client profiles are not integrations

Six of v1's 16 "integrations" call no external system. They are clients of sluis: databases, SSH hosts, cert-manager, the GitOps controllers, MCP and SSH sign-in. In v2 they are **client profiles**: configuration for how that client signs in and what it may ask for. A client profile has no process, no credential and no manifest of its own. Their pages here say what the client needs and what the operator writes.

The AWS and Kubernetes pages sit between the two: the integration part is the `WorkloadVerifier` that checks the token a workload presents, and the rest is configuring the account or cluster to trust sluis.

## Setup

The mechanics of connecting each vendor repeat. The [setup patterns](/integrations/connecting/) describe them once: the GitHub App manifest flow, Slack app manifests, Google Workspace delegation (with and without a stored key), Cloudflare's minting token, AWS trust, and a generic OIDC upstream.

## Status

The pages describe the target state. v1 does most of this today; the [features page](/features/) lists each v1 capability against its parity issue, and every integration page links its issues in the last section. [Open decisions](/open-decisions/) lists the points the maintainer has not decided.
