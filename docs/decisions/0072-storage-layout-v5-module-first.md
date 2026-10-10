# 0072 — Storage layout v5: module first, a table per module, and a one-time migration

**Status:** Accepted (2026-10-09). Refines point 8 of [0071](0071-least-privilege-modules-one-credential-holder-per-function.md)
(state, secrets, keys and blobs are per module) and amends its points 1, 5, 9 and 11; builds on
[0041](0041-the-secret-contract.md) (the secret contract) and [0031](0031-a-generic-migration-tool.md) (a generic
migration tool).
**Date:** 2026-10-09

## Context

Layout v4 keeps all State in one DynamoDB table (`pk` is the kind, `sk` the id), and all secrets under
`internal/config/...`, `internal/credentials/<kind>/<id>/<ref>` and `external/<kind>/<id>`. Every process reads the
whole `internal/config` subtree at start and again every five minutes. That shape cannot express
[0071](0071-least-privilege-modules-one-credential-holder-per-function.md): a role that is allowed one module's data is
allowed all of it, because the table and the parameter prefix are shared. Three kinds of GitHub App (link, catalogue,
runner) and a `directory` kind that the Google module owns add names that say how the code grew, not what the data is.

## Decision

1. **A table per module.** Each module owns one DynamoDB table, on demand. The modules are `oidc` (the issuer, the
   signer's ring and the console, which stay together), `github`, `slack`, `cloudflare`, `google` and `backup`. The
   default name is `sluis-<instance>-<module>`, which matches the SSM root `/sluis/<instance>` and the key aliases
   `alias/sluis-<instance>-...` and makes the IAM resource pattern greppable. An installation may name each table
   itself; the library default is a default, not a rule. Every table carries its own `lease`, `notify` and `maintenance` kinds. A
   process opens its own module's table for writing, and a named peer's table for reading where a cross-grant says so
   (the issuer reads the `google`, `github` and `slack` tables); writing a key of another module is a typed error.
2. **Kinds lose their module prefix.** The table already says the module, so `github-org` is `org`, `slack-workspace`
   is `workspace`, the three GitHub App kinds are one kind `app` keyed by the App id, and `directory` is `workspace` in
   the `google` table. The App record has a purpose (`link`, `catalogue` or `runner`) and labels; an organisation
   record references an App id and holds the installation and removal-set state, so a key is stored once and rotated in
   one place. `google` is the name in storage, paths, IAM and documents. `Directory` stays only as the name of the Go
   interface and of the console page.
3. **Secrets are module first.** `internal/<module>/<name>` and `external/<module>/<name>`, with no `config` or
   `credentials` level in between. The names:
   - `internal/oidc/signin/google/client-id` and `.../client-secret`, `internal/oidc/recovery-password`,
     `internal/oidc/state-secret`, `internal/oidc/console-session-key`, `internal/oidc/clients/<id>`;
   - `internal/github/apps/<id>/<ref>`, `internal/github/links/<user>/<ref>`, `internal/github/orgs/<org>/<ref>`;
   - `internal/slack/workspaces/<team>/<ref>`, `internal/slack/apps/<id>/<ref>`;
   - `internal/google/workspaces/<id>/key`;
   - `internal/cloudflare/<account>/minter`, unchanged because it is already module first;
   - `internal/backup/...` for the backup key references;
   - `external/oidc/<client>` is byte-for-byte what v4 writes, so External Secrets documents do not change.
   A random `<ref>` is kept per write where a credential must not be replaced in place.
4. **Secrets are read by name, when needed.** The source reads one parameter at a time, caches it for a short TTL
   (60 seconds by default), shares one in-flight read per name and still fails closed after `MaxStale`. Constructing a
   module makes no SSM or KMS call. A name a document declares but does not exist is found by `sluis check` and by a
   post-deploy invocation, not by the first user.
5. **The KMS signing context does not change in v1.75.** The wrapped key ring is encrypted with `{instance, purpose}`.
   Adding `module` to the context would make a copied ring undecryptable, so a fresh ring and a new key set that
   relying parties have cached. The module joins the context when the signer's key is split from the issuer. SSM
   parameters are already bound by parameter ARN, so the isolation of secrets does not wait for it.
6. **v1.75 reads both layouts.** `secrets.layout` is `v4` or `v5`, and `ports.dynamodb` takes `table` (v4) or `tables`
   (v5). A mixed configuration is refused. This is what lets an installation deploy the new infrastructure, migrate,
   flip and, if it must, flip back with the old resources untouched. The v4 reader, the v4 adapter and the migration
   below are deleted in v1.77.
7. **One migration, run once per installation.** `sluis migrate v5` has `plan`, `copy` and `verify`:
   - `plan` opens the v4 source and the v5 destination and prints, per module, what is new, same, different or refused,
     with names and versions and never a value;
   - `copy` is idempotent and stops on a conflicting destination value unless `--overwrite`; it needs
     `--i-have-stopped-writers`. Leases, `notify`, caches, dedupe records and snapshots start fresh; blob snapshots are a
     cache and are not copied;
   - it runs in **two passes**: a live pass (`--skip issuer`) for the durable domains and secrets, then a short frozen
     pass for the issuer's State and the flip, which re-plans so that writes between the passes are copied again;
   - **sessions, refresh tokens, single sign-on records and the key ring are copied** with their remaining lifetime,
     the wrapped ring entries verbatim (same context, point 5) and the state-secret fingerprint record, so users and
     agents stay signed in, a spent refresh token stays spent and the key set does not change;
   - `verify` re-reads both sides and compares per module; secrets are compared by a hash that does not depend on the
     version.
   The old table and the old parameters stay for **7 days** after the flip and are then deleted; backups taken before
   the cutover are kept as long as the installation's backup rule says. Rolling back inside the window is a
   configuration change back to `v4`.
8. **Binaries and functions (amends 0071 points 1, 5 and 11).** The signer and the console stay inside the issuer. There
   are six binaries and seven functions:

   | Binary | Function(s) |
   |---|---|
   | `sluis-issuer` (issuer, signer, console) | `sluis-issuer` |
   | `sluis-cloudflare` | `sluis-cloudflare` |
   | `sluis-github` | `sluis-github` |
   | `sluis-slack` | `sluis-slack` |
   | `sluis-google` | `sluis-google` |
   | `sluis-backup` | `sluis-backup` and `sluis-restore` |

   Restore is the second deployment of the backup binary, chosen by the function's configuration and enforced by its
   role, and only an administrator or the break-glass role may invoke it. The console has no restore button. The
   module of a zip is pinned when it is built. Cloudflare is already a function of its own; the provider modules leave
   the issuer's function one per release, from v1.76. Until a provider leaves, it runs inside the issuer's function and
   that function's role is the union of the roles of the modules it hosts, built from per-module statements so that each
   split only removes some.
9. **Calls between modules** carry a protocol version and a verified caller. On AWS the caller is the alias the callee
   was invoked through, never a field the caller writes; a method lists the callers it allows. A callee accepts the
   previous version as well as its own.

## Consequences

- The isolation of 0071 becomes a property of an ARN: a role names its module's table, parameter prefix and blob
  prefix, and the deny-matrix test generated from the module list proves that no role names another's.
- A typo in a secret name now surfaces at the first request that needs it, not at start. `sluis check` and the
  post-deploy invocation must therefore ship in the same release.
- Reading a peer's table is a named cross-grant, read only. A module that needs more calls the peer.
- A migration exists for exactly one release train. It is tested against a fixture of a full v4 installation, including
  the rollback.
- Breaking: the layout, the table configuration and the secret names change; the upgrade page for v1.75 carries the
  runbook ([0007](0007-breaking-changes-inside-1x.md) applies).

## Alternatives considered

- **Table names `<instance>-<module>`.** Shorter, but anonymous in a shared account and able to collide with other
  applications' tables. Kept as an override.
- **Reset sessions and the ring.** Simpler, but every person and unattended agent signs in again and relying parties
  see a new key set.
- **Add `module` to the KMS context now.** Stronger isolation sooner, at the cost of the outage above. Deferred to the
  signer split.
- **A single frozen window for the whole copy.** Two to three times as long as the two-pass copy.
- **Dual writes with no freeze.** Every writer would have to know both layouts.
- **Keeping the module name in the kind.** Redundant in a table per module and the source of the three App kinds.
- **Folding organisations into the App record, or keeping an organisation's key under its own path.** Either stores a
  key in two places or rotates it in two.
