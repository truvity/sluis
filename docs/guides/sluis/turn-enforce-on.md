# Turn groups-scoping enforce on

Narrow every token's and `/userinfo`'s `groups` claim to what [the scoping rule](../../concepts/sluis/groups-in-a-token.md) keeps, for one installation.

## Before you start

- [Read the report](read-the-groups-scoping-report.md) for every audience first. Accept each finding or give it a `groups:` override.

- Enforce is per installation. The chart's default stays `report`.

- `requires` and lifetimes still read the full set a caller holds, so enforcing locks nobody out. A relying party can lose a group it read.

## 1. Set the mode

Set `config.groupsScoping: enforce` in the service document, or the chart's value, and roll out. On Kubernetes that is a rollout. On Lambda it is a new configuration layer. Preview first.

The ID token, the access token (code, refresh and exchange) and the console's own mint all narrow, and so do `/userinfo` answers keyed by them. Verify: sign in to one client of each kind and decode `groups`.

## 2. Find the dropped group

If a relying party loses a role, set `log.level` to `debug`. Enforce logs the finding at DEBUG, because dropping is the steady state. Reproduce the failure and read this line for the audience and subject:

```text
groups scoping (enforce mode): this token dropped groups
```

`dropped` lists the groups the token stopped carrying.

## 3. Add the override

Add the groups the relying party reads to a `groups:` override on the audience's row ([keys](../../reference/sluis/policy-clients.md#groups-override)). A self-described client uses `client_documents.groups`. Roll out and repeat the sign-in. Set `log.level` back to `info`.

Read the logs at DEBUG once after the next monthly or other slow audience has run. Tell owners of relying parties that read groups beyond their `requires`.

## Roll back

Set `groupsScoping: report` and roll out. Tokens already issued keep their narrowed claim until they expire.
