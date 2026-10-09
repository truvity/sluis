# Migrate from an identity provider

Retire an IdP that only grants infrastructure access, with its login hook, broker and per-console clients, without broken logins.

## Before you start

- Install sluis ([install](../operate/install-with-helm.md)) and keep the access matrix the login hook reads.
- Sign-out must end the issuer's session, not only the gateway's, before the second console.
- Keep the old issuer running until step 6. Pointing a consumer back undoes every earlier step.

## Steps

### 1. Deploy sluis beside the IdP

Declare the service-account keys as workspaces and connect an audit installation. Render the policy from the access matrix, so internal group names equal the strings the IdP mints. Diff the `groups` of a live token from each issuer before rewiring, and fix differences in the derivation: they become rebinds when the provider flips. To undo, uninstall.

### 2. Pilot one console

The console's `Explain` and `ListHolders` replace the login hook's per-tenant reads, honouring `authoritative`. Point one console at the new issuer:

- Envoy Gateway: gateway-native OIDC, a `SecurityPolicy` with `oidc:` against a declared client ([console app](../connect/console-app.md)).
- Any other gateway: upstream oauth2-proxy with a declared confidential client ([recipe](../connect/oauth2-proxy.md)).

Role checks do not change. Check sign-in and sign-out. To undo, point the console back.

### 3. Clusters and cloud accounts

Add the new issuer as the API server's OIDC provider, non-production first. Distribute kubeconfigs with `sluisctl setup`; `kubectl auth whoami` shows the groups.

Add the IAM OIDC provider and an audience trust condition beside the old one, move people to `sluisctl aws`, then remove the old condition.

### 4. CI and the other consumers

Replace the broker's client in workflows with the action, or the `sluisctl` files a laptop uses. Rules on repository, ref and visibility replace the broker's mapping. Repeat step 2 for the other consoles, the CD system and the CLIs.

### 5. GitHub organisations and Slack workspaces

[Enable the organisation](../enable-github-organisation.md) and [the workspace](../enable-slack-workspace.md). Import the pairings the team-sync tool approved. Channels are covered in [bind Slack channels](../bind-slack-channels-in-git.md). To undo, remove the entry from the enabled list.

### 6. Retire

When no relying party uses it, remove the old IdP with its database, operator, login hook, broker and minted clients. Check no log shows a request to the old issuer. Keep its last backup per your retention policy: there is no undo.

## Afterwards

Export the old IdP's audit log before deleting it.
