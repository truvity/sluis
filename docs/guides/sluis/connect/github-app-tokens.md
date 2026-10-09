# Mint a GitHub App token

A job or a person holding a granted group exchanges its proof at sluis's `/token` for an installation token of a
catalogue App, narrowed to what it asked for and never more than one grant allows. This page is the grants and the
exchange. Declaring and creating the App is [A catalogue of GitHub Apps](github-apps-catalogue.md).

## Grants

A grant says that the holders of one policy group may ask for an
installation token of the App, for some of its repositories, with at most
some of its permissions:

```yaml
grants:
  - group: all:docs:writer            # a group the policy declares
    repositories: [docs, "site-*"]    # names or globs, within the App's organisation
    permissions: {contents: write}    # the most a token may carry
```

- `repositories` are repository names in the App's organisation, or
  globs in Go's `path.Match` syntax (`*`, `?`, `[a-c]`). `["*"]` is every
  repository. No `/`: a grant never reaches another organisation.
- every permission in a grant must be one the App has, at a level no
  higher than the App's: `read` < `write` < `admin`. A grant of
  `contents: write` on an App with `contents: read` is refused.
- a group may appear in several grants; each is read on its own.

How a grant becomes a token is [Minting a token](#minting-a-token).

## Minting a token

A caller exchanges the proof it already holds — a GitHub Actions job's
identity token, a cluster workload's ServiceAccount token, or a person's
`sluisctl login` — for an installation token of one App. The proof is
verified exactly as for [any other exchange](github-actions.md), and
resolves to the same groups; the App's grants for those groups then
decide. Nothing is stored: every token is minted from GitHub on request,
and every request is audited.

### From a job

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

`audiences` may be left out when a job wants only the GitHub token, or
given beside it for clusters and cloud roles in the same step.

Or with `sluisctl`, which picks the job's identity token up from the
environment and the sign-in on a laptop, so the same line serves both:

```sh
sluisctl github-token --app publisher --repository app --permission contents=write
sluisctl github-token --app publisher --repository app --json
# {"token":"ghs_…","expires_at":"2026-01-01T13:00:00Z","repositories":["app"],"permissions":{"contents":"write"}}
```

Or raw, with nothing of ours at all:

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

The exact wire contract is in
[the reference](../../../reference/sluis/contracts.md#installation-tokens-at-token).

### Pinning a grant to one workflow

A grant names a **group**; the **policy** says which proofs hold it. For
a token that can write to a repository, the group should admit one
reviewed workflow file on the default branch, not every job in the
repository — a pull request's run of an edited workflow is a job in the
repository too. A `github` matcher pins exactly that:

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

`job_workflow_ref` is the file the **job** is defined in, at its ref:
for a reusable workflow it is the called file, which is the code that
holds the token. `workflow_ref` is the file the run started from. Both,
with `sha`, `event_name` and `ref_type`, are read from GitHub's token and
matched as globs where `*` does not cross a `/`
([policy](../../../reference/sluis/policy-groups.md#proof-to-groups)).

### One grant covers the whole request

Of the App's grants whose group the proof holds, **in catalogue order**,
the first that covers all of the request is the one the token is minted
under. Two grants are never added together.

- **Repositories named:** each must match some grant the proof holds,
  and the chosen grant must match all of them. `app` and `docs` from two
  different grants are two tokens.
- **No repositories named:** the chosen grant must be for every
  repository, `["*"]`, and the token is not narrowed to any — it reaches
  what the installation reaches. Otherwise the request is refused with
  *name the repositories*.
- **Permissions named:** the chosen grant must cover each, at its level
  or above (`read` < `write` < `admin`).
- **No permissions named:** the token asks for exactly the chosen
  grant's permissions — never the App's whole set.

GitHub is always sent an explicit narrowing, `{"repositories": [...],
"permissions": {...}}` (repositories left out when none were named), so
what the installation holds beyond the grant never reaches the token. A
repository the installer did not select, or a permission the
installation has not accepted, is GitHub's refusal and comes back as
`invalid_scope` with GitHub's words.

### Errors

| `error` | HTTP | When |
|---|---|---|
| `invalid_request` | 400 | a malformed request: `actor_token` given (delegation is not served), a repository spelled with its owner, a permission not `name:read\|write\|admin`, subject token or its type missing |
| `invalid_client` | 401 | a client was presented, other than the App's audience, and did not authenticate |
| `invalid_grant` | 400 | the subject token is not a proof: unverifiable, or a sign-in presented by a client other than its own, or its session ended |
| `invalid_target` | 400 | the App cannot mint — not declared, not created, not installed, uninstalled on GitHub — **or** no grant of the App names a group the proof holds. The description says which, as for an ordinary exchange |
| `invalid_scope` | 400 | the request is wider than every one grant the proof holds: a repository outside them, two repositories no one grant covers, a permission above them, no repositories named where no grant is `["*"]`, or GitHub refusing the narrowing |
| `server_error` | 500 | GitHub failing, or the App's key failing |

`sluisctl github-token` exits `4` on every refusal and `5` when the
issuer cannot be reached.

### Audit

One record per request, minted or refused: action
`roster.github_token.minted`, outcome `success`, `denied` (refused) or
`failure` (GitHub or the key). The actor is the proof's subject, by kind —
a CI job (`github:example-org/app`), a workload (its ServiceAccount), a
person (their address) — or `anonymous` for a request refused before any
proof was read; the target is the App (`github_app`, its catalogue id); the
reason is the description the caller was given. Its data:

| Property | Holds |
|---|---|
| `proof` | `ci`, `workload` or `person` |
| `org` | the App's organisation |
| `grant` | the group of the grant the token was minted under |
| `repositories` | a list: what GitHub granted, or what was asked for when refused |
| `permissions` | `name:level`, space separated, likewise |
| `installation` | the installation id |
| `expires_at` | when the token stops working |

**The token itself is never recorded**, nor any part of it.

### Recent tokens, on the App's page

An App's page in the console shows the last ten requests made of it under
*Recent tokens*: when, who asked and how they proved it, the grant it was
decided under, the repositories and permissions, and whether it was
minted, refused or failed — with the refusal's reason in the chip's
tooltip. The token is not there, because nothing keeps it.

It is this service's own memory of those requests, kept beside the half
that mints them: bounded to the last ten of each App, since the process
started, and forgotten on a restart, which the page says under the table.
It is **not** the record. The record is the audit trail above, which holds
every request for as long as the installation keeps it, and every reading
of that section links to the console's Audit page narrowed to that App
(`#/audit?q=action:roster.github_token.minted target:github_app:<id>`, an
address to send somebody). The section exists so that an App's page loads
without asking the trail anything, and works with no installation
connected.

An internal group's page carries the one fact that falls out of the same
memory: when each grant it holds was last used. A grant with nothing
beside it is one nothing is remembered for — after a restart that is every
grant — and never one that is known to be unused.
