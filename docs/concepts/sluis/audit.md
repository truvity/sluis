# Audit

sluis does not keep its own audit trail. It records into an installation of
[audit](../audit/README.md) that belongs to this application: with `audit.writer` set it
registers its catalogue and sends its records to that one address, and with `audit.query` set it shows the
installation's view as the console's Audit page; with neither it keeps nothing beyond the log line every record also
is, and has no page. On AWS the sink is SQS ([ports](../../reference/sluis/ports.md#audit-sink)). Setting it up: [connect an audit installation](../../guides/sluis/connect-audit-installation.md).

**The catalogue is the model.** [`internal/audit/catalogue/roster.yaml`](../../../internal/audit/catalogue/roster.yaml)
declares every action (listed in [audit actions](../../reference/sluis/audit-actions.md)) with what kind of operation it is,
the framework categories it answers, its `category` (`security` or `activity`: the installation's destinations take records by category, so the estate decides which copies, retention and key each gets; the deprecated per-action `profiles` list is no longer used), the types of its targets, the kinds
of actor (a person, recovery, a CI job, a workload, the service itself), a schema for its data, and how it reads as a
sentence. Every action has one constructor in [`internal/audit/events.go`](../../../internal/audit/events.go), and
nothing else builds a record, so the vocabulary is fixed by the compiler; `just audit-catalogue` holds the two
together with the audit component's own validator and emitter check. A change to the catalogue needs a new version
and its released fixture; an installation refuses a changed document under a version it already holds. The writer
refuses to start without the catalogue's `.json` schemas, which ship as the release asset
`sluis-audit-catalogue_<version>.tar.gz`.

**Who is who.** The actor is who acted, by kind; the subject is who it concerns, and differs from the actor as often
as not: an operator revokes a person's sessions, the controller invites a person. A person is named by the address
the directory knows them by, and the trail keeps it in clear, because a trail of staff whose subjects are
pseudonyms answers none of the questions it exists for; the installation runs no pseudonymisation keys. A GitHub
account is a person's, so its login is treated the same way. No address is ever data.

**Every record belongs to the installation**, the audit tenant `@platform`: an installation of sluis serves one
organisation, and its trail is the organisation's own.

**The process records as itself.** It presents its own projected service-account token (or its cloud identity on
Lambda), and the installation stamps the verified workload as every record's observer. The controllers inside the
process report through the same path: a component that can write to the trail directly, as itself, needs no one to
vouch for it.

An event caused by a request keeps where it came from: the client's address, its User-Agent, the gateway's
`X-Request-Id` and the trace. The server reads them once, at the outermost handler, and they travel in the request's
context to wherever the record is made, including the token endpoint's storage, which an OpenID library calls with a
context and nothing else. The address is the peer's unless the deployment says how many of its own proxies sit in
front of the service (`audit.forwardedForTrustedHops`), counted the way the audit emitter counts them, with the peer
as one: the forwarded chain is then read from the right, because only the right end is written by those proxies.

**The Audit page reads as the person.** The console forwards the page's calls to the query service with a token it
mints for the person signed in, through the same decision a token exchange makes, for the installation's audience,
lasting five minutes. No token reaches the browser and there is no cross-origin call; what anyone may read is the
installation's grants', and every read is recorded there. The page asks the query service which profiles the person
may read.

## Recording never fails what is being recorded

A sign-in that could not be written down still happened, and refusing it because the trail was slow would turn an
audit outage into an access outage: almost every action goes on a bounded queue in the process before the request
completes, and reaches the writer when it can. What to do when the installation cannot be reached: [read the audit trail](../../guides/sluis/operate/read-the-audit-trail.md#3-when-the-installation-cannot-be-reached).

**The one exception is a recovery sign-in, which fails closed.** Its catalogue entry is `block`: the record is in the
installation before the sign-in succeeds, at the issuer and at the console's own door alike, and when it cannot be,
the sign-in is refused and the refusal recorded. Recovery is the way in that bypasses the directory, so a recovery
that left no trace is the one gap an auditor most needs to be impossible. It does make recovery depend on the
installation's writer, which is the price; a deployment with no installation connected does not refuse recovery,
because it has no trail to keep it in ([recovery](recovery.md)).

## The audit writer goes first

Recording is best effort, so an issuer that emits a field its writer's catalogue does not know loses those records
rather than failing. A catalogue change therefore reaches the audit installation before the issuer that emits it. This
matters for catalogue 1.10.0: `roster.person.signed_in` gains the session's `class` and `deadline`, and
`roster.session.ended` gains `spared`, the client ids of the agent sessions a sign-out kept running. Deploy the audit
writer with catalogue 1.10.0 first, then the issuer. A writer on Kubernetes receives the catalogue when the service
registers it; a Lambda writer needs the new release asset, schemas included
([connect an audit installation](../../guides/sluis/connect-audit-installation.md#before-you-start)).
