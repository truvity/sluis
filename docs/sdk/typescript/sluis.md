# TypeScript package `@truvity/sluis`

Entry points: the root (`fetchIdentity()`, `Identity`), `/react` (`useIdentity()`, `<UserBadge/>`) and `/server` (a Node verifier). The Go counterpart is the [Go module](../go/sluis.md). Set the registry scope once, in the [SDK overview](../README.md#installing-from-github-packages).

```sh
yarn add @truvity/sluis@^1.8.0
```

`react` and `@mui/material` are optional peers.

```tsx
import { useIdentity, UserBadge } from "@truvity/sluis/react";

function Header() {
  const me = useIdentity();          // asks /.access/whoami once
  return <UserBadge identity={me} />;
}
```

The browser half parses no token. It asks the application, which has verified the caller. If `/.access/whoami` is not served, the UI renders as signed out. `useIdentity()` asks once on mount and aborts on unmount.

## Status

```ts
type Status = "loading" | "signed-in" | "signed-out" | "unknown";
```

A 401 or 403 reads as `signed-out`. A network failure or a 502 reads as `unknown`: show no sign-in button. `UserBadge` renders all four.

## Identity

```ts
interface Identity {
  status: Status;
  email?: string;
  name?: string;
  givenName?: string;
  familyName?: string;
  roles?: string[];    // what the policy grants: ["operator", "viewer"]
  groups?: string[];   // the internal groups behind those roles
  source?: string;     // "directory" | "forwarded" | "recovery"
  version?: string;    // the build the application is running
  signOutUrl?: string;
  error?: string;      // only when status is "unknown"
}
```

The endpoint is specified in [contracts](../../reference/sluis/contracts.md#the-whoami-endpoint).

## Server half

`/server` is the Go issuer anchor in TypeScript.

```ts
import { Issuer, middleware, requireGroups, whoami, whoamiPath, identityOf } from "@truvity/sluis/server";

const issuer = new Issuer({ url: "https://access.example", audience: "url-shortener-dev" });

app.use(middleware(issuer));                       // establishes, never refuses
app.get(whoamiPath, whoami(version));              // what useIdentity() asks
app.use("/admin", requireGroups("dev:url-shortener:deployer"));
app.get("/me", (req, res) => res.json(identityOf(req)));
```

`Issuer` checks signature, `iss`, expiry and an `aud` equal to your client id. The audience is required. Discovery is lazy.

The verifier accepts the algorithms the discovery document lists in `id_token_signing_alg_values_supported`. Without a list it accepts RS256, ES256, ES384 and ES512. It rejects `none` and HMAC. Pin with `algorithms: ["ES384"]`.

`Unverified` is a bad token. `IssuerUnreachable` is an outage: never answer it with a 401.

`middleware` reads `x-auth-request-access-token` or the bearer. `requireGroups` answers 401 for nobody and 403 for the wrong groups, and never names the groups that would work.

`identityOf(request)` returns `subject`, `email`, `name`, `givenName`, `familyName`, `groups` and `serviceAccount`. Do not authorize on the display names.

Only the issuer anchor ships here. A Node service reaches in-cluster callers through the issuer.
