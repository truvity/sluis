# TypeScript package `@truvity/audit`

```sh
yarn add @truvity/audit      # after the registry scope in the SDK overview
```

Read an audit trail from TypeScript: the typed query client, the qualifier box compiled to the typed filter, and
records as the sentences their catalogues declare. It is the TypeScript counterpart of the
[Go query client](../go/audit-query.md); the React hooks and the default view are a separate package,
[`@truvity/audit-react`](audit-react.md). The registry scope and token are set once, in
[the SDK overview](../README.md#installing-from-github-packages). The package is published at the release's version
and holds the generated contract (`QueryService`, `SearchRequest`, `Filter`, `Record`, …) next to the helpers below.

Who may read what is decided by the query service's grants, not by the package; the service is described in
[read the trail](../../guides/audit/connect/read-the-trail.md) and its wire contract in the [API reference](../../reference/audit/api.md).

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

**The transport is the host's, because the credentials are.** The client authenticates nothing itself; it sends what
the transport sends, and the service verifies a bearer token from an issuer its grants name. `useProtoFieldName` keeps
the JSON the same as the API reference shows.

## Qualifiers: a typed filter from a box

`compileQualifiers(text)` compiles what a person types into the typed `Filter` the service takes. It never becomes free
text on the server, so every token either names a field or is an error the box can show.

```ts
import { compileQualifiers } from "@truvity/audit";

const { filter, errors } = compileQualifiers("action:wallet.issue* outcome:failure,denied -actor.kind:service since:24h");
const page = await audit.search({ profile: "security", filter: [filter], limit: 50 });
```

| token | means |
|---|---|
| `actor:alice` | the field, exactly |
| `action:wallet.issue*` | a trailing `*` is a prefix |
| `outcome:failure,denied` | a comma is any of |
| `-outcome:success` | a leading `-` excludes |
| `target:credential:c-1` | a target by type and id; `target:credential` is any of that type |
| `data.channel:web` | a filterable property of the data slot, by its path |
| `since:24h`, `until:2026-09-18` | when the event occurred: a duration (`24h`, `7d`) or a date |
| `"request:a b"` | quotes keep a value with spaces together |

The fields are `id`, `action`, `source`, `operation`, `outcome`, `tenant`, `actor`, `actor.kind`, `subject`,
`subject.kind`, `request`, `trace`, `client`, `observer` and `meter`; `qualifierNames` lists them for help text.
`errors` holds one message per token the box could not read, and the filter leaves those tokens out.
`qualifier(field, value, exclude?)` writes the token that narrows to, or away from, one value: what a row's
filter-for and filter-out buttons append to the box.

## Records as sentences

A catalogue says how each of its actions reads. `audit messages <catalogue.yaml>` prints that as JSON at build time,
and an application ships its own next to its console.

```ts
import { Sentencer } from "@truvity/audit";
import shop from "./audit-sentences.json";

const sentences = new Sentencer([shop], "en");
sentences.sentence(record);   // "Alice refused order 1042"
sentences.summary(record);    // what the action is, for a tooltip
```

The common catalogue, which describes the audit component's own `audit.*` actions, is always included
(`commonSentences`). A record names the catalogue version it was written under; that version's template is used when
present, otherwise the newest version of the source that has the action. **A record is never left without words:** a
template that does not render falls back to the action's summary, and an action no catalogue knows reads as its
action name.

## Paging and one record

Paging is the Go page's loop in TypeScript: pass `cursor: page.cursors?.next` to the next `search` until a page comes
back short or without one, and keep the last cursor to ask later for what was recorded since. `audit.get({ profile,
id })` returns one record with its provenance. The hooks in [`@truvity/audit-react`](audit-react.md) do both for a
view.
