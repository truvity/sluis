# Security Policy

## Reporting a vulnerability

Report a vulnerability privately through
[GitHub Security Advisories](https://github.com/truvity/sluis/security/advisories/new).

Do not open a public issue, a pull request or a discussion for a vulnerability.

Please include:

- the version or commit you tested, and the branch (`main` or `master`);
- what you did, what you expected and what happened;
- a minimal way to reproduce it;
- what an attacker gains.

Do not include real secrets or personal data. Use test values.

## What to expect

The maintainer is one person. Replies are best effort, and we aim to answer within a week.
We confirm the report, agree on severity, fix it and then publish an advisory. We credit you if you wish.
Please give us reasonable time to fix a problem before you tell anyone else.

## How we fix in private

The fix is prepared out of public view:

1. We open a temporary private fork from the advisory.
2. The fix, its tests and the advisory text are written there.
3. When the fix is ready, we merge it to the affected branches and publish the advisory and the release together. A CVE is requested through the advisory when it applies.

If you want to propose a fix, say so in the advisory. The maintainer can invite you to the private fork.

## Supported versions

| Version | Branch | Support |
|---------|--------|---------|
| v1 | `master` (renamed `v1` at the v2 cutover) | Supported until the maintainers' production deployments run v2. Security fixes go to the latest release only. |
| v2 | `main` | Pre-release (`v2.0.0-alpha.N`). No support promise. Reports are welcome and handled the same private way. |

Older v1 releases are not patched. Upgrade to the latest.
For the design properties that matter to v1 reports, read `SECURITY.md` on the `master` branch.
The two lines are described in [ADR 0001](docs/decisions/0001-v1-and-v2-lines.md).
When v2 becomes the supported line, this table will change in the same pull request.

## Scope

In scope: the code, workflows and published artifacts of this repository.
Out of scope: your own deployment's configuration, third-party services, and findings that need an already-compromised administrator.
