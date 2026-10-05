# Turn groups-scoping enforce on

## Purpose

Narrow every token's and `/userinfo`'s `groups` claim to what [the scoping rule](../explanation/groups-in-a-token.md)
keeps, for one installation.

## Preconditions

- You have [read the report](read-the-groups-scoping-report.md) for every audience this installation serves, and each
  finding was either accepted (the dropped groups are truly unused) or given a `groups:` override.
- Enforce is per installation. Turning it on here does not turn it on elsewhere, and the chart's default stays `report`.

## Before you start

- **`requires` and lifetimes do not change.** Both still read the full set a caller holds, so enforcing cannot lock
  anyone out; what it can do is make a relying party stop seeing a group it used to read.
- **A missing role is the one regression this mode can cause**, and its fix is a `groups:` override on the audience's
  row, never code.
- **A policy or configuration change is a new instance.** On Kubernetes it is a rollout; on Lambda it is a new
  configuration layer. Preview before every apply, and read the preview.
- **Enforce logs at DEBUG.** The same finding report logged at INFO is logged at DEBUG under enforce, because a dropped
  group is then the steady state. To diagnose, you must raise `log.level`.

## Steps

### 1. Set the mode

**Run**: set `config.groupsScoping: enforce` in the service document (the chart's `config.groupsScoping` in values mode,
or the document you render) and roll out.

**Expect**: tokens, and `/userinfo` answers keyed by those tokens, carry only the kept groups. The ID token, the access
token (code, refresh and exchange alike) and the console's own mint all narrow.

**Verify**: sign in to one client of each kind and decode the token's `groups`.

**Rollback**: set `groupsScoping: report` and roll out. Tokens already issued keep their narrowed claim until they expire.

### 2. If a relying party loses a role, find which group

**Run**: set `log.level` to `debug`, reproduce the failure, and read the line `groups scoping (enforce mode): this token
dropped groups` naming that audience and subject. `dropped` is exactly the groups the token stopped carrying.

**Expect**: the group the relying party reads is among `dropped`.

**Verify**: after step 3, the same sign-in carries the group and the failure is gone.

**Rollback**: set the level back to `info`.

### 3. Add the override

**Run**: add whichever dropped groups the relying party reads to a `groups:` override on that audience's row (a
client's, a resource's, or `client_documents.groups` for a self-described client) and roll out
([keys](../reference/policy-clients.md#groups-override)).

**Expect**: the audience carries those groups again.

**Verify**: repeat the sign-in.

**Rollback**: revert the override and roll out.

## Afterwards

Check `/userinfo` as well as the token: it narrows by the access token's own audience, so a relying party that reads it
sees the same set. Tell the owners of relying parties that read groups beyond their `requires`. Re-read the logs at
DEBUG once after the next monthly or otherwise slow audience has run.
