# TypeScript package `@truvity/audit`

```sh
yarn add @truvity/audit      # after the registry scope in the SDK overview
```

A typed query client, a qualifier box compiled to a typed filter, and records as sentences. It mirrors the [Go query client](../go/audit-query.md). React hooks live in [`@truvity/audit-react`](audit-react.md). Set the registry scope once, in the [SDK overview](../README.md#installing-from-github-packages).

The query service's grants decide who reads what: see [read the trail](../../guides/audit/connect/read-the-trail.md) and the [API reference](../../reference/audit/api.md).

## The client

```ts
import { createConnectTransport } from "@connectrpc/connect-web";
import { createQueryClient } from "@truvity/audit";

const audit = createQueryClient(createConnectTransport({
  baseUrl: "https://audit-query.example.com",
  jsonOptions: { useProtoFieldName: true },
  interceptors: [(next) => async (req) => {
    req.header.set("Authorization", `Bearer ${await session.token()}`);
    return next(req);
  }],
}));

const page = await audit.search({ profile: "security", limit: 50 });
```

The client sends what your transport sends, so the transport carries the bearer token. `useProtoFieldName` matches the JSON in the API reference.

## Qualifiers: a typed filter from a box

`compileQualifiers(text)` compiles typed text into the `Filter` the service takes. Every token names a field or is an error.

```ts
import { compileQualifiers } from "@truvity/audit";

const { filter, errors } = compileQualifiers("action:wallet.issue* outcome:failure,denied -actor.kind:service since:24h");
const page = await audit.search({ profile: "security", filter: [filter], limit: 50 });
```

| token | means |
|---|---|
| `actor:alice` | the field, equal to the value |
| `action:wallet.issue*` | a trailing `*` is a prefix |
| `outcome:failure,denied` | a comma is any of |
| `-outcome:success` | a leading `-` excludes |
| `target:credential:c-1` | a target by type and id; `target:credential` is any of that type |
| `data.channel:web` | a filterable property of the data slot, by its path |
| `since:24h`, `until:2026-09-18` | when the event occurred: a duration (`24h`, `7d`) or a date |
| `"request:a b"` | quotes keep a value with spaces together |

The fields are `id`, `action`, `source`, `operation`, `outcome`, `tenant`, `actor`, `actor.kind`, `subject`, `subject.kind`, `request`, `trace`, `client`, `observer` and `meter`.

`qualifierNames` lists them for help text.
`errors` holds one message per unreadable token, and the filter omits it. `qualifier(field, value, exclude?)` writes the token that narrows to, or away from, one value.

## Records as sentences

`audit messages <catalogue.yaml>` prints a catalogue's sentences as JSON at build time. Ship them with your console.

```ts
import { Sentencer } from "@truvity/audit";
import shop from "./audit-sentences.json";

const sentences = new Sentencer([shop], "en");
sentences.sentence(record);   // "Alice refused order 1042"
sentences.summary(record);    // what the action is, for a tooltip
```

`commonSentences` covers the `audit.*` actions and is always included. A record uses its own catalogue version's template, else the newest version with the action. A template that fails falls back to the action's summary, then to the action name.

## Paging and one record

Pass `cursor: page.cursors?.next` to the next `search` until a page comes back short or without one. Keep the last cursor to fetch later records. `audit.get({ profile, id })` returns one record with its provenance.
