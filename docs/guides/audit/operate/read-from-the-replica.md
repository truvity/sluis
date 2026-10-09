# Read the archive from the replica

Keep verifying and querying the trail when the primary bucket's region is unavailable.

## Before you start

- Replicate the bucket across regions to another account, with Object Lock and replicated retention metadata. See [prepare the bucket](prepare-the-bucket.md). You need read access to the replica.

- Reads only: the writer keeps writing to the primary and withholds acknowledgements until it can reach it.

- Verification against the replica is as good as against the primary: seals replicate with the objects they cover.

## Steps

1. Point `audit verify` and the query service's `archive.bucket` at the replica.

   ```
   audit verify --profile <p> --last 24h --bucket <replica> --prefix <prefix>
   ```

## Verify

The report matches the primary's. `audit conformance` passes against the query service. When the primary returns, run `audit verify` over the outage window to confirm replication caught up.

## Roll back

Point the readers back at the primary.
