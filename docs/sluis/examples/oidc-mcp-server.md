# Example: an MCP resource server and clients that describe themselves

## Goal

Let editors and assistants you did not deploy connect to an MCP server, each with a token minted for that server alone.

## What you need

- A hostname that serves the MCP endpoint and its RFC 9728 protected-resource metadata, and a host that serves client
  documents (here `mcp-clients.example`).
- Permission to change the policy.

## The policy snippet

```yaml
client_documents:
  origins:  [mcp-clients.example]        # hosts that may serve a Client ID Metadata Document; empty = off
  requires: [all:observability:user]     # mandatory once origins is set
  ttl_cap:  10m
resources:
  https://observability-mcp.example/mcp:
    requires: [all:observability:user]
    ttl_cap:  5m
    display_name: Observability MCP server
```

A resource id is an absolute URI without fragment; scheme and host match without regard to case, the rest exactly
(declare it without a trailing slash, the way the server publishes it). For a read-only server add `read_only: true` and
`absolute_cap: 168h` (up to seven days).

## The exchange / command

The client presents an HTTPS URL as its `client_id` and sends the resource on the authorization and token requests:
`resource=https://observability-mcp.example/mcp`. The token's `aud` is that URI. Do not hand-roll the server's side: a Go
server uses `identity/resource`, which verifies the token (`aud` = its own resource URL), answers `401` with the challenge and
serves the RFC 9728 metadata at the path-inserted well-known URL; a stock server runs behind `resource-proxy`.

```go
res, err := resource.New(resource.Config{
    IssuerURL:   "https://access.example.com",
    ResourceURL: "https://observability-mcp.example/mcp",
})
mux.Handle(res.Path(), res.Metadata()) // /.well-known/oauth-protected-resource/mcp
mux.Handle("/", res.Protect(mcpHandler))
```

The client reads that metadata, then the issuer's `/.well-known/oauth-authorization-server`. When a tool calls a backend,
the server uses its own workload identity, never the caller's bearer.

## Verify

A person in the group signs in through a client whose document is on an allow-listed host and gets a token with the
server's URI as `aud`. A request naming an undeclared resource, or two, is refused with `invalid_target`; a document from
another host is refused before anything is fetched.

## Undo

Empty `client_documents.origins` and remove the resource row.

Recipe and limits: [Connect an MCP server](../../how-to/connect/mcp.md).

Snippet source: `docs/how-to/connect/mcp.md`; `policy/clientdoc.go` validates `client_documents`, and the snippet is accepted
by `sluisctl policy render`.
