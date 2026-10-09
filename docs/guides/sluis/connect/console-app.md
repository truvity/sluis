# Connect a console

Give a web console for people a login against the issuer. A service API that workloads call is a second listener: see [service to service](service-to-service.md).

## Before you start

- Pick the shape first with [choose native or gateway OIDC](choosing-native-or-gateway-oidc.md).
- A console keeps its own hostname. Only the directory console shares the issuer hostname, under `/console/` ([console](../../../concepts/sluis/console.md)).
- Never list your sign-out landing page in `redirects`. The render refuses an address in both `redirects` and `signed_out`.

| | Gateway OIDC | Own flow |
|---|---|---|
| Login runs in | The gateway `SecurityPolicy`, which forwards a verified bearer | The console, which sends the browser to `/authorize` |
| Client kind | `confidential`, the gateway holds the secret | `public`, with PKCE |
| You deploy | Your chart and a [`SecurityPolicy`](envoy-gateway-oidc.md), or [oauth2-proxy](oauth2-proxy.md) on another gateway | Your chart |

## Steps

### 1. Declare the groups and the client

```yaml
groups:
  all:myconsole:operator: { members: [myconsole-admins@example.com] }
  all:myconsole:viewer:   { members: [everyone@example.com] }
clients:
  myconsole.example.internal:
    kind: confidential          # public for your own flow, with no secret
    secret: myconsole-client
    display_name: My Console    # shown on the sign-in page
    description: the team's dashboard
    redirects:  [https://myconsole.example.internal/oauth2/callback]
    signed_out: [https://myconsole.example.internal/]
    requires:   [all:myconsole:operator, all:myconsole:viewer]
    # backchannel_logout_uri: https://myconsole.example.internal/backchannel
```

The group name is the value in the token, so name it what the console checks. Nobody outside `requires` gets a token. `display_name` and `description` are shown to anyone who starts a sign-in. Only a console with its own flow can take `backchannel_logout_uri`. Group naming is in [trust](../../../concepts/sluis/trust.md#naming).

### 2. Verify tokens in the backend

```go
issuer := &identity.Issuer{URL: "https://issuer.example.internal", Audience: "myconsole.example.internal"}

mux := http.NewServeMux()
mux.Handle(identity.WhoAmIPath, identity.WhoAmI(version))
mux.Handle("/admin/", identity.Require("all:myconsole:operator")(admin))

http.ListenAndServe(":8080", identity.Middleware(issuer)(mux))
```

```ts
import express from "express";
import { Issuer, middleware, requireGroups, whoami, whoamiPath } from "@truvity/sluis/server";

const issuer = new Issuer({ url: "https://issuer.example.internal", audience: "myconsole.example.internal" });

const app = express();
app.use(middleware(issuer));
app.get(whoamiPath, whoami(version));
app.use("/admin", requireGroups("all:myconsole:operator"), admin);
```

`Middleware` establishes the caller and `Require` refuses, so health and landing routes run before anyone is established. The caller carries `name`, `givenName`, `familyName`, the address and the groups, so there is no userinfo call. Both verifiers follow the key the issuer advertises, so set no signing algorithm ([configuration](../../../reference/sluis/configuration.md)).

### 3. Use the frontend package

Use `useIdentity()` and `<UserBadge/>` from `@truvity/sluis` ([install](../../../sdk/typescript/sluis.md)). The console writes no login page, session, token parsing or sign-out logic.

### 4. Attach the route

If your platform publishes listeners as a `ListenerSet`, write `parentRefs` in full: `group`, `kind`, `name`, `namespace`. Point DNS for the hostname at the gateway.

## Traps

- Long work inside a request dies at the gateway route timeout. Trigger it and poll.
- Test authorization at the handler. An unwired role check still passes its unit test.
- Never mount an operator RPC on the service API port.
- No `authz` package exists. Pass `Require` the group names as the policy spells them ([Go module](../../../sdk/go/sluis.md)).

## Verify

Open the console signed out and expect a redirect to the issuer. Sign in as a viewer and expect `identity.WhoAmIPath` to answer with your identity.

## Decided in

[ADR 0001](../../../decisions/0001-sessions-and-an-absolute-limit.md).
