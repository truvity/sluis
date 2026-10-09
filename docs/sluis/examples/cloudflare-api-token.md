# Example: a short-lived Cloudflare API token for a CI job

## Goal

A release job edits DNS on one zone with a Cloudflare token that lives 15 minutes, and only the one reviewed workflow at a
tag can ask for it.

## What you need

- The minter and a disabled prototype with DNS Write on one zone (see
  [R2 credentials](cloudflare-r2-credentials.md#what-you-need)).
- The workflow runs with `permissions: id-token: write`.

## The policy snippet

Service document:

```yaml
cloudflare:
  presets:
    dns-example:
      account: main
      prototype: <prototype token id>
      description: DNS edits on example.com for the release job
      lifetime: 15m
      rotation: 5m
```

Policy: the job as a group, pinned by `job_workflow_ref` and ref, and the grant (a `group:` row).

```yaml
groups:
  all:ci:release:
    matchers:
      - github:
          repository: example-org/example-repo
          ref: refs/tags/v*
          job_workflow_ref: example-org/example-repo/.github/workflows/release.yml@refs/tags/v*
cloudflare:
  grants:
    - group: all:ci:release
      presets: [dns-example]
```

Never grant to a job that anyone who can push a branch can start; the workflow's display name is not a pin.

## The exchange / command

```yaml
- run: echo "CLOUDFLARE_API_TOKEN=$(sluisctl cloudflare token dns-example --format json | jq -r .token)" >> "$GITHUB_ENV"
```

A person: `sluisctl cloudflare token dns-example` prints `CLOUDFLARE_API_TOKEN=...`;
`--format json --lifetime 5m` prints `{"token","expires_on"}`.

## Verify

The token works against the zone and is refused elsewhere; a run from a branch is refused (exit 4). The audit trail has
`roster.cloudflare.token.minted` with the caller.

## Undo

Remove the grant row or the group; delete the preset. A live token expires on its own, or revoke it by id.

Source: the how-to [mint short-lived Cloudflare tokens](../../how-to/cloudflare-tokens.md) and the
[`sluisctl cloudflare` reference](../../reference/sluisctl.md#cloudflare-token-and-cloudflare-r2). The policy snippet renders with
`sluisctl policy render` (checked against master, 2026-10-09).
