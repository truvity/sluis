# Investigate a failed verification

Work out what a failing `audit verify` or verify job found, and what to record and fix.

## Before you start

- Have the report (`--json` gives data), the bucket, the profile and the range. You need read access to the archive and `s3:ListBucketVersions`.

- Treat it as an incident. Nothing in the archive can be repaired. Record what you found and when.

- `audit verify` does not find a removed or added object by itself. Seals do ([0061](../../../decisions/0061-seals.md)), with `--root` or `seals.roots`.

- The check covers one installation's prefix. A problem under another application's prefix in a shared bucket is not yours.

- An archive written before the v1 layout needs the v0.6.x CLI, which also walks its digest chain.

## Steps

1. Read each finding.

   | the report says (rule: finding) | look for |
   |---|---|
   | `object.sha256`: the bytes do not match the object's `sha256` | a changed object: a new version under the same key (possible only if Object Lock was off or governance when written; compare with `aws s3api list-object-versions`), or damage in storage or transit |
   | `record.hash`: a record's hash does not match | a line altered inside an object, or an object not written by the writer |
   | `key.grammar`, `object.metadata`, `record.placement` or `object.body`: a malformed key, metadata or line, or a record under another profile or tenant | an object put by something other than the writer, or a copy that lost its user metadata (`format`, `sha256`, `count`) |
   | `object.count`: a different number of records than its `count` | the same: the writer sets both |
   | the lock ends sooner than the profile requires | a shorter retention than the profile asks: check the writer version and the profile then (`schema/profile/<name>/`) |
   | `seal.root`: an object was added, removed or changed after its hour was sealed | the finding shows the seal's count and the hour's count side by side |
   | `seal.missing`: an hour has no seal | a stopped notary (`AuditSealStale`), then a removed seal: the bucket's access log and versioning say who |
   | `seal.signature`: signed by a key the verifier does not pin, or a lapsed or revoked delegation | take it seriously: signing as a pinned root is the one thing the bucket's writer cannot do |

2. Preserve the evidence. Place a legal hold on the affected prefix if it may be needed: see [place a legal hold](place-a-legal-hold.md).

3. Fix what let it happen: a role with a delete, a bucket without the policy, or a copy that lost its metadata.

## Verify

You can say which rule failed on which object. Re-run `audit verify` over the range and record the incident where your deployment keeps them.

## Roll back

None. Verification only reads.
