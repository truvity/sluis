# Go module

The module `github.com/truvity/sluis` verifies a caller, exchanges tokens and evaluates policy. Packages: `identity`, `tokens`, `policy`, `config`, `backend` ([extending](../../guides/sluis/extend.md)). TypeScript: [`@truvity/sluis`](../typescript/sluis.md).

Both verifiers of [trust](../../concepts/sluis/trust.md) return one `Verified`: `Subject, Email, Name, GivenName, FamilyName, Groups, ServiceAccount`. See [service to service](../../guides/sluis/connect/service-to-service.md).

```go
import (
    "github.com/truvity/sluis/identity"
    "github.com/truvity/sluis/policy"
    "github.com/truvity/sluis/tokens"
)
```

## Issuer anchor

Use this anchor for a console behind a proxy that forwards a bearer, such as an [oauth2-proxy](../../guides/sluis/connect/oauth2-proxy.md).

```go
issuer := &identity.Issuer{
    URL:      "https://access.example",
    Audience: "console.example",   // this console's client id at the issuer
}

mux := http.NewServeMux()
mux.Handle(identity.WhoAmIPath, identity.WhoAmI(version))   // GET /.access/whoami
mux.Handle("/admin/", identity.Require("all:console:operator")(admin))

http.ListenAndServe(":8080", identity.Middleware(issuer)(mux))
```

Discovery is lazy. `Middleware` never refuses. `Require` refuses without naming a group that would work; with no group it admits any vouched caller. The `WhoAmI` body is in [contracts](../../reference/sluis/contracts.md#the-whoami-endpoint).

## Cluster anchor

Use this anchor for a workload calling a service in the same cluster.

```go
cluster := &identity.Cluster{
    Review:   kube.ReviewToken,          // yours, or client-go's
    Audience: "the-service",
    Name:     "prod",
    Groups:   []string{"prod:k8s:admin"},
}

http.ListenAndServe(":8080", identity.Middleware(cluster, issuer)(mux))
```

You supply `Review` and `Groups`; a TokenReview carries no policy. Verifiers run in order and the first answer wins. A verifier that cannot reach the issuer stops the chain.

## MCP server

`Protect` answers the RFC 9728 challenge. See [MCP](../../guides/sluis/connect/mcp.md#a-go-server-identityresource).

```go
res, _ := resource.New(resource.Config{IssuerURL: "https://access.example", ResourceURL: "https://mcp.example.com/metrics", Scope: "openid"})
mux.Handle(res.Path(), res.Metadata())
mux.Handle("/", res.Protect(mcp))
```

## Token exchange

```go
exchanger := &tokens.Exchanger{Issuer: "https://access.example", ClientID: "local-dev"}
token, err := exchanger.Exchange(ctx, subject, tokens.TypeJWT, "aws:111122223333:power")
```

Use `tokens.TypeJWT` for an outside proof, such as a GitHub job token. Use `tokens.TypeAccessToken` for a sign-in of this issuer: only the access token of a live session at a `public` client with `sign_in_exchange: true`. Send the client in HTTP Basic. A refusal is `tokens.ErrRefused`.

For a GitHub App installation token, name the App ([contract](../../reference/sluis/contracts.md#installation-tokens-at-token)):

```go
exchanger := &tokens.Exchanger{Issuer: "https://access.example", ClientID: "github-app:publisher"}
minted, err := exchanger.GitHubInstallationToken(ctx, subject, tokens.TypeJWT, "publisher",
	[]string{"app"}, map[string]string{"contents": "read"})
// minted.AccessToken, minted.Expires, minted.Repositories, minted.Permissions
```

```go
tokens.WriteExecCredential(os.Stdout, apiVersion, token)   // kubectl reads this
creds, _ := tokens.AssumeRoleWithWebIdentity(ctx, nil, roleARN, who, token.AccessToken)
tokens.WriteCredentialProcess(os.Stdout, creds)            // the AWS SDKs read this
```

## Policy

```go
declared, _ := policy.LoadDeclared("/etc/sluis/policy")  // a file or a directory
set, _ := policy.NewSet(declared)                         // validated once, at load

result := set.Evaluate(policy.Input{
    Email:           "alice@example.com",
    DirectoryGroups: groups,      // what the directory confirmed
    Authoritative:   true,        // whether that answer may be acted on
})
```

`Input` also takes `GitHub` and `ServiceAccount` proofs. `LoadDeclared` refuses `team_id`, `domains` and `owner` under a Slack workspace, and `owner` under a GitHub organisation.

| accessor | returns |
|---|---|
| `Set.SlackWorkspaceDeclared(key)` | whether the policy declares that Slack workspace key |
| `Set.SlackWorkspaceKeys()` | every declared Slack workspace key, sorted |
| `Set.Declared()` | the declared `Policy` in force, read-only, to validate a definition of your own against |
| `Policy.PeopleByAddress()` | every address in the `people` table, normalised, mapped to its person's key |

## Configuration

```go
import "github.com/truvity/sluis/config"

in, err := config.LoadInstallation("installation.yaml")
service, policy, err := config.Render(in)   // sluis.yaml and policy.yaml, as bytes
```

`config` holds the service (v3) and policy (v2) document types and the [installation](../../reference/sluis/installation-document.md). `Render` is deterministic and backs `sluisctl render`.

Decided in: [0038](../../decisions/0038-estates-render-through-sluis.md).
