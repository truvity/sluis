---
title: Google Workspace
description: How sluis signs people in with Google accounts and reads a Workspace directory for users, groups and domains, with read-only scopes and a keyless option.
status: target
sidebar:
  order: 2
---

Google Workspace is the corporate directory in the common case. It plays two integration actors: people sign in with their Google account, and sluis reads the tenant's users, groups and domains as facts.

## What it's for

- **People sign in with the account they already have.** The engineer opens a console or runs `sluis login`, is routed to the right Google tenant by the domain of their email address, passes the tenant's MFA, and returns with a sluis session. sluis never sees a password.
- **Policy talks about groups that already exist.** "Members of `platform-engineers@example.org` may assume this role" needs the group's membership. The directory integration supplies it, and keeps it fresh.
- **A leaver loses access when the directory says so.** Group membership changes reach sluis on the next refresh, and a failed read is an error, never an empty answer that would remove everyone.
- **Several companies, one sluis.** Each connected tenant serves the domains it owns, and an address is routed to the tenant that owns its domain.

## Integration actors

| Actor | What it does for Google |
|---|---|
| `Upstream` | Runs the sign-in: sends the person to Google, checks the returned ID token (issuer, audience, `nonce`, verified email), and returns a verified identity. |
| `FactsSource` | Reads users, groups and domains, keeps a snapshot per tenant refreshed on an interval, and answers "what groups is this person in" with a freshness: `as_of`, `max_age` and whether the answer is authoritative. |

## What sluis asks the vendor for

For sign-in, an OAuth client of type *Web application* with the scopes `openid`, `email` and `profile`. Nothing else.

For the directory, **read-only** scopes only:

```text
https://www.googleapis.com/auth/admin.directory.user.readonly
https://www.googleapis.com/auth/admin.directory.group.readonly
https://www.googleapis.com/auth/admin.directory.group.member.readonly
https://www.googleapis.com/auth/admin.directory.domain.readonly
```

There are three ways to give sluis those scopes, from least to most standing power:

| Method | What sluis holds | What it needs |
|---|---|---|
| Workload Identity Federation with delegation (keyless) | Nothing long-lived. sluis exchanges its own workload token for a short-lived service-account identity. | A workload identity pool, a service account with domain-wide delegation for the four scopes. |
| Service account key with delegation | A JSON key, as a credential. | The same delegation grant, plus a key. Some organisations forbid creating keys. |
| Admin consent | A refresh token of a Super Admin, which dies with that account. | An OAuth consent by a Super Admin. |

The [setup pattern](/integrations/connecting/#google-workspace) compares them. The first is the target for new installs; the other two exist because v1 offers them.

## What policy controls

- **Which domains a tenant serves.** An address routes to a tenant by its domain; a domain served by no tenant cannot sign in. The tenant can serve every domain it owns, or a list.
- **Which groups are read.** `syncGroups` narrows the groups that are mirrored, so a policy can only refer to those.
- **Who may sign in at all.** Sign-in proves an identity; whether it is admitted to a client is the policy's `requires`.
- **Freshness.** How long a held snapshot may be used when the directory cannot be reached. After that window sluis refuses rather than guesses.

sluis refuses a sign-in whose email is unverified, and a sign-in for an address whose Google `sub` differs from the first one recorded (a deliberate move needs an operator's action).

## What is audited

- A directory connected, reconnected or disconnected, and a change to its domains or groups.
- Each sign-in, with the client and the class of session, and each refusal, including a changed subject.
- A directory read that failed, as a failure, not as an empty result.

No Google credential and no refresh token is ever written to the audit record.

## Setup

An example; the full flow is in [the setup patterns](/integrations/connecting/#google-workspace).

1. In Google Cloud, create a project and enable the Admin SDK API. Create the OAuth client for sign-in with the redirect URI that sluis prints.
2. In the Admin console, authorise the service account for the four read-only scopes (domain-wide delegation), or consent as a Super Admin.
3. Run `sluis integrations connect google-workspace --tenant example.org`.
4. Choose which domains the tenant serves. Unselected domains are never read.

```yaml
# /etc/sluis/install.yaml (example)
integrations:
  google-workspace:
    example-org:
      actors: [upstream, facts]
      admin: admin@example.org            # the account the delegation impersonates
      serve: [example.org]                # omitted: every domain the tenant owns
      oauth_client:
        id: cred:sluis-int-google.client-id
        secret: cred:sluis-int-google.client-secret
      # keyless: workload identity federation; keyed: a service-account key credential
      auth: { method: federation, service_account: sluis-dir@project-id.iam.gserviceaccount.com }
```

## v1 → v2 notes

- v1 supports admin consent and a service-account key, connected from the console. It does not do keyless delegation. See [Connect a corporate directory](/v1/guides/sluis/connect/google-workspace/) and [what a workspace is](/v1/concepts/sluis/directory-model/).
- Entra (Microsoft) has no backend in v1 and none in the first v2 release.
- Parity issues: [#805](https://github.com/truvity/sluis/issues/805) the directory, [#806](https://github.com/truvity/sluis/issues/806) snapshots and freshness, [#807](https://github.com/truvity/sluis/issues/807) sign-in through the directory.

:::note[Proposed]
Keyless delegation for Google APIs uses the `sluis-node` workload token as the federated identity, which is the design of the GCP exchange ([#871](https://github.com/truvity/sluis/issues/871)). It needs a GCP-compatible signing algorithm (ES256), which is covered by the cloud-federation exception in [Keys](/concepts/keys/). It is not yet built.
:::
