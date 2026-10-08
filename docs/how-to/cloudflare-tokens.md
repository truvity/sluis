# Mint short-lived Cloudflare tokens and R2 credentials

Cloudflare has no web-identity federation: nothing trades proof of identity for a
short-lived credential. sluis can be that service. It holds one **minter
credential** per Cloudflare account and clones a **prototype** token, a disabled
account token whose policies are the rights, into new account tokens that
expire. The policy language stays Cloudflare's own: you edit the prototype in the
dashboard (or declare it with Pulumi), and Cloudflare validates the permission
names.

This is optional. R2 with static credentials needs none of it: see
[keep the blobs on an S3-compatible store](blobs-on-r2.md), which works with no
`cloudflare` section.

Needs `secrets.source: ssm` with `secrets.layout: v4` or `transition`: the minter
credential lives at an `internal/` address and the credentials sluis stores at
`external/cloudflare/<preset>`.

## The minter can mint anything the account owner can

Checked against a live account (2026-10-08): **a token that can create tokens is
not bounded by its own permissions.** A token holding only Account API Tokens
Read and Write created children with DNS Write, Account Settings Write and R2
Write. The minter is created by the account owner, so it can mint anything the
owner can, and **sluis's refusal list is the only guard** between it and the
whole account. Its custody is therefore the owner's:

- the document lives only under `internal/`, which no consumer is ever granted:
  keep it as a KMS-encrypted parameter (`secrets.kmsKeyId`) that only the
  function's role (or the Kubernetes identity) can read, and give nobody else
  `ssm:GetParameter` on `internal/cloudflare/`;
- the refusal list is built in and cannot be shortened by configuration: a
  prototype that is active, missing, or grants Account API Tokens Edit (Write),
  Billing, Account Settings, Memberships, or Access: Organizations, Identity
  Providers, and Groups is refused. `cloudflare.forbiddenPermissionGroups` can
  only **add** names to it;
- it is checked against the prototype's live permission groups at every mint
  and once when a cluster service starts; a permission group sluis cannot name
  is refused. Every refusal is an audit event
  (`roster.cloudflare.token.refused`), a log line and the counter
  `sluis.cloudflare.prototype.refused`.

A possible hardening, not required and not yet tested (whether a member who is
not a Super Administrator can create account-owned tokens): create the minter as
a dedicated member with limited roles, so that its ceiling is lower than the
owner's.

## 1. Create the minter token

In Cloudflare, create an **account** API token with **Account API Tokens Read
and Write** and nothing else, then store it:

```sh
aws ssm put-parameter --type SecureString --name /sluis/<instance>/internal/cloudflare/main/minter \
  --value '{"schema":"cloudflare-minter/v1","token":"<the token>"}'
```

([schema](https://github.com/truvity/sluis/blob/master/schemas/internal/cloudflare-minter.v1.schema.json).)
Never put the value in a service document, a Pulumi input or a command line.

## 2. Create a prototype

Create an account token with the rights a minted token should have, for example
DNS Write on one zone, or Bucket Item Read and Write on one R2 bucket. Then
**disable it**: Cloudflare ignores `status: disabled` at creation (the token comes
back active), so disable it with an update afterwards. sluis refuses a prototype
that is not `disabled` **every time it reads one**, because an active prototype is
a usable token that never expires.

For R2 the resource key of a bucket is
`com.cloudflare.edge.r2.bucket.<account-id>_default_<bucket>` (`_eu_` for the EU
jurisdiction), with the Bucket Item Read or Write permission groups. The
derived credentials are refused (403) on any other bucket.

## 3. Declare the preset

In the service document:

```yaml
cloudflare:
  accounts:
    main:
      id: <account-id>
      minter: internal/cloudflare/main/minter
  presets:
    dns-example:
      account: main
      prototype: <prototype token id>
      description: DNS edits on example.com for the release job
      lifetime: 15m
      rotation: 5m
    r2-archive:
      account: main
      prototype: <R2 prototype token id>
      description: Archive bucket, read and write
      endpoint: https://<account-id>.r2.cloudflarestorage.com
      lifetime: 15m
      rotation: 5m
```

`endpoint` makes a preset an R2 preset (use `<account-id>.eu.r2.cloudflarestorage.com`
for an EU jurisdiction). `lifetime` is each minted token's `expires_on`;
`rotation` is how often the stored credential is replaced. Both are at least a
minute, `rotation` is shorter than `lifetime`, and `lifetime - rotation` is the
time consumers have to pick up a new credential.

sluis **refuses** a prototype that is active, that does not exist, or that grants
any permission group on the built-in refusal list above (and any you add with
`cloudflare.forbiddenPermissionGroups`). The check runs against the prototype's
permission groups **at every mint** and when a cluster service starts, and a
permission group sluis cannot name is refused too.

## 4. Say who may ask

In the policy document:

```yaml
cloudflare:
  grants:
    - group: all:infra:dns-editors
      presets: [dns-example]
    - group: all:ci:release
      presets: [dns-example, r2-archive]
```

A row names a `group` of the policy, and everyone who holds it may ask. A CI job
is such a group, declared like any other with `github` matchers on what the
verified identity token says (`repository`, `ref`, `event_name`,
`job_workflow_ref`): a job that can be started from any branch by anyone who can
push is not one to grant a token to, so pin it, for example:

```yaml
groups:
  all:ci:release:
    matchers:
      - github:
          repository: example-org/example-repo
          ref: refs/tags/v*
          job_workflow_ref: example-org/example-repo/.github/workflows/release.yml@refs/tags/v*
```

## How it runs

Each pass (every minute in a cluster; an EventBridge schedule sending
`{"kind":"cloudflare"}` on Lambda) looks at each preset's stored credential. A
credential that is not yet older than `rotation` costs one read of the secrets
store and nothing at Cloudflare. A due one is replaced:

1. the prototype is read and checked, and its policies and condition copied into
   a new account token named `sluis/<instance>/<preset>/<time>` that expires
   after `lifetime`;
2. the token's id and expiry are **recorded** in the secrets store, then the
   credential is written to `external/cloudflare/<preset>` as `cloudflare/v1`
   (`token`, or for R2 `access_key_id`, `secret_access_key`, `endpoint`; always
   `expires_on`; [schema](https://github.com/truvity/sluis/blob/master/schemas/external/cloudflare.v1.schema.json)).
   For R2 the access key is the token's id and the secret is the hex SHA-256 of its
   value;
3. the preset's recorded tokens that have expired are deleted.

The sweep deletes **by the recorded ids**, not by listing the account:
Cloudflare hides an expired token from its list while still answering a GET for
it and (assumed) counting it toward the 500 tokens an account may hold, so a
sweep that listed would never find them. It also deletes only a token whose name
still begins with its own preset's prefix; nothing else in the account is touched.

Limits to know: 1,200 API requests per 5 minutes per user (exceeding blocks the
API for 5 minutes) and 500 account tokens per account. At a 15 minute lifetime and
5 minute rotation a preset has about three live tokens and costs a few calls per
rotation.

## On demand and revoke

A granted caller (a person signed in to the console or `sluisctl`, or a CI job
with its GitHub OIDC token) can have a token of its own, minted from the same
prototype with a lifetime up to the preset's and named
`sluis/<instance>/<preset>/<caller>/<time>`. To revoke a live token, delete it by
id from the console or the CLI; if it was the stored one, a replacement is
minted at once.

### In the console

The Cloudflare page of the console (Systems) is both views of this. Anyone
signed in sees the presets their groups are granted and can **Get a token**: the
value is shown once and not kept. A viewer also sees each preset's prototype as
sluis reads it now, the stored token's id, age and expiry (a warning past one
rotation, an error past two, the alert's threshold), and the on-demand tokens
still live. An operator can **Rotate now** and **Revoke** after a confirmation;
both are audited under their name (`roster.cloudflare.token.minted`,
`roster.cloudflare.token.revoked`). Rotate now is refused while the prototype is
active or grants a forbidden permission, like any mint, and answers "aborted"
when the schedule is rotating the same preset. Opening the page reads each
preset's prototype and lists its tokens once, a handful of the API requests in
the limit above.

## sluis's own R2 credentials, without a static document

An R2 consumer of sluis itself can name a preset instead of a static document, so
that no long-lived R2 secret exists at all:

```yaml
ports:
  blob:
    adapter: s3
    s3:
      bucket: example-blobs
      endpoint: https://<account-id>.r2.cloudflarestorage.com
      credentials: {preset: r2-blobs}   # exclusive with credentialsRef
```

sluis uses the minter credential to clone the preset's R2 prototype, renews
with a third of the lifetime left, mints again once after a 403, and waits about
five seconds after each mint (Cloudflare accepts a derived credential that long
after creation). The static `credentialsRef` stays the default and the simpler
path.

## Audit events

`roster.cloudflare.token.minted`, `.refused`, `.tokens.swept` and `.token.revoked`
(category `security`). They carry the preset, the account, the token's id and its
expiry; never a value.

## Alert

`AccessRosterCloudflareRotationStale` fires when a preset's stored credential is
older than twice its rotation. The metrics are
`sluis.cloudflare.rotation.last_timestamp` and `.interval` (by preset),
`sluis.cloudflare.tokens.minted` and `.swept`; see [telemetry](../reference/telemetry.md).
