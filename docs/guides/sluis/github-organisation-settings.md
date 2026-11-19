# Reconcile a GitHub organisation's settings

Let the GitHub controller keep an organisation's Actions variables, Actions secrets and code security configurations from a document the organisation owns.

## Before you start

- Give the organisation's App write access to organisation Administration, Secrets and Variables. Name it in `controllers.github.appRefs` ([connect a GitHub organisation](connect/github-organisation.md)).

- To switch an organisation already connected with an App of its own, install the declared App in it: the controller moves to it on its next pass. Until then `app_note` in the report says what is missing. The old App is shown as no longer used, not removed.

- Allow the controller's role `sts:AssumeRole` on the read role of step 1.

- Start in `report` mode, and remove any hand-made setting the document names: the controller never overwrites what it did not create.

## 1. Create the read role in the organisation's account

The role trusts only the controller's role, optionally with an external id, and reads the document and the secrets' parameters. Each organisation has its own role.

```json
{"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": [
  "arn:aws:ssm:eu-west-1:111122223333:parameter/example/sluis/github-org",
  "arn:aws:ssm:eu-west-1:111122223333:parameter/example/sluis/credentials/*"]}
```

```json
{"Effect": "Allow", "Principal": {"AWS": "<the controller's role>"}, "Action": "sts:AssumeRole",
 "Condition": {"StringEquals": {"sts:ExternalId": "example-id"}}}
```

## 2. Write the document

Store it as the parameter `/example/sluis/github-org`. It is the `org` section of a [company fragment](../../reference/sluis/policy-fragment.md). A secret names its parameter and never holds a value.

```yaml
org:
  variables:
    EXAMPLE_REGION: {value: eu-west-1, visibility: all}
  secrets:
    EXAMPLE_DEPLOY_KEY: {parameter: /example/sluis/credentials/deploy-key, visibility: private}
  security_configurations:
    baseline:
      description: Scanning on for every repository.
      settings: {secret_scanning: enabled}
      attach: {scope: all}
```

## 3. Declare the organisation

The installation lists the platform's variable names and sets their values. The organisation's document may not declare them.

```yaml
controllers:
  github:
    orgSettings:
      example-org:
        read: {roleArn: "arn:aws:iam::111122223333:role/sluis-read", region: eu-west-1, parameter: /example/sluis/github-org, externalId: example-id}
        platformVariables: ["CI_RUNNER_LABEL_*"]
        platformValued: [ACCESS_ROSTER_ISSUER]
        secretPrefix: /example/sluis/credentials/
        variables:
          CI_RUNNER_LABEL_DEFAULT: {value: self-hosted}
          ACCESS_ROSTER_ISSUER: {value: "https://sluis.example.com"}
```

## 4. Read the report

Read `settings` in the organisation's report ([GitHub organisation reference](../../reference/sluis/github-organisation.md)). `document.outcome` is `applied`, `refused` or `unreadable`. An empty document is `unreadable`. A row is `drift`, `conflict` (GitHub holds a name the controller did not create) or `held`, and each is one `roster.github_org_setting.changed` record ([audit actions](../../reference/sluis/audit-actions.md)).

## 5. Enforce

Add `mode: enforce` under the organisation, and the organisation to `enabledOrgs`, and roll out. The controller applies the rows and records what it wrote. Removing the organisation from `enabledOrgs` returns it to `report`.

A pass that would remove most of the names the controller wrote removes none, and reports `removal held`.

## Roll back

Remove `mode` and roll out. Nothing is undone, and no more is written. A name removed from the document leaves GitHub only if the controller wrote it.

Decided in: [ADR 0080](../../decisions/0080-the-github-controller-reconciles-an-organisations-settings-from-a-document-it-reads-at-run-time.md).
