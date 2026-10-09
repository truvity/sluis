# Example: a catalogue GitHub App, its key, and a token for a job

## Goal

Declare a GitHub App for one automation and create it with two clicks. Deliver its key to a consumer that cannot ask the issuer, using an ExternalSecret with `property:`. Let one pinned workflow mint a narrowed installation token.

## What you need

- A deployment with the GitHub controller connected and `secrets.layout: v4` (the key is kept at `external/github/<id>`).
- An owner of the organisation, for the install click.
- A secrets operator in the consumer's cluster.

## The policy snippet

Values (`githubApps.catalogue`) and the policy group the grant names. The group must exist in the policy.

```yaml
githubApps:
  catalogue:
    - id: publisher
      org: example-org
      description: Cuts releases for example-org/app
      permissions: {contents: write, pull_requests: write}
      installation: selected
      grants:
        - group: all:app:publisher
          repositories: [app, "lib-*"]
          permissions: {contents: write, pull_requests: write}
```

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

## The exchange / command

1. Roll the values out. In the console, GitHub page, *Apps* tab, press *Create* on `publisher`. An owner confirms on GitHub and picks the repositories.
2. Hand the key to a consumer that cannot ask the issuer. The document is at `external/github/publisher`; an ExternalSecret
   reads one property of it:

```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: publisher-app-key, namespace: ci}
spec:
  secretStoreRef: {kind: ClusterSecretStore, name: sluis}
  target: {name: publisher-app-key}
  data:
    - secretKey: private-key
      remoteRef:
        key: external/github/publisher
        property: private_key
```

3. A job mints a narrowed token instead of holding the key:

```yaml
permissions: {id-token: write, contents: read}
steps:
  - id: access
    uses: truvity/sluis@<commit-sha>   # vX.Y.Z
    with:
      issuer: https://access.example.com
      github-app: publisher
      repositories: app, lib-core
      permissions: contents:write
  - run: gh release create v1.2.3 --repo example-org/app
    env: {GH_TOKEN: "${{ steps.access.outputs.github-token }}"}
```

On a laptop: `sluisctl github-token --app publisher --repository app --permission contents=write`.

## Verify

The App's page reads *installed*. The consumer's Secret holds the key. The audit trail shows
`roster.github_token.minted` with outcome `success` and the grant's group; a request wider than the grant is refused with
`invalid_scope`.

## Undo

Remove the catalogue entry or press *Disconnect*, which uninstalls the App and forgets the key. Deleting the App on GitHub stays the owner's act. Remove the ExternalSecret.

Recipes: [a catalogue of GitHub Apps](../github-apps-catalogue.md),
[mint a GitHub App token](../github-app-tokens.md),
[keep and back up a key](../github-app-keys.md).

Snippet source: the values shape is `tests/cases/sluis/catalogue/values.yaml` (golden `tests/golden/sluis/catalogue.yaml`).
The policy group is written so that `sluisctl policy render` accepts it.
