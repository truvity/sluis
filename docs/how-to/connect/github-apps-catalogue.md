# A catalogue of GitHub Apps

> **Built.** Declaring, creating, installing, drift and the kept key ship
> in 1.11, and so does minting installation tokens under the grants: a
> job or a person holding a granted group exchanges its proof at the
> issuer's `/token` for a token of the App, narrowed to what it asked for
> and never more than one grant allows.

**Anchor:** none of ours. An App is created on GitHub by an owner of its
organisation, from a manifest this service posts, and GitHub hands the
App's private key to this service once. Nothing is typed or pasted.

Minting tokens under the grants is [Mint a GitHub App token](github-app-tokens.md); keeping and backing up the key is
[Keep and back up a GitHub App's key](github-app-keys.md); states, drift and the objects left behind are in
[GitHub catalogue reference](../../reference/github-apps.md).

A deployment usually needs more GitHub Apps than the three this service
creates for itself — one for dependency updates, one for releases, one
for a bot that labels pull requests. Each is the same chore by hand: fill
in a form on GitHub, pick permissions, download a key, put the key
somewhere, install the App, write down its ids. The catalogue turns each
into a declaration in the deployment's values and two clicks in the
console.

## Declaring an App

```yaml
githubApps:
  catalogue:
    - id: renovate                 # [a-z0-9-], at most 32, unique; never changes
      org: example-org             # the organisation the App is created under
      name: example-org-renovate   # optional; default <org>-<id>, at most 34
      description: Dependency updates for example-org
      public: false                # a private App installs only on example-org
      permissions:                 # GitHub's permission names: read | write | admin
        contents: write
        pull_requests: write
        issues: write
        workflows: write
      events: []                   # optional; delivered only if `webhook` is set
      export: true                 # set it with a `webhook`: consumers read its secret from the exported document
      # webhook:                   # optional; see "Delivering events"
      #   url: https://argocd.example/api/webhook
      installation: all            # all | selected (default): what the installer is expected to pick
      grants:
        - group: all:platform:engineer
          repositories: ["*"]
          permissions: {contents: read, pull_requests: read}
        - group: all:docs:writer
          repositories: [docs, "site-*"]
          permissions: {contents: write, pull_requests: write}
```

| Field | Meaning |
|---|---|
| `id` | the App's name **here**: where it is kept and what a token request will name. Lower-case letters, digits and dashes, at most 32. Renaming it forgets the App (see [Disconnecting](#disconnecting)) |
| `org` | the organisation's login. The App is created under it, and an App created under anything else is refused |
| `name` | the App's name on GitHub, which is global across GitHub. Empty is `<org>-<id>`, cut to 34 characters; a declared name over 34 is refused |
| `description` | shown on the App's page on GitHub and in the console |
| `public` | whether any account may install it. Leave it `false` unless something outside the organisation installs the App |
| `permissions` | the App's permissions, by the names GitHub's REST API uses (`contents`, `pull_requests`, `members`, `organization_administration`, …), each `read`, `write` or `admin`. At least one |
| `events` | the webhook events the App subscribes to. They are delivered only to an App that declares a `webhook`; without one the webhook is inactive and nothing in this service receives anything |
| `webhook` | optional: where GitHub delivers the events, a `url` or a `kargo` receiver — see [Delivering events](#delivering-events). Needs non-empty `events` |
| `installation` | `all` or `selected`. GitHub's install page is where the installer chooses; this says which choice the deployment expects, and the console shows both |
| `grants` | who may ask for a token of this App — see below |
| `push` | optional: copy this App's credential to a secret store, for a consumer that cannot ask the issuer when it runs — see [Projecting one App to a secret store](github-app-keys.md#projecting-one-app-to-a-secret-store) |

The catalogue is rendered into `ConfigMap <release>-github-apps-catalogue`,
mounted, and read once at start; a change rolls the service out. **The
service refuses to start** on an unknown key, a duplicate id, a level
that is not `read`, `write` or `admin`, a name GitHub would refuse, a
grant above the App, a glob that does not compile, or a grant naming a
group the policy does not declare. A wrong declaration found at start
costs a rollout; one found after the App was created costs an owner's
edit on GitHub, because nothing here can change an App's permissions.

## Delivering events

!!! note "Preview"
    This is a preview. It is off unless an App declares `webhook:`. It has not yet been verified against real GitHub:
    whether a redelivery is re-signed with the current secret, and the hash Kargo expects when the project is empty,
    are untested. The console has no *Rotate webhook secret* button yet (the `RotateGitHubAppWebhook` RPC exists).
    GitHub has no API to switch the webhook on for an App that already exists, so an existing App has to be
    disconnected and created again to get one.

An App with a `webhook` is created with an active webhook. Its secret is **generated by this service** and set on
GitHub right after the App is created; you never type or see it. Consumers read it from the App's exported
document, `external/github/<id>` field `webhook_secret` (so give the entry `export: true`, and the App must be
installed before a consumer can read it).

Argo CD takes one URL and one secret and verifies `X-Hub-Signature-256` on `push` and `ping`:

```yaml
githubApps:
  catalogue:
    - id: argocd-webhook
      org: example-org
      permissions: {contents: read}
      events: [push]
      export: true
      webhook:
        url: https://argocd.example/api/webhook
```

An ExternalSecret copies the field into the key Argo CD reads (`webhook.github.secret` in `argocd-secret`):

```yaml
data:
  - secretKey: webhook.github.secret
    remoteRef: {key: github/argocd-webhook, property: webhook_secret}
```

Kargo derives a receiver's path from its secret, so its URL is not declared but computed: `<base>/github/` and the
hex SHA-256 of the project, the receiver's name and the secret, joined with nothing. Declare where Kargo serves
receivers, and which one:

```yaml
      webhook:
        kargo:
          base: https://kargo-webhooks.example
          receiver: github            # the receiver's name in the project
          project: apps               # empty for a cluster-scoped receiver
```

and give the Kargo receiver the same `webhook_secret` as its `secretRef`.

**Rotating the secret** is the RPC `RotateGitHubAppWebhook` (a console button is not built yet). Neither consumer
accepts two secrets, so there is no overlap, and the order is what keeps deliveries from failing: the console keeps
the new secret where the consumers read it, sends the (new) target a signed `ping` until it answers `2xx` (two
minutes at most), and only then tells GitHub the new secret — and, for Kargo, the new URL — in a single call. If the
target does not accept the new secret in time (the ExternalSecret has not refreshed, or the consumer has not
reloaded), the console puts the previous secret back and says so: nothing was lost, and you rotate again once the
consumer follows. The same verb finishes a webhook whose first setup failed.

The *Check* of an App with a webhook also compares GitHub's webhook URL, content type and secret presence with what
was set; a hand edit on GitHub shows as drift, and a rotation repairs it. **An App created without a webhook cannot be
given one**: GitHub has no API to switch the webhook on for an existing App, so disconnect it and create it again.

## A default set

Every estate on GitHub needs the same few automations, and every estate
builds them by hand. The set below is shipped as values to copy —
`charts/sluis/examples/github-apps.yaml` in this repository —
rather than as a default the chart applies, because creating an App is an
owner of the organisation confirming a manifest, and that stays a
deliberate act.

| App | Does | Permissions | Installed on |
|---|---|---|---|
| `renovate-public` | dependency updates in **public** repositories | `contents: write`, `pull_requests: write`, `issues: write`, `workflows: write`, `checks: read` | the public repositories, selected |
| `renovate-private` | the same, in **private** repositories | the same | the private repositories, selected |
| `ci-automation` | approves bot pull requests; cuts release tags | `contents: write`, `pull_requests: write`, `checks: read` | selected |
| `iac` | the program that manages the organisation: repositories, teams, rulesets, settings | `organization_administration: write`, `members: write`, `administration: write`, `contents: read` | all repositories |

**Why four identities and not one.** An App *is* an identity, and its
permissions are the whole of what a stolen key can do. One App with every
permission is one key that can do everything, installed everywhere, used
by every automation — and nothing reading the audit trail afterwards can
say which automation acted. Four Apps cost four creations once, and each
is separately installable, scoped and revocable.

The three separations that carry weight:

- **Public and private dependency updates are two Apps.** The split is
  the privilege boundary, and the **installation** enforces it: the
  public App is installed on the public repositories only, so the App
  whose pull requests, logs and forks are world-readable cannot read a
  private repository at all. One App installed on both would be a single
  key reaching everything, and no setting inside the updater would change
  that.
- **`ci-automation` approves, and cannot change what it satisfies.** An
  App's review counts as an approving review, so a ruleset requiring one
  is satisfied without a person rubber-stamping a version bump, and the
  approval is a reviewable act in the pull request's timeline. It holds
  no administration permission: an approver that can edit the rule it
  satisfies satisfies nothing.
- **`iac` administers, and approves nothing.** It holds
  `administration: write`, which includes a repository's rulesets and
  branch protection. Keep it out of the approving and merging path even
  when one pipeline runs both — that is the same rule read from the other
  side.

`workflows: write` is on the updaters because a dependency update touches
`.github/workflows` (an action pinned by digest is a dependency like any
other) and GitHub refuses such a push from a token without it. `metadata:
read` is on every App; GitHub gives it to anything that touches
repositories, and it is never drift.

Copy the entries, change `example-org`, add `grants` for whoever should
be able to mint tokens of each, and for `iac` add the `push` block that
[hands its credential to the program](infrastructure-as-code.md).

## Creating and installing

The *Apps* tab of the GitHub page lists every App this service keeps a
key for or is declared to — the link App, each organisation's controller
App and every App the catalogue declares — grouped by organisation, those
needing an operator first. The runner tiers' Apps are on the *Runners* tab,
in the same shape. Each App has a page of its own.

1. **Create.** An operator presses *Create* on the App's page. The browser posts the App's
   manifest to GitHub's create page for the organisation; an owner of
   the organisation confirms. GitHub returns to
   `/connect/github/catalogue/callback` with a one-time code, and the
   service exchanges it for the App's id and private key. The App now
   reads *created, not installed*.
2. **Install.** The browser goes straight on to the App's install page.
   The owner picks all or selected repositories and installs. GitHub
   returns to `/connect/github/catalogue/setup`; the service asks GitHub,
   as the App, where it is installed on the organisation — the id in the
   redirect is a browser's word for it — and keeps that.

If the owner stops between the two, the App's page offers *Install*,
which picks up where they left off rather than creating a second App.
Both redirects check the flow cookie against a state signed by this
service naming the App and the operator who started; a state from any
other GitHub flow is refused.

## Who may

Creating, installing, re-checking and disconnecting a catalogue App need the
**operator** role over the directory recorded as the owner of the App's
organisation (the operator role `<directory id>:access-roster:operator`), or the
installation-wide operator. An App of an organisation nobody has connected yet
can be created by anyone who could connect that organisation (see
[Connect a GitHub organisation](github-organisation.md#adding-one-organisation));
that connect records the owner. A viewer sees the Apps of the organisations they
may view, and each row says whether they may operate it. Only the
installation-wide operator changes an organisation's owner, with *Change owner*
on the organisation's page or its controller App's page. The callbacks ask the
role question again of whoever is signed in then.

## Disconnecting

*Disconnect* uninstalls the App from the organisation, then forgets its
record and every key it had here. A failed uninstall still forgets, and
says what is left to do on GitHub. **It does not delete the App on
GitHub** — the API cannot — so the registration stays for its owner to
delete in the App's settings, which the console links to.

An App whose entry is removed from the catalogue, or whose `id` changes,
stays listed as *no longer declared* so it can be disconnected; it cannot
be created again under that id until it is declared again.
