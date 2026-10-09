# Front a stock MCP server with resource-proxy

Verify tokens, publish the protected resource metadata and write an audit line for an MCP server you cannot change. To write the server yourself, see [Connect an MCP server](mcp.md#a-go-server-identityresource). Declare the resource and its clients there first.

Run the sidecar `ghcr.io/truvity/sluis/resource-proxy:<version>`, pinned by digest, beside the server.

```
client ──▶ gateway ──▶ :8080 resource-proxy ──▶ 127.0.0.1:8081 stock MCP server
                        verifies the token                     │
                        serves the PRM                         ▼
                        writes the audit line       127.0.0.1:8429 resource-proxy (outbound)
                                                    adds the workload's OWN token ──▶ backend
```

Every request except metadata and health needs a token for `RESOURCE_URL`. The proxy reads no group and strips the caller's `Authorization` header.

Each flag has an upper-case environment variable of the same name.

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `:8080` | inbound listener |
| `--upstream` | required | the stock server, such as `http://127.0.0.1:8081/mcp` |
| `--issuer-url` | required | the issuer, as in a token's `iss` |
| `--resource-url` | required | the token audience, byte for byte as in `resources` |
| `--scope` | `openid` | advertised, never checked; empty omits it |
| `--max-request-bytes` | `4194304` | larger bodies get `413` |
| `--body-read-timeout` | `30s` | time to read one body |
| `--upstream-timeout` | `2m` | wait for response headers; streams are unbounded |
| `--outbound-listen` | off | loopback address of the outbound forwarder |
| `--outbound-target` | | where the forwarder sends requests |
| `--outbound-token-endpoint` | | the issuer's token endpoint, ending in `/token` |
| `--outbound-client-id` | | the exchange client this workload presents |
| `--outbound-audience` | | the audience to exchange for |
| `--outbound-sa-token-file` | | projected ServiceAccount token (audience: the issuer), re-read for every exchange |
| `--outbound-ca-file` | system roots | PEM CAs trusted only for `OUTBOUND_TARGET`; read once at start |
| `--outbound-allow-non-loopback` | `false` | allow a non-loopback `--outbound-listen` |
| `--refresh-before` | `1m` | exchange again this long before expiry, at most half a token's life |

Any `OUTBOUND_*` value without `OUTBOUND_LISTEN`, or `OUTBOUND_LISTEN` without the five it needs, is refused at start.

## Route the gateway

The gateway rewrites nothing. For `RESOURCE_URL=https://mcp.example.com/metrics`, route these paths to the pod:

| Request path | Handled as |
|---|---|
| `/metrics` and `/metrics/...` | authenticated and proxied. The prefix becomes the path of `UPSTREAM`, so `/metrics/x` reaches `/mcp/x`. The query string is kept |
| `/.well-known/oauth-protected-resource/metrics` | the PRM, unauthenticated |
| `/.well-known/oauth-protected-resource` | the same PRM. Route it to one pod only |
| `/healthz`, `/readyz` | kubelet probes. Do not route them |

A host with several resources needs one well-known route per resource.

## Call a backend as the server

Point the stock server's backend URL at `OUTBOUND_LISTEN`. The proxy adds this workload's own bearer and forwards to `OUTBOUND_TARGET`. It never forwards the caller's token, and acting as the user is not provided.

The proxy exchanges the ServiceAccount token at `OUTBOUND_TOKEN_ENDPOINT` ([service to service](service-to-service.md)). If a refresh fails and no valid token remains, the call is `502`. `/readyz` waits for the first token.

A non-loopback listener is refused at start unless `OUTBOUND_ALLOW_NON_LOOPBACK=true`.

## Read the audit line

```json
{"time":"…","level":"INFO","msg":"mcp_request","http_method":"POST","path":"/metrics",
 "status":200,"duration_ms":41.3,"sub":"ada@example.com","email":"ada@example.com",
 "name":"Ada L","client":"https://client.example/cimd.json",
 "method":"tools/call","tool":"query"}
```

One line per request goes to standard output, refused requests included. A batch logs `"method":"batch"` with a `calls` array. The line never holds a token, header, query string, tool argument or response body.
