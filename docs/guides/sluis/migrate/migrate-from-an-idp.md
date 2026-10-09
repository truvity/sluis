# Migrate from an identity provider

## Purpose

Retire an IdP whose only job is infrastructure access, and the pieces around it (a directory reader per tenant, a login
hook that computes roles, a broker that mints machine tokens, a per-console client minted by an operator), without a day
of broken logins.

## Preconditions

- sluis installed ([install](../operate/install-with-helm.md)) and the access matrix the login hook reads.
- A way to decode a live token from the old IdP.

## Before you start

- **Diff the `groups` lists before anything is rewired.** A difference becomes a rebind when the provider flips.
- **Sign-out must end the issuer's session**, not only the gateway's or the proxy's, before the second console, or the
  first report from every pilot is "it signed me straight back in".
- **Preview before every apply, and read the preview.** Each step is an installation or policy change: render, diff, upgrade.
- **The old issuer keeps running until step 9.** Pointing a consumer back at it is the rollback of every step before.

## Steps

### 1. Deploy sluis beside everything

**Run** install with the existing service-account keys as declared workspaces, an audit installation connected, and the
policy rendered from the same access matrix the login hook reads, so the internal group **names** are the strings the
IdP mints today. No relying party trusts the new issuer yet.

**Expect** nothing user-visible changes.

**Verify** decode a live token from each issuer and diff the two `groups` lists. Fix differences in the derivation, not in
the bindings.

**Rollback** uninstall; nothing depends on it.

### 2. Move the directory reads

**Run** whatever the login hook read per tenant is answered by the console's `Explain` and `ListHolders`, honouring
`authoritative`.

**Expect** the per-tenant readers have no caller.

**Verify** the answers agree for a sample of addresses.

**Rollback** point the hook back at its readers.

### 3. Pilot one console

**Run** point one console at the new issuer. On Envoy Gateway use gateway-native OIDC (a `SecurityPolicy` with `oidc:`
against a declared client, [console app](../connect/console-app.md)); on any other gateway run upstream oauth2-proxy with a
declared confidential client ([recipe](../connect/oauth2-proxy.md)). The `access-proxy` chart is gone
([ADR 0003](../../../decisions/0003-deprecate-access-proxy.md)). Its role checks do not change, because the `groups` values did not.

**Expect** sign-in and sign-out both work.

**Verify** watch a day of logins.

**Rollback** point the console back at the old issuer.

### 4. Clusters

**Run** add the new issuer as the API server's OIDC provider (platforms that allow one provider per cluster need a flip
per cluster, non-production first). Put `sluisctl` on every laptop and distribute kubeconfigs with `sluisctl setup`.

**Expect** `kubectl` works with the new tokens.

**Verify** one person per cluster signs in and `kubectl auth whoami` shows the expected groups.

**Rollback** flip the provider back.

### 5. Cloud accounts

**Run** add the IAM OIDC provider for the new issuer and a trust condition on the audience beside the old one; move people
to `sluisctl aws`; remove the old condition.

**Expect** both issuers work until the old condition goes.

**Verify** `sluisctl aws` yields working credentials.

**Rollback** re-add the old condition.

### 6. CI

**Run** replace the broker's client in workflows with the action, or with the same `sluisctl` files a laptop uses. Rules on
repository, ref and visibility replace the broker's mapping.

**Expect** workflows obtain credentials from sluis.

**Verify** one run per workflow type.

**Rollback** revert the workflow change.

### 7. The rest of the consoles, the CD system, the CLIs

**Run** as step 3, one by one.

**Expect** and **Verify** as step 3. **Rollback** per consumer.

### 8. GitHub organisations and Slack workspaces

**Run** bind the organisation's teams in the policy, connect it from the console, read the dry run on its page, then list it
in `policy.controllers.github.enabledOrgs`. The tool that synced teams retires with it; the pairings it approved are
imported once. For Slack, declare the workspace in the policy, connect it, read the dry run, then list it in
`policy.controllers.slack.enabledWorkspaces`. A channel the infrastructure owns is a policy channel fed by internal
groups; a channel people manage themselves is a console channel fed by directory groups.

**Expect** the dry run shows no unexpected change.

**Verify** after enabling, the console shows the controller's last report with no error.

**Rollback** remove the organisation or workspace from the enabled list: it is a dry run again.

### 9. Retire

**Run** when the old IdP has no relying party left: remove the IdP, its database and operator, the login hook, the broker and
the minted clients.

**Expect** nothing else notices.

**Verify** no log line shows a request to the old issuer.

**Rollback** none, because the old system is deleted; keep its last backup for the length of your retention policy.

## Afterwards

The audit installation's trail replaces the IdP's audit log: export the old one before deleting it. Tell the consoles' owners
that the issuer URL is the new one.
