# Connect an MCP server

Declare a Model Context Protocol server as a resource, admit its clients, and verify the tokens it receives.

## Before you start

- A resource id is matched byte for byte, trailing slash included.
- A caller must pass the client's `requires` and the resource's `requires`. The shorter `ttl_cap` wins.
- `client_documents` admits clients you did not deploy, so its `requires` is mandatory.

## 1. Declare the resource

```yaml
resources:
  https://observability-mcp.example/:
    requires: [all:observability:user]
    ttl_cap: 5m
    display_name: Observability MCP server
```

The client sends the id as the RFC 8707 `resource` parameter and `aud` becomes that URI. See [Resources](../../../reference/sluis/policy-clients.md#resources).

For a long-running client, set `session: agent` on the client or on `client_documents`, not `absolute_cap`.

## 2. Admit clients that describe themselves

```yaml
client_documents:
  origins: [mcp-clients.example]        # hosts that may serve a document; empty is off
  requires: [all:observability:user]    # who may use any such client
  ttl_cap: 10m
```

A client presents an HTTPS URL as its `client_id`. See [Clients that describe themselves](../../../reference/sluis/policy-clients.md#clients-that-describe-themselves).

Sign-ins fail when a client's host stays down more than an hour past cache expiry.

## 3. Verify tokens in the server

Check the signature, the issuer and `aud` equal to the server's own resource URI, as in [service to service](service-to-service.md). Let the caller's `groups` bound what a tool may do.

## A Go server: `identity/resource`

```go
res, err := resource.New(resource.Config{
    IssuerURL:   "https://access.example",
    ResourceURL: "https://mcp.example.com/metrics",
    Scope:       "openid",
})
mux.Handle(res.Path(), res.Metadata())      // RFC 9728, unauthenticated
mux.Handle("/", res.Protect(mcpHandler))    // 401 + challenge, else the caller is in the context
who, _ := identity.FromContext(r.Context()) // inside mcpHandler
```

`Protect` reads only the `Authorization` bearer. No token gets `401` with `WWW-Authenticate: Bearer resource_metadata="<PRM URL>"`; a bad token adds `error="invalid_token"`. An unreachable issuer gets `503`.

`res.Ready(ctx)` is the readiness probe. Never authorize on `identity.Verified.ClientID`. See the [Go module](../../../sdk/go/sluis.md) and the [TypeScript package](../../../sdk/typescript/sluis.md).

## Fronting a stock MCP server with `resource-proxy`

See [Front a stock MCP server](mcp-resource-proxy.md).

## Verify

```sh
curl -si https://mcp.example.com/metrics | head -3
curl -s https://mcp.example.com/.well-known/oauth-protected-resource/metrics
```

The first answers `401` with `resource_metadata`. The second lists your issuer under `authorization_servers`.

## What the issuer does for MCP clients

- With no `scope`, `/authorize` supplies `scope=openid`.
- RFC 9207 `iss` is not emitted, so a client that requires it refuses the response.
- There is no `insufficient_scope` and no step-up. Groups decide reach.
- Dynamic client registration (RFC 7591) is not offered.
- With a path in the issuer URL, RFC 8414 metadata is served only at `/.well-known/oauth-authorization-server/<path>`. The gateway strips the path prefix before forwarding other endpoints.
- Over stdio there is no bearer to verify.
