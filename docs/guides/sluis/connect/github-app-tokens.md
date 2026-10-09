# Mint a GitHub App token

Exchange a proof for an installation token of a catalogue App, never wider than one grant. Create the App as in [A catalogue of GitHub Apps](github-apps-catalogue.md).

## Before you start

- One grant must cover the whole request; grants are never added together.
- Pin a grant to one reviewed workflow, not every job in the repository.

## 1. Write a grant

```yaml
grants:
  - group: all:docs:writer            # a group the policy declares
    repositories: [docs, "site-*"]    # names or globs, within the App's organisation
    permissions: {contents: write}    # the most a token may carry
```

- `repositories` are names or `path.Match` globs; `["*"]` is every repository. A `/` is refused.
- Every permission must be one the App has, at a level no higher: `read` < `write` < `admin`.
- A group may appear in several grants, each read on its own.

## 2. Mint a token

The proof is a job's identity token, a workload's ServiceAccount token or a `sluisctl login`. It resolves to groups as in [any other exchange](github-actions.md).

```yaml
permissions:
  id-token: write                       # the job's identity token, for the issuer
  contents: read
steps:
  - id: access
    uses: truvity/sluis@v1.11.0
    with:
      issuer: https://access.example.com
      github-app: publisher             # the catalogue id
      repositories: app, lib-core       # names, without the owner
      permissions: contents:write, pull_requests:write
  - run: gh release create "v1.2.3" --repo example-org/app
    env:
      GH_TOKEN: ${{ steps.access.outputs.github-token }}   # masked
```

`sluisctl` reads the job's token or the laptop's sign-in:

```sh
sluisctl github-token --app publisher --repository app --permission contents=write
sluisctl github-token --app publisher --repository app --json
# {"token":"ghs_…","expires_at":"2026-01-01T13:00:00Z","repositories":["app"],"permissions":{"contents":"write"}}
```

Without our tooling:

```sh
curl --fail-with-body -sS -u 'github-app%3Apublisher:' \
  --data-urlencode grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
  --data-urlencode "subject_token=${JOB_ID_TOKEN}" \
  --data-urlencode subject_token_type=urn:ietf:params:oauth:token-type:jwt \
  --data-urlencode requested_token_type=urn:access-roster:params:oauth:token-type:github-installation-token \
  --data-urlencode audience=github-app:publisher \
  --data-urlencode 'repositories=app lib-core' \
  --data-urlencode 'scope=contents:write pull_requests:write' \
  https://access.example.com/token | jq -r .access_token
```

The wire contract and errors are in [Installation tokens at /token](../../../reference/sluis/contracts.md#installation-tokens-at-token). `sluisctl github-token` exits `4` on a refusal and `5` when the issuer is unreachable.

## 3. Pin a grant to one workflow

```yaml
# policy
groups:
  all:app:publisher:
    matchers:
      - github:
          repository: example-org/app
          ref: refs/heads/main
          ref_type: branch
          event_name: push
          job_workflow_ref: example-org/app/.github/workflows/release.yml@refs/heads/main
```

```yaml
# values: githubApps.catalogue
- id: publisher
  org: example-org
  permissions: {contents: write, pull_requests: write}
  installation: selected
  grants:
    - group: all:app:publisher
      repositories: [app, "lib-*"]
      permissions: {contents: write, pull_requests: write}
```

`job_workflow_ref` is the file defining the job: the called file for a reusable workflow. `workflow_ref` is the file the run started from. See [policy groups](../../../reference/sluis/policy-groups.md#proof-to-groups).

## 4. Know which grant applies

Among the App's grants whose group the proof holds, in catalogue order, the first that covers the whole request mints the token.

- Named repositories must all match that one grant. Repositories from two grants are two tokens.
- Without repositories the grant must be `["*"]`. Otherwise the request fails with *name the repositories*.
- Named permissions must each be covered. Without permissions the token gets the grant's permissions, never the App's whole set.

A repository the installer did not select, or a permission the installation has not accepted, returns `invalid_scope` with GitHub's words.

## Verify

Every request writes one `roster.github_token.minted` record with outcome `success`, `denied` or `failure`. The actor is the proof's subject, or `anonymous`. The target is `github_app` with the catalogue id. The token is never recorded.

| Property | Holds |
|---|---|
| `proof` | `ci`, `workload` or `person` |
| `org` | The App's organisation |
| `grant` | The group of the grant used |
| `repositories` | What GitHub granted, or what was asked when refused |
| `permissions` | `name:level`, space separated |
| `installation` | The installation id |
| `expires_at` | When the token stops working |

The App's page lists its last ten requests under *Recent tokens*. That list resets on restart; the audit trail is the record: `#/audit?q=action:roster.github_token.minted target:github_app:<id>`. A group's page shows each grant's last use.

Decided in: [ADR 0014](../../../decisions/0014-minting-third-party-credentials-only-where-membership-is-governed.md).
