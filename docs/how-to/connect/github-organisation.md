# Connect a GitHub organisation

> **In use.** The controller acts in production organisations, each
> enabled after a supervised dry run.

How the controller decides is in [How a GitHub pass decides](../../explanation/github-pass.md); the
Secrets and records it keeps are in [GitHub roster reference](../../reference/github-roster.md); linking a person's
account is [its own page](github-account-links.md).

**Anchor:** none of ours. The controller holds one GitHub App per
organisation and acts with its own credential; the issuer holds the
bindings, and no part of this service ever proves anything to GitHub.

## The binding

*Internal group → GitHub team* is the same shape as *internal group →
client*, so it is written the same way and read the same way:

```yaml
github:
  globex:                                   # the organisation's login
    members: [all:globex:employee]          # in the organisation, with or without a team
    teams:
      team-platform:                        # the team's SLUG, not its display name
        members: [all:platform:engineer]
        maintainers: [all:platform:lead]
      team-security:
        members: [all:security:analyst]
  acme:
    teams:
      team-platform:
        members: [all:platform:engineer]
```

The holders of those groups are the people that team should contain. A
team is a consumer of a group exactly as a client is: which accounts hold
one is a question only the directory answers, and the policy answers it
once, in `groups`.

Nothing here reaches a token and no `requires` gates on it: a GitHub team
is not an internal group and opens no client.

**Why it lives in this file rather than the controller's own.** A reader
of the access model sees every GitHub team's source without opening
another file, and `git log` is the history of who was in what. That is
the same reason every other grant lives here.

## Adding one organisation

1. **Name the groups you will bind.** A team's source is an internal
   group, so it must be one the policy declares. Usually it already is —
   the engineers of a project, the leads of a team — and binding it to a
   GitHub team adds one more consumer. Where nothing describes the
   population, declare a group for it the ordinary way and give it a
   provider group to read.
2. **Name the teams** in `github:` under the organisation's login. Both
   halves matter: the organisation is GitHub's login, the team is its
   **slug**, not its display name. Use `maintainers` for the people who
   administer the team, `members` for the rest.
3. **Name the organisation's own members**, if anybody belongs in it
   without a team. Being in a bound team implies membership; this row is
   what accounts for everybody else, and without it they are people no
   binding explains.
4. **Check the Rules page.** Each binding appears beside every other
   rule, with `GitHub team` as its kind, the internal group as its rule,
   and the same *depends on* column that group has.
5. **Connect it**, on the *Apps* tab of the console's GitHub page: open the
   organisation's own App (listed under the organisation) and an operator
   presses *Create* on its page; the organisation's **owner** then does two things on GitHub and types nothing:
   - **Create** the App GitHub offers. It is private, asks for two
     permissions — `members: write`, and `organization_administration:
     read` for the seat count — and has no webhook. GitHub hands its key
     to this service once, on the way back.
   - **Install** it on the organisation, on the page GitHub goes to next.
     Coming back, the service asks GitHub where the App is installed
     rather than trusting the redirect, and the organisation shows as
     connected.

   If the owner stops after Create, the organisation shows *created, not
   installed* and the App's page offers *Install*: it picks up at Install
   instead of creating a second App. Only an organisation the
   policy binds can be connected, so a typo in a login is caught here
   rather than on GitHub's 404.

   **Which directory owns the organisation** is chosen here, not in the
   policy: the installation-wide operator picks a connected directory (or
   none) on the form; an operator of exactly one connected directory owns what
   they connect without being asked; an operator of several picks among
   theirs. It is recorded in the organisation's connection and in the audit
   record (`roster.github_org.connected` carries `owner`). The owner's
   operators then operate the organisation beside the installation-wide
   operator; an organisation connected with no owner, or before owners were
   recorded, is the installation-wide operator's alone. Only the
   installation-wide operator changes it afterwards, with *Change owner* on the
   organisation's page or on its controller App's page
   (`roster.github_org.owner_changed`). A directory is named by its workspace id
   and every domain it is authoritative for. A policy that still
   carries `github.<org>.owner` is refused at load: delete the key. The rule,
   once, is in [the policy
   reference](../../reference/policy-ownership.md#who-owns-a-github-organisation).

   Connecting an organisation nobody has connected records the **connecting
   operator's directory** as its owner: whoever connects it first owns it.
   That is a rule about who operates the connection inside sluis;
   GitHub itself still requires an owner of the target organisation to install
   the App, so the console grants nothing in GitHub. The installation-wide
   operator can change the owner afterwards, and the change is audited. The
   same rule gates catalogue Apps: see
   [Who may](github-apps-catalogue.md#who-may).
6. **Run the controller and enable the organisation**: see *Running the controller* below, then follow
   [Enable a GitHub organisation](../enable-github-organisation.md) (dry run, read the report, add the login to
   `policy.controllers.github.enabledOrgs`, roll out).

The service reaches `api.github.com` for Create, Install and Disconnect,
so the cluster's egress policy must allow it.

## Running the controller

The controller is a loop inside the one `sluis serve` process (v1.63; it was a Deployment of its own before), named in the service document:

```yaml
config:
  controllers:
    github:
      consoleURL: http://sluis.access.svc:8080/console   # this release's own Service
      interval: 15m
policy:
  controllers:
    github:
      enabledOrgs: []   # born disabled: nothing is changed until an organisation is listed
exchange:
  clusters:
    - name: prod        # this cluster: the service verifies the controller's token against its key set
      issuer: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE
      jwksUri: https://oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLE/keys
```

and the policy puts its account in the group that reads who holds a group:

```yaml
groups:
  all:access-roster:viewer:
    matchers:
      - service_account: { cluster: prod, namespace: access, name: sluis }   # the release's own ServiceAccount: the controller runs as it
```

One group, not two. The controller used to need a `reporter` group as well,
to report what it did back through this service; it records into the audit
installation itself now, as itself, so there is nobody left to vouch for it.

What it does is recorded in the audit trail by the controller itself, with
its own token, when an audit installation is connected (`audit.*`); the
installation must map its account to the source `roster`.

The controller runs as the release's own ServiceAccount, `<release>` (v1.62's
`<release>-github-roster` is gone: update the policy's matcher and the audit
installation's workload map). The chart refuses to
render the controller without an `exchange.clusters` row or a console
mount, because either absence is a controller that can never read
anything. `config.controllers.github.interval` (15 minutes) is how long between
passes. Like the [Slack controller](slack-workspace.md#a-pass-runs-promptly-after-an-install),
it does not wait out the interval for what an operator just did: see
[A pass runs promptly](#a-pass-runs-promptly).

Every answer the console gives carries the digest of the policy it was computed under; the controller changes nothing
on a holders list under another policy, and confirms no removal on an `Explain` under another one. Such a pass is tried
again within seconds, so a rollout needs no ordering.

The controller pushes metrics over OTLP when the platform sets
`OTEL_EXPORTER_OTLP_ENDPOINT` on its pod: passes, changes, rows by state, seats, breaker trips, links by
state and source, and the tick, lease and rate-limit series; traces carry a span per tick.
Nothing is exported without it. The signals and their alerts are in
[telemetry](../../reference/telemetry.md).

**It needs to reach `api.github.com`**, and a default-deny egress policy
has to allow it.


## Disconnecting

*Disconnect* uninstalls the App from the organisation, then forgets the
record and the key. GitHub's API cannot delete an App, so the
registration stays for its owner to delete; the console says where. Once
uninstalled and forgotten nothing can act through it — this service held
its only key. An uninstall that fails still forgets, and says what is
left to do on GitHub.

Nobody is removed from anything by disconnecting: the organisation simply
stops being managed.

## Runner Apps

A runner App is the GitHub App a self-hosted runner scale set registers
with: one per organisation per **tier**, so a compromised runner plane
is confined to its tier. The chart declares the tiers, and the *Runners*
tab then shows a row per bound organisation per tier:

```yaml
config:
  github:
    runnerTiers: [preview, stable]
```

An operator creates each App with the same two clicks as an
organisation's App — create, then install — and types nothing. The App
asks for `organization_self_hosted_runners: write` and nothing else; it
is private and has no webhook. Nothing in this service acts with its key:
it is kept for the deployment to hand to its runners.

Every runner App lives in `Secret <release>-github-runner-apps`. An
installed App is three keys, named as gha-runner-scale-set's
`githubConfigSecret` reads them — `<tier>.<org>.github_app_id`,
`<tier>.<org>.github_app_installation_id`,
`<tier>.<org>.github_app_private_key` — beside its record. A deployment
copies the three to its runners, for example with an External Secrets
`PushSecret`. Until the App is installed its key is kept under another
name, so a copy taken in between never hands runners an App they cannot
register with. *Disconnect* uninstalls the App and forgets its keys;
runners registered with it stop getting jobs.

Any other GitHub App a deployment needs — for dependency updates, for
releases — is declared in a catalogue and created the same way: see
[github-apps-catalogue.md](github-apps-catalogue.md).

## A pass runs promptly

Besides its interval (`config.controllers.github.interval`, 15 minutes), the controller
looks every 30 seconds at the mounted `<release>-github-apps` Secret and
`<release>-github-orgs` ConfigMap. When an organisation's own credential or
record changed (a new installation after **Install**, a reconnect, a
disconnect), it runs a full pass without waiting for the interval. Allow up to
about two minutes: the look is every 30 seconds, and the kubelet takes up to
about a minute to project a changed Secret or ConfigMap into the pod. Nothing
needs a restart.

**Refresh** on an installed organisation's page (operators of the organisation
only: the installation-wide operator, or the operator of the directory that
owns it) asks for a pass over it now. The console writes a marker
`_pass.<organisation>.json` into the records ConfigMap and the controller
notices it at its next look. A second request less than 60 seconds after the
first is refused (`resource_exhausted`), and an organisation whose App is not
installed has nothing to pass with and is refused (`failed_precondition`). The
page says *Pass requested* until a report newer than the request exists. The
pass is the full one: the controller publishes its reports as one document set,
so it never passes over one organisation alone. The marker is not a record, and
is forgotten when the organisation is disconnected. Each kept request is
audited as `roster.github_org.pass_requested`, naming the organisation and who
asked.

