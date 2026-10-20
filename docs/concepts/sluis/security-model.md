# What does sluis protect, and how?

sluis protects three things: the decision about who holds a token, the signing key behind every token, and the credentials its modules hold. Report a vulnerability as [SECURITY](../../../SECURITY.md) says.

## What it trusts

sluis verifies proofs produced elsewhere: a directory sign-in, a platform's job token, a cluster's ServiceAccount token. It holds no users and no MFA. Two accounts sign in with a password, the recovery account and a conformance test account. SRP-6a proves it, so the password never reaches the service ([recovery](recovery.md)). A removal acts only on an authoritative answer ([failure semantics](failure-semantics.md)).

## A browser sign-in

| Boundary | What holds it |
|---|---|
| The request belongs to the browser that started it | `/authorize` sets a cookie that holds a MAC of the request id. Another browser cannot finish it |
| A code needs its verifier | PKCE is required for public clients, and per client with `pkce: required`. A code issued against a challenge is redeemed with its `code_verifier` |
| A redirect is a registered redirect | A native client's loopback redirect differs in the port only |
| Sign-out needs a click | `/logout` and `/end_session` ask first, unless the `id_token_hint` names the signed-in person |
| The page cannot be framed or sniffed | Every response carries a content security policy, `X-Frame-Options: DENY` and `nosniff`; HSTS is sent on https |
| A client's own document is untrusted | It is fetched from an allowed origin to public addresses only, and it may declare no client secret |

A Google sign-in needs a verified address, and `google.oauthClient.hostedDomains` limits the domains. The full list is [endpoints: hardening](../../reference/sluis/endpoints.md#hardening).

## A token

- A repeated security parameter is refused.
- `/revoke` ends only what the authenticated client was issued.
- A refreshed ID token keeps its `acr` and `amr`.
- An exchange is decided by policy: a GitHub Actions job can be pinned to numeric owner and repository ids with `exchange.github.ownerIds` and `repositoryIds`.

## Keys and credentials

Each module holds only its own credentials, and the boundary sits in IAM per role ([ADR 0071](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md)). On AWS the signing key's control plane belongs to named administrator and break-glass roles. Every SecureString lives under the installation's key ([signing on AWS](signing-on-aws.md)).

## Backups and the audit trail

A backup is age-encrypted. A restore checks the backup against the sealed audit trail and compares its recipients with `backup.recipients` ([back up and restore](../../guides/sluis/operate/back-up-on-layout-v5.md)). A refused audit read is recorded. A record is held to the source its caller is listed for ([audit](audit.md)).

## Releases

A release carries keyless signatures, provenance and SBOM attestations, and signed checksums. The Pulumi library verifies the checksums' signature before it trusts a digest ([verify a release](../../guides/sluis/operate/verify-a-release.md)).

## Decided in

- [ADR 0071: least-privilege modules, one process per credential holder](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md)
- [ADR 0075: recovery is turned off from the console and on by configuration](../../decisions/0075-recovery-is-turned-off-from-the-console-and-on-by-configuration.md)
- [ADR 0076: password sign-in is SRP for recovery and conformance only](../../decisions/0076-password-sign-in-is-srp-for-recovery-and-conformance-only.md)
