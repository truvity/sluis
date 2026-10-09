# Survive a cold-start herd

Keep sluis on Lambda serving when many clients return at once.

## Before you start

- Each new environment reads SSM at start. SSM throttles a herd, and a failed start answers 500, which clients retry.

## Steps

### 1. Cap the concurrency

```go
cap := 20
args.Function = sluispulumi.FunctionArgs{ReservedConcurrency: &cap}
```

### 2. Raise the SSM throughput

```sh
aws ssm update-service-setting --setting-value true \
  --setting-id arn:aws:ssm:<region>:<account>:servicesetting/ssm/parameter-store/high-throughput-enabled
```

| Setting | Costs | Bounds |
|---|---|---|
| Reserved concurrency | Nothing; not provisioned concurrency | Environments, so cold starts and SSM reads |
| SSM higher throughput | Every SSM call in the account and region | The SSM rate before it throttles |
| Release v1.74.1 or later | Nothing | Reads per start: the session key and the configuration. Each is retried within 3.4 seconds |

## Verify

The function's `Throttles` metric rises in a herd, and no log line says `sluis could not start`.

## Roll back

Unset `ReservedConcurrency`, and set the SSM setting to `false`.
