# Connect an application to its trail

Give an application a catalogue, one constructor per action, the emit library, a CI check and the Audit page.

## Before you start

- Run an installation in the application's namespace, on [Kubernetes](../../../get-started/audit/kubernetes.md) or [AWS Lambda](../../../get-started/audit/aws-lambda.md).

- A changed catalogue under an unchanged `version` stops the application at start ([rollout order](change-what-a-source-records.md)).

- Do not hash identifiers, embed a writer or read the bucket from the application. Use the query service.

## Steps

### The catalogue

Keep one YAML document and a JSON Schema per data-carrying action in the application's repository ([catalogue reference](../../../reference/audit/catalogue.md)).

```yaml
source: shop            # every action is under this namespace
version: "1.0.0"        # a new version for any change to what a record carries
locales: [en]

actor_kinds:            # who can act, and how profiles treat them
  customer: { category: external }   # an identifier the application minted
  clerk:    { category: internal }   # staff, kept in clear for accountability
  service:  { category: machine }

target_types:           # what an action can be about
  order: { description: "An order." }

actions:
  shop.order.placed:
    summary: A customer placed an order.
    operation: create                     # create, access, modify, remove, authentication, transfer, restore
    categories: [data_change]             # what frameworks call it
    category: security                    # which destinations keep a copy (profiles: is deprecated)
    target_types: [order]
    delivery: async                       # block or async
    data_schema: https://schemas.example.com/shop/order-placed.json
    message: { en: "{actor} placed order {targets_0_id}" }
```

Name each action `source.thing.verb`, in the past tense. Choose [`block` or `async`](../../../reference/audit/catalogue.md#delivery) per action. The application registers the catalogue at start-up and stops if the receiver refuses it. Do not retry a refusal.

### One constructor per action

Spell each action name once, in a constructor. `check-emitters` cannot see a name built at run time.

```go
// Package shopaudit is the only place an action name is spelled.
package shopaudit

import (
	auditv1 "github.com/truvity/sluis/audit/sdk/gen/audit/v1"
	"github.com/truvity/sluis/audit/sdk/record"
)

func OrderPlaced(tenant, customerID, orderID string) *record.Record {
	return &record.Record{
		Action:    "shop.order.placed",
		Operation: auditv1.Operation_OPERATION_CREATE,
		TenantId:  tenant,
		Actor:     &record.Actor{Kind: "customer", Id: customerID},
		Targets:   []*record.Target{{Type: "order", Id: orderID}},
		Outcome:   &record.Outcome{Result: auditv1.Outcome_RESULT_SUCCESS},
	}
}
```

### The emit library

```go
// At start-up: the catalogue reaches the receiver, or the application stops.
if err := emit.Register(ctx, emit.Registration{
	URL: receiverURL, Source: shop.Source, Version: shop.Version,
	Document: document, Schemas: schemas, HTTP: client,
}); err != nil {
	return err
}

emitter, err := emit.New(emit.Options{
	Source:    shop.Source,
	Catalogue: shop,
	Sink:      sink.NewClient(client, receiverURL),
	Version:   build.Version,
	Instance:  os.Getenv("HOSTNAME"),
	Hooks:     hooks, // without OnDropped the emitter logs each drop itself
})

// Wherever the action happens:
err = emitter.Record(ctx, shopaudit.OrderPlaced(tenant, customerID, orderID))
```

The emitter sends the pod's projected token, audience `audit` by default. Tenants, middleware and metrics are in [emit records](emit-records.md).

### The CI check

```sh
audit validate catalogue/shop.yaml
audit check-emitters . --catalogue catalogue/shop.yaml
```

`check-emitters` fails on an action the code emits and the catalogue lacks, and the reverse. Add `--deployment` to `validate` to check the composed profiles.

### The Audit page

The console calls the query service with the person's session token. The [grants](read-the-trail.md#access) name the gateway's issuer and audience.

```tsx
import { createConnectTransport } from "@connectrpc/connect-web";
import { createQueryClient } from "@truvity/audit";
import { AuditProvider, AuditView } from "@truvity/audit-react";
import shop from "./audit-sentences.json"; // `audit messages catalogue/shop.yaml`

const audit = createQueryClient(createConnectTransport({
  baseUrl: "https://audit-query.example.com",
  jsonOptions: { useProtoFieldName: true },
  interceptors: [(next) => async (req) => {
    req.header.set("Authorization", `Bearer ${await session.token()}`);
    return next(req);
  }],
}));

<AuditProvider client={audit} sentences={[shop]}>
  <AuditView permalink={(p, id) => `/audit/${p}/${id}`} />
</AuditProvider>
```

Generate the sentences with `audit messages catalogue/shop.yaml > audit-sentences.json`. A console in the cluster reaches the Service `<fullname>-query`. If it runs outside, set `query.route` and list the gateway's namespace in `networkPolicy.queryIngressFrom` ([chart README](../../../../charts/audit/README.md#publishing-the-query-service)). The view is described in [the Audit page](../../../concepts/audit/audit-page.md).

### A console with a session of its own

If the session is a cookie, proxy `/audit/` through the console backend.

```
browser ──(the console's cookie)──▶ console backend ──(a short token per person)──▶ query service
         /audit/audit.v1.QueryService/*                  Authorization: Bearer …
```

The backend refuses anyone not signed in and adds a bearer token for that person, from a granted issuer, with the installation's audience. Set `baseUrl` to `/audit`. Prefer the direct call.

## Verify

- Run `audit conformance --query <url> --profile security --token-file token` with a token that may read.

- Stop the receiver. A `block` action fails; an `async` action succeeds and appears once the receiver returns.

## Decided in

[0053](../../../decisions/0053-one-installation-per-service-or-product.md), [0054](../../../decisions/0054-two-deliveries-and-a-durable-ack.md), [0055](../../../decisions/0055-no-pseudonymisation-keys-by-default.md).
