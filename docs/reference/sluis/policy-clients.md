# Policy: clients, resources and self-described clients

The `clients`, `resources` and `client_documents` tables of [the policy](policy.md). Decided in [ADR 0001](../../decisions/0001-sessions-and-an-absolute-limit.md), [0033](../../decisions/0033-a-longer-absolute-limit-for-read-only-resources.md), [0039](../../decisions/0039-the-issuer-generates-confidential-client-secrets.md), [0040](../../decisions/0040-agent-class-sessions.md).

## Clients

A client's id is the `aud`, unless the request named a [resource](#resources).

| Rule | Value |
|---|---|
| `requires` | mandatory; an empty list means nobody; sluis refuses to start on one |
| A caller in no `requires` group | refused before a token exists |
| Token exchange | target's `requires` decides; each proof is checked against its own issuer's keys |

| Kind | Used by | Secret |
|---|---|---|
| `public` | kubelogin, `sluisctl`, Kargo, `local-dev` | none |
| `confidential` | ArgoCD, Grafana, a console behind oauth2-proxy | named, or generated |
| `exchange` | AWS roles reached by token exchange | none |

| Key | Meaning |
|---|---|
| `kind` | `public`, `confidential` or `exchange` |
| `secret` | required for `confidential`: a name, or [`{generate: true}`](#a-generated-secret) |
| `redirects` | paths that start a sign-in |
| `signed_out` | pages allowed after RP-initiated logout; an address in both lists fails the load; an `exchange` client may declare none |
| `requires` | internal groups, any one admits |
| `ttl_cap` | upper bound on token lifetime |
| `session` | `interactive` (default) or `agent`; refused on `exchange`; see [agent-class sessions](#agent-class-sessions) |
| `sign_in_exchange` | `true` lets a sign-in be a token-exchange proof (`access_token`) while its session lives; `public` only |
| `display_name`, `description` | [sign-in page text](#what-the-sign-in-page-calls-a-client) |
| `backchannel_logout_uri` | receives a signed `logout+jwt` naming the `sid` when a sign-in ends; unusable behind [oauth2-proxy](../../guides/sluis/connect/oauth2-proxy.md) |
| `groups` | [groups override](#groups-override) |
| `groups_delimiter` | [rewrites `:`](#groups_delimiter) |
| `signing_alg` | [pins the algorithm](#signing_alg) |

### A generated secret

| `secret` shape | Meaning |
|---|---|
| a string | input `clients/<id>/secret`, delivered by the installation |
| `{generate: true}` | issuer makes 32 random bytes (base64url, no padding) |

| Fact | Value |
|---|---|
| Record | `credentials/oidc-client/<id>/secret` in the Secrets port: current, previous, previous-valid-until |
| Write | create-only; never overwritten; an existing input `clients/<id>/secret` is adopted |
| Read order | record first, input only while the store has no record |
| Cache | 30 s; a record read earlier is served up to 5 min when the store fails |
| Compare | current and previous on every request, constant time |
| Corrupt or unreadable record | authenticates nobody |
| Refused at start | `generate: false`; `public` or `exchange` client; Secrets adapter `legacy`; State not shared by replicas (`memory` excepted) |
| Older binary | refuses the object form; roll every replica first |
| Client no longer generated | record reported once as orphan; never deleted |
| Delivery | [`oidc/v1` document](secrets.md#the-external-documents) at `external/oidc/<client>` |
| Rotation | [`sluisctl clients`](sluisctl.md#clients-rotate-show-purge) |

How: [let the issuer generate a secret](../../guides/sluis/let-the-issuer-generate-a-clients-secret.md), [rotate a client secret](../../guides/sluis/operate/rotate-a-client-secret.md).

### What the sign-in page calls a client

| The row declares | The page says |
|---|---|
| `display_name` | that name |
| no name, id `k8s:<cluster>` | *Kubernetes, `<cluster>`* |
| no name | the client id |

| Fact | Value |
|---|---|
| Host shown | from the matched redirect URI; text, never a link |
| Loopback redirect | *a program on this computer*, no port |
| Visibility | both fields are public before sign-in |
| Refused | name over 80 characters; description over 200; blank; control, line-break, tab or bidirectional character |
| Output | HTML-escaped |
| Older build | refuses both keys as unknown; deploy the service first |

## Resources

```yaml
resources:
  https://mcp.example/:
    requires: [prod:k8s:admin]
    ttl_cap: 15m
    read_only: true       # lets absolute_cap exceed the service's lifetimes.absolute
    absolute_cap: 168h
```

A client names a resource with the `resource` parameter (RFC 8707); `aud` is then the resource.

| Rule | Value |
|---|---|
| Keys | `requires`, `ttl_cap`, `display_name`, `description`, `groups`, `groups_delimiter`, `signing_alg`, `read_only`, `absolute_cap` |
| Gates | caller satisfies the client's `requires` and the resource's |
| Caps | both apply; the shorter wins |
| Id | matched literally; absolute URI, no fragment; `requires` names declared groups |
| Recorded | with the session; re-checked on every refresh |

| A client asks | Result |
|---|---|
| nothing | token for the client |
| declared, entitled resource | `aud` is the resource, capped by both, same on every refresh |
| undeclared resource | `invalid_target` |
| resource not entitled | refused at sign-in, naming the resource and group |
| several resources | refused |
| relative URI or fragment | refused |

### Absolute session of a read-only resource

Deprecated: use [agent-class sessions](#agent-class-sessions).

| Field | Meaning |
|---|---|
| `absolute_cap` | this resource's absolute limit in place of `lifetimes.absolute`; at most `168h` |
| `read_only` | declarer's claim that the token changes nothing; unverified by the service |
| Access document | `readOnly`, `absoluteCap` |

| Rule | Value |
|---|---|
| Default limit | `lifetimes.absolute`, 24 h from `auth_time` |
| Refused at load | `absolute_cap` above 168h, zero or negative |
| Refused at start | `absolute_cap` above `lifetimes.absolute` without `read_only: true` |
| Cap below `lifetimes.absolute` | needs no `read_only` |
| Warning at start | names resources above `lifetimes.absolute`; a later minor release refuses them |
| Chain limit | shortest across resources used; client audience and cap-less resources count as `lifetimes.absolute` |
| Enforced at | refresh, silent `/authorize`, access token `exp` |
| Chain ends | earlier of `now + lifetimes.refresh` and `auth_time + limit` |
| `lifetimes.refresh` | default `12h`; raise to `168h` to survive a closed laptop |
| Ended by | sign-out, removal, suspension, refresh-token reuse |
| Migrate | set `session: agent` on those clients; lower or remove `absolute_cap` |

## Agent-class sessions

Declare `session: agent` for software that holds its own refresh token. Concepts: [sessions](../../concepts/sluis/sessions.md#agent-class-sessions).

```yaml
clients:
  mcp-host:
    kind: public
    session: agent
    redirects: [/callback]
    requires: [rung:engineering]

client_documents:
  origins: [agents.example]
  requires: [rung:engineering]
  session: agent                  # every document client gets the class
```

| Key under `lifetimes.agent` | Default | Ceiling | Meaning |
|---|---|---|---|
| `refresh` | `336h` | `absolute` | idle limit |
| `absolute` | `720h` | `2160h` | limit from `auth_time` |
| `access` | `30m` | `1h` | longest access or ID token |

All three are positive; shortening is allowed. Set them in [the service configuration](configuration.md).

| Rule | Value |
|---|---|
| Shortening tokens | `absolute_cap` only ceilings an agent chain; `ttl_cap` of client, `client_documents` and resource still apply |
| Class source | installation policy only, never a fetched document |
| `client_documents.session` | applies to every document client; declare a mixed set as `clients` rows |
| Refused at load | `session` on `exchange`; `session: agent` with `sign_in_exchange: true` |
| Warned at start | `session: agent` with `signed_out` or `backchannel_logout_uri` |
| Consent page | once per authorization; names client, origin, return host, class, deadline |
| `prompt=none` | `consent_required` |
| Own sign-out (`/logout`, `/end_session`) | ends interactive sessions, keeps agent sessions |
| `/end_session` naming an agent client | also ends that client's sessions |
| Ends agent sessions | *sign out everywhere*; per-client revoke; operator revoke; directory removal; refresh-token reuse |
| Keeps every live chain | a sign-in past its own limit; another person signing in in the same browser |
| Removing a client or origin | stops chains (unknown at refresh) but does not end them; revoke sessions first |

## Groups override

`groups` on a client, resource or `client_documents` block sets which held groups a token carries beyond the pairs in its audience's `requires`. Rule: [groups in a token](../../concepts/sluis/groups-in-a-token.md).

| Value | Carries |
|---|---|
| absent | the pairs of the audience's `requires`, any role |
| `all` | every group the caller holds |
| `[thing, ...]` | also every held group of each thing, any scope |
| `[rung]` or `[emp]` | also every held name of that family |
| `[rung:sre]` | also that one two-segment name |

```yaml
clients:
  console:
    kind: confidential
    secret: console-oidc
    requires: [devel:grafana:viewer]
    groups: [shop]
```

| Validation | Rule |
|---|---|
| No declared vocabulary | value is `all` or a list of names |
| Declared vocabulary | bare word must be a declared thing, `rung` or `emp` |
| Entry with a separator | exact two-segment name; valid either way |
| `groupsScoping` | service `config` key: `off`, `report` (default), `enforce` ([configuration](configuration.md)) |

## `groups_delimiter`

```yaml
clients:
  ssh-fleet:
    kind: public
    requires: [devel:ssh:user]
    signing_alg: RS256
    groups_delimiter: "."   # devel:ssh:user -> devel.ssh.user
```

| Rule | Value |
|---|---|
| Effect | rewrites every `:` in each name of the audience's `groups` claim, after scoping |
| Default | empty: claim unchanged |
| Applies to | ID token, access token, `/userinfo`, token exchange, console mint |
| Row read | as for [`signing_alg`](#signing_alg) |
| Unchanged | which groups a caller carries; `requires` and `groups:` use real names |
| Refused at load | empty; `:`; whitespace, `"` or `,`; a character in `[A-Za-z0-9-]` |
| Refused at load | a delimiter that makes two declared groups the same string; the message names both |
| Never use on | an audience whose tokens this installation reads back: the service's own two roles split on `:` |

Rationale: [why a groups delimiter exists](../../concepts/sluis/policy.md#why-a-groups-delimiter-exists).

## `signing_alg`

```yaml
clients:
  eks-cluster: { kind: exchange, requires: [prod:k8s:admin], signing_alg: RS256 }
resources:
  https://legacy.example/: { requires: [prod:k8s:admin], signing_alg: RS256 }
```

Accepts `RS256`, `ES256` or `ES384`; anything else is refused at load. A row without it gets the signing key's algorithm (ES384 in the chart).

| Token | Row read |
|---|---|
| ID token | the client's |
| access token | the named resource, else the client |
| token exchange (`sluisctl kube-token`, CI job) | the granted target, never the presenting client |
| Back-Channel Logout token | the receiving client |
| console mint (`MintFor`) | that call's target |

A pin with no matching key is refused at start. Extra keys: `signingKey.additional` ([configuration](configuration.md)). Rationale: [why an audience can pin a signing algorithm](../../concepts/sluis/policy.md#why-an-audience-can-pin-a-signing-algorithm).

## Clients that describe themselves

```yaml
client_documents:
  origins: [clients.example]       # hosts that may serve a document; empty (default) is off
  requires: [rung:engineering]     # gate for ANY such client; mandatory with origins
  ttl_cap: 5m                      # optional
  groups: [shop]                   # optional, as for a client row
```

A client presents an HTTPS URL as `client_id`. sluis fetches its OAuth Client ID Metadata Document and treats the client as `public`.

| Rule | Value |
|---|---|
| Precedence | a declared client always wins |
| Gate | one shared gate, `client_documents.requires` |
| Ignored in a document | `kind`, `ttl_cap`, `requires` |
| Cache | 10 min; one retry on timeout, reset or 5xx |
| Stale-while-error | last good copy up to 1 h past expiry, transport failures only; warning logged each time |
| Never served stale | refused answer: invalid document, redirect, 4xx, oversized body |
| Display name | bounded, cursor-moving characters stripped; host shown when absent |
| Audit | records the URL |
| Discovery | `client_id_metadata_document_supported` in discovery and `/.well-known/oauth-authorization-server` only while an origin is named |

| Refused | Limit |
|---|---|
| document `client_id` differs from its URL | |
| origin not allow-listed | decided before any dial |
| response over 64 KiB or slower than 10 s per attempt | |
| redirect elsewhere | |
| no `redirect_uris` | |
| `origins` entry with a scheme, path or `*` | origins are hosts |
| `requires` or `ttl_cap` without `origins`; `origins` without `requires` | |

Rationale: [why a self-described client is proportionate](../../concepts/sluis/policy.md#why-a-self-described-client-is-proportionate).
