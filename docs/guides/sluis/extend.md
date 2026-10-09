# Extension points

Eight places accept something new, each behind an interface with one implementation. A new piece ships with its fake and its acceptance scenario.

| Extension | Where | Ship with |
|---|---|---|
| Directory backend (Entra, LDAP) | implement `Backend` in `backend/`; announce it in `backendOpeners` in `internal/app`; `backend/google` is the example | fake, a runbook under `docs/guides/sluis/connect/`, connect, revoke and domain-move scenarios |
| Proof kind | implement `issuer.Verifier` in `internal/verify`; add a `policy` matcher kind if the proof carries new attributes | fake issuer minting real signatures, refusal cases, a policy test |
| Matcher kind | a `Matcher` over a verified proof's claims in `policy/` | a policy test |
| Middleware adapter | `net/http` ships; see `internal/identity` | a test against `Verified` |
| Relying-party recipe | a page under `docs/guides/sluis/connect/` | the page |
| CLI subcommand | `cmd/sluisctl` | a test with an ambient token and no cache |
| Audit action | [change the audit catalogue](change-the-audit-catalogue.md) | a constructor in `internal/audit/events.go` |
| Reconciler | `internal/rails` | see below |

## Proof kind

A token this verifier does not own returns `issuer.ErrUnverified`, so the next verifier may try it. A token it owns and rejects is final. Configure the trust boundary (an owner list, an audience) and refuse to run without it, because any public-platform token proves only that some job ran. A verifier adds a proof to the issuer's anchor and never adds an anchor to a service ([trust](../../concepts/sluis/trust.md)).

## Middleware adapter

Read the token with `identity.TokenFrom` or the framework's accessor, ask each `identity.Verifier` in turn, set `identity.WithVerified` in the framework's context, and serve `identity.WhoAmI` at `identity.WhoAmIPath`.

## Matcher kind and policy tables

The [policy tables](../../reference/sluis/policy.md#the-tables) are the whole schema. A need a new group, client or matcher kind cannot meet is a new dimension, and the answer is no.

## CLI subcommand

Share the login cache, issuer client and ambient-token detection. Work with an ambient platform token and no cache. The release's Nix flake ships it to jobs.

## Reconciler

GitHub and Slack are reconcilers built on `internal/rails` pieces, not a framework. Keep the decision, change calls, credentials, status document and audit actions in the system's own package. Ship a fake of the system (`internal/slackapp/slackfake`) and a pure `reconcile` package. Add a status document, a `controllers.<kind>` section, an enabled list born dry-run, audit actions and a connect page. The controller runs inside `sluis serve` and needs no Deployment.

Decided in: [ADR 0024](../../decisions/0024-reconciler-rails-are-shared-pieces-not-a-framework.md), [ADR 0037](../../decisions/0037-one-process-everywhere.md).
