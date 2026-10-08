# Prepare the bucket

## Purpose

Have a bucket, on the right tier, with a prefix for this installation, that the
writer, the indexer, the query service and the verify job can use.

## Preconditions

- Which tier the profiles need ([which profiles to compose](../explanation/which-profiles-to-compose.md)).
- An environment that owns the bucket: one bucket per environment, with Object
  Lock, versioning, replication and a deny-delete policy configured once, and one
  prefix per application, `audit/<application>/`
  ([0053](../../decisions/0053-one-installation-per-service-or-product.md)).

## Before you start

- **Object Lock can only be turned on when a bucket is created** (or later, one
  way, on a versioned bucket on AWS). A bucket made without it cannot be a
  `record` tier bucket by editing; see [AWS turn on the lock](aws-turn-on-object-lock.md).
- **The writer refuses to start when a composed profile demands a stricter lock
  than the deployment writes with**, naming the profile and both modes. The first
  rollout is where that shows.
- **`prefix: ""` puts an installation at the bucket's root.** That is for a bucket
  with exactly one installation in it and nothing else, and it forecloses a second.
  A prefix is chosen when an installation is created and not changed afterwards:
  moving it moves every key.
- **`schema/` is easy to miss.** The writer reads it back on every start; a policy
  that lets it put and not get produces a writer that writes one object, takes a
  403 and dies in a loop, which reads as a broken archive rather than a missing verb.
- **Static S3 credentials are a secret**, named by `credentialsSecret` and found
  through the `secrets` block; they are never an environment variable the config
  names in version 2 ([configuration](../reference/configuration.md#secrets)).
- **Do not set a default retention longer than the shortest profile.** The writer
  sets each object's own.

## Steps

### Choose the tier

The bucket is on one of two tiers
([0056](../../decisions/0056-lock-modes-and-store-tiers.md)), and which one is the
profiles' decision, not the operator's:

| tier | the preset | the store must answer | enough for |
|---|---|---|---|
| **record** | `attested` (compliance Object Lock; `governance` only for the trial of the lock) | `PutObject` with the Object Lock headers, `PutObjectRetention`, `PutObjectLegalHold`, `GetObject`, `HeadObject`, `ListObjectsV2`, presigned `GetObject` | every profile |
| **no lock** | `operational` or `standard` | `PutObject`, `GetObject`, `HeadObject`, `ListObjectsV2`, presigned `GetObject` | profiles composed only from framework profiles that demand no lock: `security`, `history`, `billing-nl` |

### Create the bucket

On the record tier:

```sh
aws s3api create-bucket --bucket example-audit \
  --create-bucket-configuration LocationConstraint=eu-example-1 \
  --object-lock-enabled-for-bucket
```

On the no-lock tier, the same without `--object-lock-enabled-for-bucket`: it is the
bucket of an `operational` or `standard` preset in the deployment document.

The bucket needs:


- Versioning enabled. Object Lock enabled at creation with a **default
  retention in compliance mode** equal to the shortest profile's
  retention; the writer sets longer per object.
- SSE-KMS with a customer-managed key whose policy allows the writer to
  encrypt and the query service and the verify role to decrypt,
  and nobody to schedule deletion of the key without a break-glass role.
- Public access blocked. Bucket policy denies `s3:DeleteObject`,
  `s3:DeleteObjectVersion`, `s3:PutBucketObjectLockConfiguration` changes
  that shorten, and `s3:BypassGovernanceRetention` to everyone; denies
  `s3:PutObjectLegalHold` removal except to the break-glass role with MFA.
- Server access logging or data-event trail enabled on the bucket, so
  direct reads are logged even when they bypass the query service.
- Cross-region replication to a bucket in another region and account with
  Object Lock, with replication of retention metadata.

None of that is per installation. An application arriving in the environment
gets a prefix and its own roles, and changes nothing about the bucket.

Expected: `aws s3api get-object-lock-configuration` (record tier) shows `Enabled`.
Verify: a put of a test object under `audit/<application>/` succeeds and a delete
is refused. Roll back: an empty bucket can be deleted; one with versions under
compliance retention cannot.

### Give the installation a prefix

Each installation is told its prefix once per preset, as `prefix` of the preset in the
deployment document its components read:

```yaml
presets:
  standard: {bucket: audit-eu-example-1, region: eu-example-1, prefix: audit/<application>/}
```

Under that prefix the layout is the same for every installation, which is what
lets two applications on different versions of this component share one bucket:
the archive's layout is the contract between them, not the code. Lifecycle rules
filter on `<prefix>/records/<profile>/`, one rule per profile per application: a
bucket-wide rule would apply the shortest profile's transition to every
application in it.

Then scope each role's IAM under the prefix
([archive prefixes and IAM](../reference/archive-prefixes-and-iam.md)). Verify:
the writer starts and `audit verify` over the first hour reports the objects.
Roll back: remove the prefix's roles; the prefix itself stays (the archive is
append-only).

### The attested tier

The same archive, the same keys under the same prefix — on a store that holds no lock. Either the store has no Object Lock API,
which is most S3-compatible stores, or the deployment composes only profiles
that demand none and chooses not to lock. The preset is not `attested` (the interactive commands' `--lock-mode none`); the writer sends no lock header on
any put, and answers a retention extension or a legal hold with
`store.ErrNotLockable`: the extension is recorded in the trail as not made,
and `audit hold place` is refused and records the attempt.

What the deployment supplies in place of the lock:

- **Seals under a managed key.** Without the lock, only a seal made with a key
  the operator cannot re-sign with proves the operator did not choose what the
  archive holds. The notary writes them ([0061](../../decisions/0061-seals.md)); run
  it on this tier. The per-object `sha256` and per-record hashes that
  `audit verify` checks are the lower layer.
- **No delete permission on any component**, exactly as on the record tier,
  and versioning on where the store offers it.
- **A bucket-level no-delete rule where the store has one.** Several stores
  let an administrator forbid deletes on a bucket or a prefix by a rule the
  same administrator can remove. That is governance-shaped protection, worth
  having and not to be mistaken for compliance mode; record in the
  deployment's own runbook that it is set.
- **Retention as a lifecycle rule** rather than a lock: the store expires
  objects when the rule says, and nothing stops the rule being shortened. A
  profile that cannot accept that demands the lock, and says so.

`audit verify --deployment <file>` then reports each object as `unlocked`
rather than pretending to have checked a lock; under a profile that demands
one, an object with no lock is `INVALID`.


### S3-compatible stores

Any store that speaks the S3 API takes the `operational` or `standard` preset, and the
`attested` one only if it is AWS S3 (the lock is an AWS guarantee). Three things differ from
AWS, and each is a key of the preset in the deployment document (`endpoint`, `path_style`,
`credentials`):

```yaml
presets:
  operational:
    bucket: audit-example
    region: auto
    endpoint: https://s3.example.test
    path_style: true
    credentials: internal/audit/main      # below the process's archive.stateRoot
```

- **The endpoint.** `endpoint`. Unset is the SDK's own resolution for the region, which is AWS.
- **Path-style addressing**, when the store's certificate does not cover a bucket subdomain:
  `path_style` sends `endpoint/bucket/key` rather than `bucket.endpoint/key`.
- **Static credentials**, when the store has no pod identity: `credentials` is the address, in
  the installation's state store below `archive.stateRoot`, of a JSON object
  `{accessKeyID, secretAccessKey}`, read with the process's own identity. No secret is in a
  file. Unset, the SDK's ambient credentials are used, which is what a workload identity provides.
- **Credentials minted for the process**, on Cloudflare R2: instead of `credentials`, a
  `credentials_preset` makes the process clone a disabled Cloudflare prototype token with a
  minter token and renew the resulting credentials with a third of their lifetime left. The static
  `credentials` stay the default and need no Cloudflare account; the two are exclusive.

  ```yaml
  presets:
    operational:
      bucket: audit-example
      region: auto
      endpoint: https://<account-id>.r2.cloudflarestorage.com
      path_style: true
      credentials_preset:
        account: <account-id>
        minter: internal/cloudflare/main/minter   # below archive.stateRoot; cloudflare-minter/v1
        prototype: <id of the disabled prototype token>
        lifetime: 15m
  ```

  The minter document is `{"schema": "cloudflare-minter/v1", "token": "..."}` in the state
  store. It can mint anything the Cloudflare account owner can, so the refusal list in the
  minting code is the only guard: a prototype that is active, or grants token admin, billing,
  account settings, memberships or Access identity providers, is refused at every mint. The
  process records the ids of the tokens it mints at `cloudflare-minted/<preset>` in the same
  store and deletes the expired ones at its next mint, because Cloudflare hides an expired
  token from its list while still counting it. The Pulumi library
  (`PresetStorage.CredentialsPreset`) grants the writer and the notary read on exactly the minter
  address and read and write on exactly `cloudflare-minted/<preset>` below the state root (the
  secrets key through SSM, as for the rest), and only for a preset that uses it. When the store
  answers 403 the request is sent once more with new credentials, at most once per request and one
  mint per 30 seconds. The
  code is `github.com/truvity/sluis/storage/cloudflare`, shared with sluis itself.
- **A private CA.** `archive.ca` is the path to a bundle trusted for the endpoint, mounted by
  the platform (the chart's `trust` puts one at `/etc/audit/trust/<key>`).

With an endpoint set, the SDK's default CRC32 request checksum — which AWS
answers and other stores may refuse — is sent only where an operation
requires one. The SHA-256 the archive names on every put is still sent, and
still stored as the object's checksum where the store keeps one.

One region-shaped trap: a store that serves one region and does not answer
a bucket-location lookup wants `region` set to whatever it documents
(often `auto`), so that the SDK skips the lookup. Set it as
`bucket.region`.

Where the store's documentation names an S3 feature it does not implement
— conditional writes (`If-None-Match`), `ListObjectsV2` continuation, SSE-KMS
with a customer key — check before choosing it: the writer relies on the
first two, and `archive.kmsKey` on the third.


## Afterwards

- Break-glass reads: auditors get a read-only role scoped to one installation's
  `records/` and `catalogue/`; their reads appear in the bucket's access log and,
  through the query service, as `audit.get` and `audit.search` records.
- Keep the exports in a **separate bucket with no Object Lock** and a lifecycle
  rule that expires `export/`: an export is a copy meant to be collected and
  cleared, and the archive's policy denies every delete.
