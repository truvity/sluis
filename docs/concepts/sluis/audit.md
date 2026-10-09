# How does sluis record what happens?

sluis keeps no audit trail of its own. It records into an installation of [audit](../audit/README.md) that belongs to this application.

With `audit.writer` set, sluis registers its catalogue and sends every record to that address. With `audit.query` set, the console shows the installation's view as its Audit page. With neither, sluis keeps only the log line each record also is, and the console has no Audit page.

On AWS the sink is SQS ([ports](../../reference/sluis/ports.md#audit-sink)). To set it up, see [connect an audit installation](../../guides/sluis/connect-audit-installation.md).

## The catalogue defines every record

[`roster.yaml`](../../../internal/audit/catalogue/roster.yaml) declares each action (listed in [audit actions](../../reference/sluis/audit-actions.md)). An action has these fields:

- the kind of operation and the framework categories it answers;

- its `category`, `security` or `activity`, which decides the copies, retention and key a destination gives it;

- the types of its targets and the kinds of actor: a person, recovery, a CI job, a workload, the service itself;

- a schema for its data and a sentence template.

Every action has one constructor in [`internal/audit/events.go`](../../../internal/audit/events.go), and nothing else builds a record. `just audit-catalogue` checks the two against each other.

A catalogue change needs a new version and its released fixture. An installation refuses a changed document under a version it already holds. The writer refuses to start without the catalogue's `.json` schemas, which ship as the release asset `sluis-audit-catalogue_<version>.tar.gz`.

## Who is in a record

The actor is who acted. The subject is who the action concerns, and often differs: an operator revokes a person's sessions, the controller invites a person.

A person is named by the address the directory knows, kept in clear. A GitHub login is treated the same way. The installation runs no pseudonymisation keys.

Every record belongs to the audit tenant `@platform`. The process presents its own projected service-account token, or its cloud identity on Lambda, and the installation stamps that verified workload as the observer. The controllers inside the process report through the same path.

An event caused by a request keeps the client's address, its User-Agent, the gateway's `X-Request-Id` and the trace. The outermost handler reads them once and passes them in the request context, including to the token endpoint's storage.

The address is the peer's. To trust forwarded addresses, set `audit.forwardedForTrustedHops` to your proxy count, counting the peer as one. The service then reads the forwarded chain from the right.

## The Audit page reads as the person

The console forwards the page's calls to the query service with a token it mints for the signed-in person. The token goes through the same decision as a token exchange, targets the installation's audience and lasts five minutes.

No token reaches the browser, and no call crosses origins. The person sees what the installation's grants allow, and the installation records every read. The page asks the query service which profiles the person may read.

## Recording never fails what is being recorded

Almost every action goes on a bounded queue in the process before the request completes, and reaches the writer when it can. A slow trail never refuses a sign-in. To handle an unreachable installation, see [read the audit trail](../../guides/sluis/operate/read-the-audit-trail.md#3-when-the-installation-cannot-be-reached).

A recovery sign-in fails closed. Its catalogue entry is `block`: the record must be in the installation before the sign-in succeeds, at the issuer and at the console's own door. When it cannot be, sluis refuses the sign-in and records the refusal.

Recovery therefore depends on the installation's writer. A deployment with no installation connected has no trail and does not refuse recovery ([recovery](recovery.md)).

## The audit writer deploys first

Recording is best effort, so an issuer that emits a field its writer's catalogue does not know loses those records. Deploy a catalogue change to the audit installation before the issuer that emits it.

A Kubernetes writer receives the catalogue when the service registers it. A Lambda writer needs the new release asset, schemas included ([connect an audit installation](../../guides/sluis/connect-audit-installation.md#before-you-start)).

## Decided in

- [ADR 0055: No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md)
- [ADR 0053: One installation per service or product](../../decisions/0053-one-installation-per-service-or-product.md)
