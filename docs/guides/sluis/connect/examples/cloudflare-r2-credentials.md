# Example: short-lived R2 credentials for a bucket

## Goal

Give people, CI jobs and pods S3 credentials for one R2 bucket that expire in minutes, with no long-lived R2 secret.

## What you need

- `secrets.source: ssm` with `secrets.layout: v4` (or `transition`).
- A Cloudflare **minter** token (account token, Account API Tokens Read and Write only) stored at
  `internal/cloudflare/main/minter`, and a **disabled** prototype token holding Bucket Item Read and Write on the bucket.
  Custody of the minter is the account owner's: it can mint anything the owner can, and sluis's refusal list is the only guard.

## The policy snippet

Service document, the preset:

```yaml
cloudflare:
  accounts:
    main:
      id: <account-id>
      minter: internal/cloudflare/main/minter
  presets:
    r2-archive:
      account: main
      prototype: <R2 prototype token id>
      description: Archive bucket, read and write
      endpoint: https://<account-id>.r2.cloudflarestorage.com
      lifetime: 15m
      rotation: 5m
```

Policy document, who may ask:

```yaml
cloudflare:
  grants:
    - group: all:infra:archive-users
      presets: [r2-archive]
```

## The exchange / command

```sh
sluisctl login
sluisctl aws-config                       # adds the profile r2-archive@r2 per granted R2 preset
aws --profile r2-archive@r2 s3 ls s3://example-archive/
```

The profile runs `sluisctl cloudflare r2 r2-archive` as its `credential_process` and sets `endpoint_url`,
`region = auto`, the two checksum settings as `when_required`, and path-style addressing. A pod that gets
`external/cloudflare/r2-archive` from a secrets operator uses
`credential_process = sluisctl cloudflare r2 r2-archive --file /var/run/secrets/cloudflare/r2-archive.json`, which refuses
an expired document.

## Verify

The listing succeeds; on any other bucket the credentials are refused (403). `sluisctl whoami` lists the granted presets.
The audit trail has `roster.cloudflare.token.minted`. The alert `AccessRosterCloudflareRotationStale` fires when the stored
credential is older than twice its rotation.

## Undo

Remove the grant row (an unknown preset and an ungranted one give the same answer). Revoke a live token by id from the
console or CLI; remove the preset to stop rotation. Delete `<config>/cloudflare/` to drop cached credentials.

Source: the how-to [mint short-lived Cloudflare tokens](../../cloudflare-tokens.md) and the
[`sluisctl cloudflare` reference](../../../../reference/sluis/sluisctl.md#cloudflare-token-and-cloudflare-r2). Snippets follow those pages; the
policy grant renders with `sluisctl policy render` (checked against master, 2026-10-09).
