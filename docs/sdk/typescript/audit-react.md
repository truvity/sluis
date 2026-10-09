# TypeScript package `@truvity/audit-react`

```sh
yarn add @truvity/audit-react @truvity/audit    # after the registry scope in the SDK overview
```

React hooks over the [`@truvity/audit`](audit.md) query client, and a default MUI audit view. Set the registry scope once, in the [SDK overview](../README.md#installing-from-github-packages).

Peer dependencies: `react` 19 or later, `@mui/material` 7 or later, `@bufbuild/protobuf` and `@connectrpc/connect`. You pass a client over your own transport; the package holds no credentials.

## The Audit page in a console

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

`AuditProvider` takes the `client`, the `sentences` of your catalogues and a `locale` (default `en`). `AuditView` shows profiles, the qualifier box, a time range, counts to narrow by, and records as sentences, newest first.

| `AuditView` prop | meaning |
|---|---|
| `profiles` | the profiles to show, in order; without it the view asks the query service which the caller may search |
| `profile` | the profile to open on; default the first |
| `query` | the qualifier box's starting text |
| `facets` | fields to count in the sidebar; empty hides it; default action and outcome |
| `pageSize` | records per page |
| `permalink` | a link to one record, for "copy link"; without it the button is hidden |

The caller's grant limits what the view shows. Wiring is in [connect an application](../../guides/audit/connect/connect-an-application.md#the-audit-page). Behaviour is in [the Audit page](../../concepts/audit/audit-page.md).

## Hooks, for a view of your own

Hooks read the nearest `AuditProvider`. `useAudit()` returns it and throws outside one. Errors return as `ReadError`, `{ code, message }`.

| hook | returns |
|---|---|
| `useSearch(profile, filter?, pageSize = 50)` | `records`, `loading`, `error`, `more`, `loadMore()`, `reload()`; changing the profile or filter starts again from the first page |
| `useTail(profile, filter, enabled, everyMs = 5000)` | `records` recorded since the tail started; it follows the service's cursor, so a late-arriving event is shown, not skipped |
| `useFacets(profile, filter, fields, limit = 10)` | counts of the values of fields under the same filter |
| `useRecord(profile, id)` | one record, where it is kept and whether a verified digest covers it |
| `useAccess()` | each profile the caller may read, and what they may do on it |

A searcher that cannot order by recorded time refuses `useTail`. Build filters with [`compileQualifiers`](audit.md#qualifiers-a-typed-filter-from-a-box).
