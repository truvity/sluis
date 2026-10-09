# Why does sluis exist?

An installation already has corporate directories, a CI platform that signs a token for each job, clusters that issue ServiceAccount tokens, and infrastructure that speaks OIDC. It lacks something that turns "Alice is in `platform-admins`" into "Alice may assume role `power` in account 1111". That needs one policy and no stored secret.

## The problems

1. Groups are not in anyone's token. Google's ID token has none. Entra's has them with an overage limit.
2. Leavers must lose access without a click, and only on an answer the directory vouches for. A hiccup must not remove anyone.
3. Several directories need one policy file, not one admin console each.
4. AWS trust policies for a custom issuer see only `sub`, `aud`, `amr` and `email`. Group-based roles need an issuer that gates audiences by policy.
5. An EKS cluster trusts one external OIDC issuer, so people and CI jobs must both arrive through it.
6. CI and workloads must get cloud and cluster credentials from the token their platform already gave them.
7. Consoles need login, session, sign-out and a bearer without implementing any of it.

## Why not an existing tool

| Tool | What it gives | Where it stops |
|---|---|---|
| Google Workspace as issuer | One issuer, sign-in, MFA. | No groups in the token, no audience gating, no CI exchange. |
| dex | A stateless federating issuer. | No group lookup for Google, fixed claims, no policy, no audience gating. |
| Zitadel, Keycloak, Authentik | Federation, login hooks, machine users. | A database, an operator and a login UI to run, and a directory reader to write. |
| Okta, Auth0, Entra ID | Everything, rented. | Policy lives in their UI and machine identities are secrets. |
| AWS IAM Identity Center | AWS roles by group. | Answers only AWS. |
| GitHub to AWS federation | CI to AWS with no code. | Policy sprawls across trust policies, and clusters are not served. |
| Teleport, Boundary | A full access plane. | Its own agents and identity store, and it replaces the gateway. |

Each tool solves two or three of the seven problems. sluis solves all seven with what the installation already has.

## Principles

When two conflict, the earlier one wins.

1. Verify, never authenticate. Passwords, user records and MFA belong to the identity provider. There are no users here, only sessions.
2. Configuration is chart values. The policy is a file in git that the console reads and never edits, so `git log` is the history of access. The console writes only bootstrap, removals and a few audited records of its own. They never name an internal group.
3. Almost nothing is a secret: one signing key, the directory credentials, the GitHub App keys and the Slack bot tokens. Machines prove themselves with their platform's tokens.
4. Serve only what is needed. Three grants and three endpoints cover a browser, a CLI and a machine. Every served endpoint must pass the conformance suite.
5. One issuer, one policy, one vocabulary: one `iss`, one file, one flat `groups` claim.
6. Authoritative or hold. A failed probe, a stale snapshot or a doubly claimed domain never removes access.
7. Keep the relying party light. ArgoCD, Kargo and a cluster need a client id and an issuer URL.

## What it is not

sluis is not a customer-facing identity provider, a service mesh or a secrets manager. It is not a replacement for the corporate directory: a wrong directory makes every token wrong. It is not a chat or source-hosting administrator. It keeps GitHub team and Slack channel membership equal to groups it already resolves. It creates no accounts, manages no user groups and removes no one from a public Slack channel.

## Decided in

- [ADR 0008: credentials only where we govern membership](../../decisions/0008-credentials-only-where-we-govern-membership.md)
- [ADR 0018: do not configure what the product knows](../../decisions/0018-do-not-configure-what-the-product-knows.md)
- [ADR 0028: nothing writes ConfigMaps or Secrets](../../decisions/0028-nothing-writes-configmaps-or-secrets.md)
