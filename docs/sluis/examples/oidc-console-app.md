# Example: a console that runs its own sign-in

## Goal

A web console you write signs people in against the issuer and checks two roles itself, with no login page, session or token
parsing of its own.

## What you need

- A hostname for the console, and permission to change the policy.
- The Go or TypeScript identity verifier (`@truvity/sluis/server` for Node).

## The policy snippet

```yaml
groups:
  all:myconsole:operator: { members: [myconsole-admins@example.com] }
  all:myconsole:viewer:   { members: [everyone@example.com] }
clients:
  myconsole.example.internal:
    kind: public              # own flow in the browser: PKCE, no secret
    display_name: My Console
    description: the team's dashboard
    redirects:  [https://myconsole.example.internal/callback]
    signed_out: [https://myconsole.example.internal/signed-out]
    requires:   [all:myconsole:operator, all:myconsole:viewer]
```

Never register the sign-out landing page as a redirect too: landing on a redirect starts the sign-in again.

## The exchange / command

The backend wraps everything in the verifier; `Require` refuses.

```go
issuer := &identity.Issuer{URL: "https://access.example.com", Audience: "myconsole.example.internal"}
mux := http.NewServeMux()
mux.Handle(identity.WhoAmIPath, identity.WhoAmI(version))
mux.Handle("/admin/", identity.Require("all:myconsole:operator")(admin))
http.ListenAndServe(":8080", identity.Middleware(issuer)(mux))
```

The frontend sends the browser to `/authorize` with PKCE and reads the result; `useIdentity()` and `<UserBadge/>` come from
`@truvity/sluis`.

## Verify

A viewer reaches the console and is refused `/admin/`; a person outside both groups gets no token at all, because `requires`
is the primary gate. `GET /whoami` shows the groups.

## Undo

Remove the client row; sign-in then fails at the issuer and admits nobody.

Recipe and traps: [Connect a console](../../how-to/connect/console-app.md). If the console has no authorization model of its
own, use [Envoy Gateway OIDC](oidc-envoy-gateway.md) or [oauth2-proxy](oidc-oauth2-proxy.md).

Snippet source: `docs/how-to/connect/console-app.md`; the client row is accepted by `sluisctl policy render`.
