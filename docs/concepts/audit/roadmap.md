# What is planned for audit?

Nothing is queued. R2 is a supported archive store without Object Lock: a preset there is `operational` or `standard`, and `attested` is refused.

## Parked

Three Cloudflare items are parked:

| item | state |
|---|---|
| Cron Worker as the notary over R2 | parked |
| Cloudflare Queues as an ingest transport | parked |
| Cloudflare Queues as the observe change feed | parked |

The target is S3 Object Lock in compliance mode on Lambda and Kubernetes. Cloudflare stays a proxy and DNS in front of an installation.

A store other than S3 would have to meet the [bucket contract](../../reference/audit/bucket-contract.md).

## Decided in

- [0058 Three parts installed independently](../../decisions/0058-three-parts-installed-independently.md)
- [0065 Archive retention and lifecycle](../../decisions/0065-archive-retention-and-lifecycle.md)
