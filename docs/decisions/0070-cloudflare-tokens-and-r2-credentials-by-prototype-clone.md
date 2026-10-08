# 0070 — Cloudflare tokens and R2 credentials: sluis clones a disabled prototype token

**Status:** Accepted (2026-10-08). Follows [0014](0014-minting-third-party-credentials-only-where-membership-is-governed.md)
(a credential is minted only where membership is governed), stores its output under the contract of
[0041](0041-the-secret-contract.md) and grants it to people and agents as [0040](0040-agent-class-sessions.md) sets out.
**Date:** 2026-10-08

## Context

Cloudflare has no STS: no web-identity federation and no short-lived credentials handed out on proof of identity.
A service that needs to edit DNS, or read and write an R2 bucket, holds a long-lived API token or R2 key pair.
sluis already brokers GitHub App installation tokens and signs tokens that AWS roles trust, so it can be the one
place that holds a parent credential and hands out short-lived children. A separate R2 broker and a `sluisctl r2`
wrapper exist today; both cover only R2.

Cloudflare expresses a token's limits as a list of policies (effect, resources, permission groups) plus a
condition and a validity window. There is no standalone policy object. An account-owned token outlives the person
who made it. An R2 token's access key is its id and its secret is the SHA-256 of its value, so a cloned R2 token
is directly an S3 credential.

## Decision

1. **A preset names a prototype.** A preset is `account`, `prototype`, `description`, `lifetime`, `rotation` and,
   for R2, `endpoint`. The prototype is a **disabled** Cloudflare account token whose `policies` and `condition`
   are the preset's rights. The policy language stays Cloudflare's own: the dashboard edits it, Cloudflare
   validates permission names, and infrastructure code can declare it. A hand-made prototype works the same way.

2. **Minting is a clone.** sluis reads the prototype at every mint (a policy edit applies at the next rotation),
   copies its policies and condition into a new account token with `expires_on = now + lifetime`, and names it
   `sluis/<instance>/<preset>/<RFC3339 time>`. For R2 the access key is the token id and the secret is the hex
   SHA-256 of the token value.

3. **Refusals.** sluis refuses at load or at mint: an **active** prototype (it would be a usable, never-expiring
   token); a prototype granting Account API Tokens Edit, Billing, Account Settings, Memberships or Access identity
   providers; `rotation >= lifetime`; and either below one minute. Each refusal is an audit event.

4. **The minter credential.** Per account, `internal/cloudflare/<account>/minter` holds a `cloudflare-minter/v1`
   document `{schema, token}`: an account token with Account API Tokens Read and Edit only. It is internal and
   never granted to anyone.

5. **Two settings, one tick.** `lifetime` is the token's validity; `rotation` is how often a new current token is
   stored. On each tick, per preset, if the current token is older than `rotation`, sluis mints, writes
   `external/cloudflare/<preset>` and deletes that preset's own tokens past `expires_on` (Cloudflare only marks
   them expired). It deletes only names carrying its own `sluis/<instance>/<preset>/` prefix. `lifetime - rotation`
   is the time consumers have to pick up the new token. On Lambda the tick is the EventBridge schedule. An alert
   fires when the last rotation is older than twice `rotation`.

6. **Delivery, both ways.**
   - *Stored:* `external/cloudflare/<preset>` holds `cloudflare/v1` `{schema, token, expires_on}`, or for R2
     `{schema, access_key_id, secret_access_key, endpoint, expires_on}`. Readers get an exact-address grant and the
     external secrets operator projects it with `property:`.
   - *On demand:* a granted caller (a person through a console or `sluisctl` session, a CI job through the GitHub
     OIDC exchange) gets its own token cloned from the same prototype with a lifetime no longer than the preset's,
     named `sluis/<instance>/<preset>/<caller>/<time>`. Audit and revocation are per caller.
   Grants are policy entries: `grants: [{group: …, presets: […]}, {job: github:<org>/<repo>:<name>, presets: […]}]`.

7. **Revoke** is deleting the live token by id from the console or CLI; the next tick mints a replacement for the
   stored one.

8. **CLI.** `sluisctl cloudflare token <preset> [--format env|json]` prints the token.
   `sluisctl cloudflare r2 <preset>` prints AWS `credential_process` JSON
   (`{Version:1, AccessKeyId, SecretAccessKey, Expiration}`); with `--file <path>` it reads the projected document
   and needs no sign-in. Results are cached under `~/.config/sluisctl` and renewed when less than a third of the
   lifetime is left. `sluisctl aws-config` writes one R2 profile per granted preset (`endpoint_url`,
   `region = auto`, `request_checksum_calculation = when_required`, `response_checksum_validation = when_required`,
   path-style addressing, because R2 refuses some SDK default checksums). `sluisctl whoami` lists the granted
   presets. The existing `sluisctl r2` is deprecated in favour of `sluisctl cloudflare r2`.

9. **Pods** receive the rotated document as a file from the external secrets operator and run
   `credential_process = sluisctl cloudflare r2 <preset> --file …`, so the SDK refreshes on `Expiration` without a
   network call. A plain shared-credentials file would go stale because most SDKs read it once.

10. **Audit.** Events in the `security` category: mint (stored or on demand, preset, caller, token id,
    `expires_on`; never the value), refusal (why), sweep (deleted ids) and revoke.

11. **Implementation.** The Cloudflare client sits behind a small interface (`cloudflare-go/v7`
    `accounts.TokenService`) so tests use a fake and CI never reaches Cloudflare.

### Limits to document

| Limit | Value |
|-------|-------|
| API rate | 1,200 requests per 5 minutes per user; exceeding it blocks the API for 5 minutes |
| Account API tokens | 500 per account |
| Scope floor | a zone is the smallest resource a token names; one DNS record cannot be scoped |

At tens of presets, a 15 minute lifetime and a 5 minute rotation, this is about 24 calls per preset per hour and
roughly three live tokens per preset.

### Open points settled by test before the build

The IP condition key (`request.ip` in the documentation, `request_ip` in the SDK); whether the infrastructure
provider can create a disabled token; the R2 bucket resource key in a policy; whether a token-creating token is
bounded by its own permissions or its creator's; and whether expired but undeleted tokens count toward the 500.
Each is held in one constant until tested.

## Consequences

- One credential, the minter, replaces every long-lived Cloudflare token a consumer holds; a leaked child expires
  in minutes and each is attributable by name.
- Rights are edited where Cloudflare edits them. sluis carries no policy language of its own to keep in step.
- The minter can create tokens, so its custody is the new sensitive point; it is internal, never exported, and
  its own permissions are the ceiling on what a prototype may grant.
- A consumer must tolerate rotation: it re-reads within `lifetime - rotation`.
- The scoping limits of Cloudflare are accepted: a subdomain only as its own zone (Enterprise), no per-record
  scope, no bucket prefix scope.
- The separate R2 broker and its chart are retired once this ships.

## Roads not taken

- **Policy in sluis's own language** (a preset as permissions plus zones): rejected; Cloudflare's own policy is
  preferred, and a prototype also covers hand-made tokens.
- **R2 temporary credentials** (bucket and prefix, signed locally, no API call): rejected as the default because
  the scope would be sluis-side configuration. Revisit if a consumer needs prefix scoping.
- **More lifecycle settings** (a consumer lag, a margin): rejected as overkill; `lifetime - rotation` is documented
  instead.
- **A sidecar** answering `AWS_CONTAINER_CREDENTIALS_FULL_URI`: kept only for images that cannot change.
