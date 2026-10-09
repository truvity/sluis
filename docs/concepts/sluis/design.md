# sluis — the design

**Status:** shipped. One process reads the corporate directories, applies the policy, issues tokens, serves the login
page and serves the console, and runs the GitHub and Slack controllers beside them.

This page is the map of the design. The rule it stands on, two trust anchors and one vocabulary, is
[trust](trust.md); every container and how they connect is [architecture](architecture.md); the contracts are under
[reference](../../reference/sluis/configuration.md); the storage and platform edges are [ports](ports.md). Operating it is under
[how-to](../../guides/sluis/operate/day-two.md).

## Purpose

One installation-wide issuer that every cluster, cloud account, CD system and console trusts, fed by the corporate
directories the company already runs.

It answers two questions about a person, **is this account live** and **who is in this group**, and turns the answer
into a token, under a policy that is a file in git. It holds every directory credential so that nothing downstream
holds any. It authenticates nobody: sign-in, passwords, MFA and device policy stay with Google Workspace or Entra.

## One process, and why

Directory reader, token service, console and both controllers are one process, since v1.63. See
[one process](one-process.md).

## The directory model

A workspace, its domains, routing by email domain, and what "authoritative" means. See
[the directory model](directory-model.md).

## Freshness

Snapshots, never a read on the request path, one lease per interval, the state shared across replicas. See
[freshness](freshness.md).

## The policy

One file, one layer, validated at load. See [what it verifies and issues](verify-and-issue.md#the-policy).

## What it verifies

Corporate sign-ins, CI tokens and workload tokens from any cluster. See
[what it verifies](verify-and-issue.md#what-it-verifies).

## What it issues

Six grants, three kinds of exchange subject. See [what it issues](verify-and-issue.md#what-it-issues).

## Sessions and sign-out

The SSO session, per-client sessions, the absolute limit, who may open which console, and telling the relying party.
See [sessions](sessions.md) and [back-channel logout](back-channel-logout.md).

## The console

Same origin, signs in as a client, reads everything, changes only bootstrap, removals and confirmations. See
[the console](console.md).

## The GitHub controller

Joiners, movers and leavers with nobody in the loop, and the rails shared with Slack. See
[the GitHub controller](github-controller.md#the-loop) and [reconciler rails](github-controller.md#reconciler-rails).

## The Slack reconciler

Channels that contain the people who hold the groups bound to them. See [the Slack reconciler](slack-reconciler.md).

## Audit

sluis records into an audit installation and keeps no trail of its own. See [audit](audit.md); the actions are in
[audit actions](../../reference/sluis/audit-actions.md).

## The store

State, secrets and blobs behind ports: DynamoDB on AWS, the legacy Kubernetes objects until the cutover. See
[the store](store.md) and [Kubernetes objects](../../reference/sluis/kubernetes-objects.md).

## Recovery

The way in on the day no directory can vouch for anybody. See [recovery](recovery.md).

## Failure semantics

Access is removed only on an authoritative answer. See [failure semantics](failure-semantics.md).

## What was removed

See [what is not served, and what was removed](not-served.md).

## Build

`devbox shell`, then `just check`. The chart is `charts/sluis`. For a console with no OpenID flow of its own, use
gateway-native OIDC on Envoy Gateway, or run upstream oauth2-proxy on other gateways
([ADR 0003](../../decisions/0003-deprecate-access-proxy.md); the recipe is [oauth2-proxy](../../guides/sluis/connect/oauth2-proxy.md)).
