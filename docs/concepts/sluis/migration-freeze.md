# The migration freeze

`sluis maintenance on` freezes an installation for a migration without taking the issuer offline. The deploy that ends the migration signs in through the same issuer, so the issuer has to keep answering.

## What the flag does

The flag is the maintenance item U27 added to each table ([ADR 0072](../../decisions/0072-storage-layout-v5-module-first.md)). `on` sets it, in the state `migration`, in the one table of layout v4 or in every module's table on v5.

The command returns once every replica has read the flag, 15 seconds at most.

While it is set, the source refuses every write of state with `503` and `Retry-After: 30`. That covers sign-in, codes, revocation, sign-out, client secrets, refresh rotation, the console's changes and the controllers' ticks. Clients retry. No session is dropped.

## What keeps answering

- Discovery, the key set, userinfo, introspection, the grants listing and the console's reads.
- `POST /oauth/token` for a token exchange. A CI job, a workload, an AWS role or a person with a recorded sign-in trades its proof.
- `POST /oauth/token` for a refresh. It returns the refresh token it was given, unrotated, so the token the copy carries stays the live one.
- The `{"kind":"check"}` event on Lambda, which reads no state.

An exchange and a refresh write nothing. The access token they mint is a signed JWT that verifies by the key set, and it is not recorded. The copy carries the issuer's records. A record written after the final pass would be missing from the destination.

So `sluisctl token`, `sluisctl aws` and `sluisctl kube-token` work, and so do the Pulumi program and the AWS provider. One case waits. A person whose cached access token has expired gets a new one but cannot exchange it. An exchange needs the recorded token. Run `sluisctl aws` once before the freeze.

## What stays refused

A code redemption, a device code, the installation tokens of GitHub and Cloudflare, and every other write. The issuer has no client-credentials grant. A restore's flag, state `restoring`, refuses the token endpoint too.

## On Lambda

Do not set the reserved concurrency to 0. A throttled function cannot sign the deploy in. The AWS provider also retries a throttled check for minutes. The freeze changes nothing on the function.

## The migration copy

`sluis migrate v5 copy` accepts a frozen source as the proof of a stopped writer. It first waits until every replica has seen the flag. It warns when only `--i-have-stopped-writers` vouches for it. It never copies the flag, so the destination starts out of maintenance, and it refuses a destination that holds one.

## The operator grant

The function roles are denied the write of the maintenance item. The operator's own role writes it:

```go
st, err := sluispulumi.MaintenanceOperatorStatements(tableArns, tableKeyArn)
```

It allows `GetItem`, `PutItem` and `DeleteItem` on the `maintenance` partition of the tables, and the table key through DynamoDB. It allows no scan, query or update, and no other item.

## Lifting it

`sluis maintenance off` lifts the flag. Lift it on the source only once you will not roll back. A flag a restore holds needs `--force`, and only when the restore is abandoned. Each change records `roster.maintenance.changed` in the audit trail, and the record is delivered before the flag changes. `sluis maintenance status` shows the flag of each table, and the console banner shows it too.

## Audited, or refused

`on`, `off`, `sluis migrate v5 copy` and `sluis migrate v5 prune` change an installation from a workstation, so each is audited synchronously. The record is delivered to the audit destination before the change. If delivery fails, the command refuses and names the queue and the principal AWS denied. Nothing has changed.

`--no-audit` is the break-glass. The command goes ahead, prints a warning, and writes the same record to stderr as JSON. Keep that line and report it.

The operator's role needs `sqs:SendMessage` on the audit queue. The Pulumi library renders it with `OperatorAuditStatements(queueArn)`.
