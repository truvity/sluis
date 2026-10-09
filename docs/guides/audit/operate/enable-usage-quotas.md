# Enable usage quotas

Switch on the usage extension, a second consumer that counts a tenant's meter into a cache with an hourly reconciler, in a stream-mode installation. How it works: [usage quotas](../../../concepts/audit/usage-quotas.md).

## Before you start

- Run [stream mode](run-stream-mode.md): without a stream a second consumer has nothing to read.

- Declare a `meter:` in the catalogue, as in [enable billing](enable-billing.md). What is capped is what is billed.

- Run a cache (Valkey or compatible) and a decision point of your own, such as the gateway's external authorisation.

- `extensions.quotas` takes `enabled` only and renders nothing yet. A `cache:` or `period:` key fails the values schema. The chart refuses it without `mode: stream`.

- This is not burst rate limiting. Decide "fifty requests a second" in the gateway's rate limiter.

- Fail open and say so. A cold cache means the tenant used nothing: serve the customer and record that you decided without a count. The reconciler corrects it within the hour.

## Steps

1. Enable it in a stream-mode installation.

   ```yaml
   audit:
     mode: stream
     extensions:
       quotas:
         enabled: true
   ```

2. Declare a refusal as an `async` action in the application's catalogue, so "you cut me off" has an answer.

## Verify

`helm template` renders, and refuses with `mode: direct`.

## Roll back

Set `enabled: false`.

## Watch

Watch the usage consumer's lag apart from the writer's, the reconciler's drift and denials. Growing drift means the consumer is missing records. A sudden rise in denials is an attack or a misconfigured plan.
