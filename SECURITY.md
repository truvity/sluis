# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it privately via
[GitHub Security Advisories](https://github.com/truvity/sluis/security/advisories/new).

Do NOT open a public issue for security vulnerabilities.

## Supported Versions

Only the latest release is supported with security updates. One repository, one
version: sluis and audit are released together.

| Version | Supported |
|---------|-----------|
| latest  | yes       |
| older   | no        |

## Design properties that matter for reports

Each property links the page that owns it; this file does not restate them.

### sluis

- It **authenticates nobody**: sign-in stays with the corporate directory, and sluis
  verifies proofs produced elsewhere. It holds no passwords, no users and no MFA.
  [trust](docs/concepts/sluis/trust.md)
- It trusts **exactly two anchors**, the cluster and the issuer, and never a third.
  [trust](docs/concepts/sluis/trust.md)
- It holds directory credentials so that its consumers hold none, and is **read-only
  against the directory**. Its write surfaces are GitHub and Slack, acting only in the
  organisations and workspaces the policy names. [safety](docs/concepts/sluis/safety.md)
- Removals act only on an **authoritative** answer; a failed probe, a partial read or a stale
  snapshot reads as "not authoritative", never as "gone", and a controller never removes
  more than half of a set without an operator's confirmation. [safety](docs/concepts/sluis/safety.md)
- A client may describe itself over HTTPS only when `client_documents.origins` allows its
  host (an SSRF surface bounded by that list: 64 KiB, 5 seconds, no redirect).
  [policy clients](docs/reference/sluis/policy-clients.md#clients-that-describe-themselves)
- Every request-derived value is routed through `logattr` (`storage/logattr`) before it is logged.
- The client address in an audit record is read from `X-Forwarded-For` only as far as
  `audit.forwardedForTrustedHops` says: set it to the deployment's own proxies and no more.

### audit

- Records are append-only; the store is expected to carry S3 Object Lock in compliance
  mode. [prepare the bucket](docs/guides/audit/operate/prepare-the-bucket.md)
- Every read of the audit trail is itself recorded.
- Subjects are referenced by identifiers or keyed pseudonyms, never by identity attributes.
  [ADR 0047](docs/decisions/0047-identity-tiers-and-pseudonymisation.md)
- The split writer is the most privileged component: it holds unwrapped pseudonymisation
  keys in memory. Reports about it are especially welcome.
- sluis reaches audit as its own workload; ordinary records never wait on it, and a recovery
  sign-in is refused when its record cannot be kept. [audit](docs/concepts/sluis/audit.md)
