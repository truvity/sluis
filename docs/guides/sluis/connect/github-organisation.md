# Connect a GitHub organisation

Bind groups to an organisation's teams, connect its App and run the controller. See [How a GitHub pass decides](../../../concepts/sluis/github-pass.md), the [Kubernetes objects](../../../reference/sluis/kubernetes-objects.md) and [Link a GitHub account to a person](github-account-links.md).

## Before you start

- The team key is the team's slug.

- Only an organisation the policy binds can be connected.

- Egress must allow `api.github.com`.

## 1. Bind groups to teams

```yaml
github:
  globex:                                   # the organisation's login
    members: [all:globex:employee]          # in the organisation, with or without a team
    teams:
      team-platform:                        # the team's slug
        members: [all:platform:engineer]
        maintainers: [all:platform:lead]
      team-security:
        members: [all:security:analyst]
```

Use groups the policy declares. The Rules page lists each binding as kind `GitHub team`.

## 2. Connect the organisation

On the GitHub page's *Apps* tab, open the organisation's App and press **Create**. The owner creates the App on GitHub (private, no webhook, `members: write`, `organization_administration: read`) and installs it. After a stop, press **Install**.

The owning directory is chosen on the form or defaults to the connecting operator's ([ownership rules](../../../reference/sluis/policy-ownership.md#who-owns-a-github-organisation)). A policy carrying `github.<org>.owner` is refused at load: delete the key.

## 3. Run the controller

```yaml
config:
  controllers:
    github:
      consoleURL: http://sluis.access.svc:8080/console   # this release's own Service
      interval: 15m
policy:
  controllers:
    github:
      enabledOrgs: []   # nothing changes until an organisation is listed
exchange:
  clusters:
    - name: prod        # the service verifies the controller's token against this key set
      issuer: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE
      jwksUri: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE/keys
```

```yaml
groups:
  all:access-roster:viewer:
    matchers:
      - service_account: { cluster: prod, namespace: access, name: sluis }   # the controller runs as the release's ServiceAccount
```

The controller runs inside `sluis serve` as the ServiceAccount `<release>`. The chart refuses to render it without an `exchange.clusters` row or a console mount. With `audit.*` set, map the account to the audit source `roster`. To act, follow [Enable a GitHub organisation](../enable-github-organisation.md).

## 4. Run a pass now

Every 30 seconds the controller checks the `<release>-github-apps` Secret and `<release>-github-orgs` ConfigMap, and a change starts a full pass within about two minutes.

**Refresh** on an installed organisation's page asks for a pass now. A second request within 60 seconds fails with `resource_exhausted`, and an uninstalled App with `failed_precondition`. Each is audited as `roster.github_org.pass_requested`.

## 5. Add runner Apps

A runner App is what a self-hosted runner scale set registers with, one per organisation per tier:

```yaml
config:
  github:
    runnerTiers: [preview, stable]
```

The *Runners* tab shows a row per bound organisation per tier. Create then install each as in step 2. The App asks only for `organization_self_hosted_runners: write`.

The Secret `<release>-github-runner-apps` holds `<tier>.<org>.github_app_id`, `.github_app_installation_id` and `.github_app_private_key`, as `gha-runner-scale-set` reads in `githubConfigSecret`. Copy them to your runners, for example with a `PushSecret`.

For other Apps, see [A catalogue of GitHub Apps](github-apps-catalogue.md).

## Roll back

**Disconnect** uninstalls the App, then forgets its record and key, even if the uninstall fails. Its owner deletes the registration on GitHub. Nobody is removed. Disconnecting a runner App stops its runners getting jobs.
