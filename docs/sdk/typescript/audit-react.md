# TypeScript package `@truvity/audit-react`

```sh
yarn add @truvity/audit-react @truvity/audit    # after the registry scope in the SDK overview
```

React hooks over the [`@truvity/audit`](audit.md) query client, and a default MUI audit view. It is a package of its
own, published at the release's version (the same as `@truvity/audit`), so a console that only reads the trail with
its own components does not pull in React or MUI. The registry scope and token are set once, in
[the SDK overview](../README.md#installing-from-github-packages).

**Peer dependencies:** `react` (19 or later), `@mui/material` (7 or later), `@bufbuild/protobuf` and
`@connectrpc/connect`. The package holds no credentials and signs nobody in: the host passes a client over its own
transport.

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

`AuditProvider` takes the `client`, the `sentences` of the application's catalogues (the common one is always
included) and a `locale` (default `en`). `AuditView` shows profiles along the top, the qualifier box and a time range,
counts to narrow by, and the records as sentences, newest first. A row opens to the record itself.

| `AuditView` prop | meaning |
|---|---|
| `profiles` | the profiles to show, in order; without it the view asks the query service which the caller may search |
| `profile` | the profile to open on; default the first |
| `query` | the qualifier box's starting text |
| `facets` | fields to count in the sidebar; empty hides it; default action and outcome |
| `pageSize` | records per page |
| `permalink` | a link to one record, for "copy link"; without it the button is hidden |

What the view shows is what the caller's grant lets through. How a console wires the page, the route and the
network policy is in [connect an application](../../audit/how-to/connect-an-application.md#the-audit-page); what the
view does with a record is in [the Audit page](../../audit/explanation/audit-page.md).

## Hooks, for a view of your own

All hooks read the client and the sentences of the nearest `AuditProvider` (`useAudit()` returns them; it throws outside
one). Errors come back as `ReadError`, `{ code, message }`, not thrown.

| hook | returns |
|---|---|
| `useSearch(profile, filter?, pageSize = 50)` | `records`, `loading`, `error`, `more`, `loadMore()`, `reload()`; changing the profile or filter starts again from the first page |
| `useTail(profile, filter, enabled, everyMs = 5000)` | `records` recorded since the tail started; it follows the service's cursor, so a late-arriving event is shown, not skipped |
| `useFacets(profile, filter, fields, limit = 10)` | counts of the values of fields under the same filter |
| `useRecord(profile, id)` | one record, where it is kept and whether a verified digest covers it |
| `useAccess()` | each profile the caller may read, and what they may do on it |

A searcher that cannot order by recorded time refuses `useTail`; the error says so. Compile a typed filter from a box
with `compileQualifiers` from [`@truvity/audit`](audit.md#qualifiers-a-typed-filter-from-a-box).
