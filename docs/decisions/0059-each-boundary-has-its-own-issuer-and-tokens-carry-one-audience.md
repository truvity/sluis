# 0059 — Each boundary has its own issuer; tokens carry one audience that names a registered client

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (threat IDs from the threat model added to the consequences).

## Context

v1 was one installation with one token issuer and one signing ring. Every relying party, in every environment, trusted the same key. A compromise of that signer, or a bug in its policy, could mint a token for production as easily as for development.

v2 is a federation (ADR 0009). One **front installation** gives people one sign-in URL, one console and one identity plane. Each **environment boundary** at L4 is its own installation, with its own account or cluster, state and keys, joined to the front by declared trust. Company boundaries own directories, upstreams and people records, but hold no authority key. Product boundaries (a company in an environment) are created only when a company's own customers sign in through sluis.

That raises four questions:

- which boundaries issue tokens, and at which URLs;
- how one sign-in reaches every environment without the front holding the environments' tokens;
- what `iss`, `aud` and `sub` mean, so that a relying party can pin them and a token for one party is useless to another;
- where multi-factor authentication is demanded.

Some constraints come from outside.

- **OpenID Connect requires `iss` to be an https URL**, and discovery lives under it at `/.well-known/openid-configuration`.
- **Not every relying party accepts an issuer with a path.** An issuer URL with a path would have to be verified at each one. (inferred)
- **A passkey belongs to a relying party ID**, a domain that the page's origin must equal or sit under. A browser lets a page use only its own registrable domain or a parent of it, short of a public suffix.
- **AWS IAM OIDC providers check `aud` against a list of client IDs.** The default audience `sts.amazonaws.com` is the same for every account.

Tokens are standard JWS and JWT, signed as ADR 0010 says, with keys that sign in place and are published through a JWKS ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)). The actors are ADR 0007: a slim `entry`, `session`, `exchange`, `signer` and `policy`. Claims and their vocabulary are ADR 0025. Passkeys and step-up are [0048](/decisions/0048-passkeys-step-up-and-device-login/).

## Decision

### Issuers

- **Each environment boundary has its own issuer, and the front has a group issuer.** An installation with management, development, staging and production environments therefore has five issuers.
- **Company boundaries have no issuer.** They hold no authority key.
- **A product boundary gets its own issuer when it is created.** None exists now.
- **Each issuer has its own host, and one host is one origin.**
  - The group issuer is `https://sluis.example.com`.
  - A boundary issuer is `https://sluis.<env>.example.com`, for example `https://sluis.prod.example.com`.
  - The issuer URL has no path.
- **A test installation lives outside these names.** It serves no real user and trusts nothing.
- **Discovery and the JWKS are static documents**, one set per issuer, in a bucket of that boundary. The JWKS carries the current and the next key. A relying party can verify a token while the issuer's compute is down.

### Sign-in across boundaries

- **Each boundary runs its own slim `entry@<boundary>`, with no sign-in of its own.** Its upstream is the group issuer.
- **The boundary verifies the person's passkey assertion itself**, against its own challenge. A compromised front cannot act in a boundary that demands a fresh passkey.
- **In a browser, sign-in is transparent.** A relying party in a boundary sends the person to that boundary's issuer, which sends them to the group issuer. A person who is already signed in there comes straight back. A passkey tap is asked for only where that boundary's policy demands fresh multi-factor authentication.
- **On the command line, there is one `sluis login`.** It signs in at the group issuer. For each environment the CLI then exchanges that token silently (RFC 8693) at the boundary's own token endpoint. The front never sees a boundary's tokens.

### `iss` and `aud`

- **`iss` is the issuer URL.** OpenID Connect requires an https URL.
- **`aud` is exactly one registered `client_id`, not a URL.** These rules apply:
  1. `iss` is an https URL, as OpenID Connect requires.
  2. A token has one audience.
  3. A resource indicator (RFC 8707 `resource=`) is mapped to a registered client. It is never copied into `aud`.
  4. Between actors of different boundaries, `aud` is the target's SPIFFE ID, and the call runs over mutual TLS.
  5. Client IDs are unique per issuer and readable, with the same name in every environment: for example `kubernetes`, `argocd` or `aws-<account-alias>`. AWS uses a custom client ID for each account in its IAM OIDC provider, not `sts.amazonaws.com`.
  6. A token exchange into a boundary uses that boundary's client ID at the group issuer.
- **Every relying party pins both `iss` and `aud`.** sluis prints reference configuration for each kind of relying party, and the infrastructure code applies it.

### Subject and claims

- **`sub` is an opaque, stable person key, the same at every issuer.** It is not an email address and not a directory account.
- **The directory account is in a claim of its own, and `email` is separate.**
- **Tokens carry `acr`, `amr` and `auth_time`**, and `cnf.jkt` for a token bound to a DPoP key.
- **A token carries only the grants of its own boundary.**
- **The company is the claim `org`.**
- **Lifetimes are set per issuer.**

### Workload tokens

- **JWT-SVIDs come from the same per-environment issuer.** `iss` is the boundary's URL and `sub` is the workload's `spiffe://` ID. The trust domain is the boundary ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)).

### Multi-factor authentication

- **Multi-factor authentication is decided per client and per environment, by `policy@<boundary>`.**
- **It is enforced by step-up (RFC 9470).** A relying party or the boundary answers `insufficient_user_authentication` with the `acr_values` it needs, and the boundary checks the passkey itself (issue #885, [0048](/decisions/0048-passkeys-step-up-and-device-login/)).

### Passkeys

- **The passkey relying party ID is the registrable parent domain**, `example.com` in the public examples. `sluis.<env>.example.com` is not under `sluis.example.com`, so a single passkey that works at every issuer needs the common parent.
- **Every verifier keeps a strict allow-list of origins:** the sluis hosts and nothing else.

## Consequences

- **A signer compromise stays in its environment.** A development signer cannot mint a token that a production cluster, account or application accepts, because each relying party pins its own environment's issuer.
- **There are five issuers, five key sets and five sets of static documents to publish and rotate**, and a DNS name and a TLS certificate for each host.
- **Applications that serve every environment trust the group issuer.** Everything else trusts its own environment's issuer. (inferred)
- **The CLI keeps a token per issuer under one sign-in.** A person signs in once and the exchanges are silent.
- **The front cannot read or replay a boundary's tokens**, because they are minted by the boundary at its own token endpoint and never pass through the front.
- **An outage of the front stops new sign-ins.** Tokens that a boundary has already issued keep verifying against its static JWKS until they expire. (inferred)
- **A token is useful to exactly one relying party.** A party that receives a token cannot present it to another, and a token for one AWS account is refused by another account's provider. The cost is a client registration for every relying party, in every environment where it runs.
- **Relying-party configuration is generated and pinned, not hand-written.** A party that cannot pin `aud` to a client ID is an integration problem to solve before it is connected. (inferred)
- **`sub` links one person across issuers** without exposing an email address or a directory account, and it does not change when either of them does. Linking a person's accounts across companies is still a declared person link, with no grant inherited across it (ADR 0009).
- **A broad passkey relying party ID is a trade-off.** Every page under the parent domain can ask a browser for an assertion for it, so the origin allow-list in every verifier is what keeps a page outside sluis from using one. Moving sluis to another parent domain means new passkeys. (the second part is inferred)
- **Workloads and people share an issuer per environment**, so a relying party such as a cluster's API server or a cloud's federation trusts one issuer per environment for both. The workload tokens that `sluis-node` gives its actors for cloud credentials come from the same boundary issuer, whose key set holds one key per node card; per-node issuers are optional, only for fleets whose nodes run different actors ([0012](/decisions/0012-machine-identity-follows-spiffe-proofs-are-exchanged-for-short-lived-credentials/)).
- **The identity and federation pages and the standards register** (OpenID Connect discovery, RFC 8693, RFC 8707, RFC 9470, SPIFFE JWT-SVID) follow this record.
- **Threats addressed:** T-2, T-3, T-5, T-6 and T-21, see the [threat model](/security/threat-model/).

## Roads not taken

### One issuer for every boundary

- Pros: every relying party is configured the same way. One key set to publish. It is what v1 does.
- Cons: one key is accepted everywhere, so a compromise or a policy bug in one environment mints tokens for all of them.
- Why not: environment boundaries exist so that development cannot reach production. A shared issuer would undo that for tokens.

### An issuer per environment as a path under one host

- Pros: one host and one certificate. A passkey relying party ID could be the sluis host itself, narrower than the parent domain.
- Cons: an issuer with a path must work at every relying party, which has to be checked one by one. Several issuers share one origin, so its cookies, storage and content-security policy are shared too. (inferred)
- Why not: the maintainer chose one origin per host. Every issuer then has a plain URL, and its pages are isolated by the browser's origin rules.

### Boundary hosts under the group host (`prod.sluis.example.com`)

- Pros: a passkey relying party ID of `sluis.example.com` would cover every issuer, so the parent domain would not be needed.
- Cons: the hosts would not follow the per-environment naming that the rest of each environment uses, and each environment's DNS would hang under the group's name. (inferred)
- Why not: the hosts follow `sluis.<env>.example.com`. The wider relying party ID is accepted, with a strict origin allow-list in every verifier.

### An issuer per company

- Pros: each company's people get tokens from their company's issuer.
- Cons: applications shared by every company accept one issuer, so they break. It does not stop a development key from minting production tokens.
- Why not: companies own the identity plane, not authority keys. The company is a claim (`org`) and a policy scope.

### `aud` as a URL, or the resource indicator copied into `aud`

- Pros: the audience says where the token is used. Some relying parties expect a URL.
- Cons: one party gets two spellings, its client ID and its URL. A URL can serve several parties behind one gateway, so a token for one is accepted by the others. (inferred)
- Why not: a client ID is registered, unique per issuer and pinned. The resource indicator chooses the client and stops there.

### Several audiences in one token

- Pros: one token reaches several services, with fewer exchanges.
- Cons: every party named in it can replay it to the others.
- Why not: one audience makes a stolen or forwarded token useless beyond its one party. Token exchange is cheap.

### `sts.amazonaws.com` as the audience for AWS

- Pros: the AWS default, with no client ID to register in each account.
- Cons: the audience is the same for every account, so a token minted for one account is accepted by every other account that trusts the same issuer.
- Why not: a custom client ID per account makes the account part of the token.

### `sub` as the email address or the directory account

- Pros: readable, and familiar to relying parties.
- Cons: it changes when a person is renamed or moves company, and it differs between a person's directory accounts. It puts personal data into every log line that records the subject. (the last part is inferred)
- Why not: an opaque key is stable across issuers and renames. The readable identifiers are in claims of their own.

### A separate issuer for workloads

- Pros: people's and workloads' tokens never share a key. (inferred)
- Cons: a second issuer, key set and discovery document per environment, and relying parties that accept both would need two trust entries. (inferred)
- Why not: the trust domain is the boundary, and the boundary's issuer serves both. `sub` tells a person from a workload.

### Multi-factor authentication once, at the front

- Pros: one place to configure. One tap per sign-in.
- Cons: a compromised front could claim a fresh authentication anywhere. Environments differ in what they need.
- Why not: each boundary decides for its clients and checks the passkey itself, so production can ask for a fresh tap where development does not.
