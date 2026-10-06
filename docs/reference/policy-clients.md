# Policy: clients, resources and self-described clients

The `clients`, `resources` and `client_documents` tables. Part of [the policy](policy.md).

## Clients

A client's id is the `aud`, unless the request named a [resource](#resources). `requires` lists the internal groups any
one of which admits a caller; a caller in none is refused before a token exists. `requires` is mandatory: an empty list
means nobody, not everyone, and sluis refuses to start on one.

| Kind | Used by | Has a secret |
|---|---|---|
| `public` | kubelogin per cluster, `sluisctl`, Kargo's web UI and CLI, `local-dev` | no |
| `confidential` | ArgoCD, Grafana, a console behind oauth2-proxy | yes: the `secret` it names, or one the issuer generates ([ADR 0039](../decisions/0039-the-issuer-generates-confidential-client-secrets.md)) |
| `exchange` | AWS roles reached by token exchange | no |

| Key | Meaning |
|---|---|
| `kind` | `public`, `confidential` or `exchange` |
| `secret` | the secret a confidential client names; required for that kind unless the issuer generates it, see [ADR 0039](../decisions/0039-the-issuer-generates-confidential-client-secrets.md) |
| `redirects` | where a code is delivered: a path that starts a sign-in |
| `signed_out` | the pages a person may land on after an RP-initiated logout. An address in both lists fails the load; an `exchange` client may declare none |
| `requires` | the internal groups, any one of which admits a caller |
| `ttl_cap` | an upper bound on this client's token lifetime |
| `sign_in_exchange` | `true` lets a person's sign-in to this client be presented as a proof in a token exchange, as its `access_token`, while the session behind it is live. `public` clients only. No other token this service signs is a proof |
| `display_name`, `description` | what the sign-in page shows, see below |
| `backchannel_logout_uri` | opts the client into OIDC Back-Channel Logout: when a sign-in ends, a signed `logout+jwt` naming the session (`sid`) is POSTed here |
| `groups` | [the groups override](#groups-override) |
| `groups_delimiter` | [rewrites `:` in the audience's groups](#groups_delimiter) |
| `signing_alg` | [pins the signing algorithm](#signing_alg) |

A token **exchange** trades a proof for a token whose `aud` is any declared client, and the target's `requires` decides.
Each proof is checked against its own issuer's keys.

Clients are declared: one row each, in git. They are never created in a console and never registered by a workload.
The one exception is [`client_documents`](#clients-that-describe-themselves).
Back-Channel Logout suits a client running its own session; a console behind oauth2-proxy cannot take it, because
oauth2-proxy keeps each session under a key only the browser's cookie holds ([oauth2-proxy.md](../how-to/connect/oauth2-proxy.md)).

### What the sign-in page calls a client

| The row declares | The page says |
|---|---|
| `display_name` | that name |
| no name, and the id is `k8s:<cluster>` | *Kubernetes, `<cluster>`* |
| no name | the client id, as it is |

The host shown is taken from the redirect URI of the request being answered, already matched against `redirects`, as
text and never as a link. A loopback redirect (`localhost`, a loopback address) shows *a program on this computer*
instead, with no port. Both fields are public: anyone who starts a sign-in reads them before proving who they are.
Validation refuses a name over 80 characters, a description over 200, a blank value, and any control or formatting
character (a line break, a tab, a bidirectional override). Everything is HTML-escaped when written. A build older than
the release that introduced them refuses both keys as unknown: deploy the service before declaring them.

## Resources

```yaml
resources:
  https://mcp.example/:
    requires: [prod:k8s:admin]
    ttl_cap: 15m
    read_only: true       # lets absolute_cap exceed the service's lifetimes.absolute
    absolute_cap: 168h
```

A client names a resource with the `resource` parameter (RFC 8707) and `aud` is the resource. Keys: `requires`,
`ttl_cap`, `display_name`, `description`, `groups`, `groups_delimiter`, `signing_alg`, `read_only` and `absolute_cap`.

- **Both gates apply.** A caller must satisfy the client's `requires` and the resource's.
- **Both caps apply, and the shorter wins.**
- **The id is matched exactly.** A trailing slash or a different scheme is a different resource. It must be an absolute
  URI with no fragment, and its `requires` must name declared groups.

| A client asks | It gets |
|---|---|
| nothing | a token for the client itself |
| a declared resource it is entitled to | a token whose `aud` is the resource, capped by both, with the same `aud` on every refresh of that session |
| a resource this installation does not declare | `invalid_target` |
| a resource it is not entitled to | refused at sign-in, naming the resource and the group it would need |
| more than one resource | refused |
| a relative URI, or one with a fragment | refused |

The resource is recorded with the session and re-checked on every refresh, which is where a withdrawn grant bites.

### Absolute session of a read-only resource

The service's `lifetimes.absolute` ends every session 24 hours after `auth_time`
([ADR 0001](../decisions/0001-sessions-and-an-absolute-limit.md), [configuration.md](configuration.md)). A resource that
only reads may ask for longer, up to 168h ([ADR 0033](../decisions/0033-a-longer-absolute-limit-for-read-only-resources.md)).

| Field | Meaning |
|---|---|
| `absolute_cap` | this resource's absolute session limit, in place of `lifetimes.absolute` |
| `read_only` | the declarer's claim that a token for this resource cannot change anything. The service cannot verify it |

Refused at load: an `absolute_cap` above 168h, and zero or a negative one. Refused at start: an `absolute_cap` above
`lifetimes.absolute` without `read_only: true`. A cap below `lifetimes.absolute` needs no `read_only`. A refresh chain's
limit is the **shortest** among the resources it has been used for; the client's own audience and a resource without an
`absolute_cap` count as `lifetimes.absolute`. The limit is enforced at refresh, at a silent `/authorize` and in the
access token's `exp`. Sign-out, removal or suspension, and refresh-token reuse end an extended chain as they end any
other. The sliding window still applies: a chain ends at the earlier of `now + lifetimes.refresh` and
`auth_time + limit`, and `lifetimes.refresh` defaults to `12h`, so raise it (to `168h`) for the cap to be usable across a
closed laptop. In the access document the fields are `readOnly` and `absoluteCap`.

## Groups override

`groups` on a client row, a resource row or the `client_documents` block sets which held groups a token carries beyond
the `<scope>:<thing>` pairs of its audience's `requires`. The rule that uses it, with its reasoning, is
[explanation/groups-in-a-token.md](../explanation/groups-in-a-token.md).

| Value | Carries |
|---|---|
| absent | the pairs of the audience's `requires`, in any role |
| `all` | every group the caller holds |
| `[thing, ...]` | additionally every held group of each named thing, in any scope |
| `[rung]` or `[emp]` | additionally every held name of that family |
| `[rung:sre]` | additionally that one two-segment name |

```yaml
clients:
  console:
    kind: confidential
    secret: console-oidc
    requires: [devel:grafana:viewer]
    groups: [shop]
```

Without a declared vocabulary, validation checks only that the value is `all` or a list of names. With one, each
bare-word entry must be a declared thing, or `rung` or `emp` (there is no `vocabulary.families` table). An entry with a
separator is an exact two-segment name and validates either way. `groupsScoping` is a key of the service's `config`
([configuration.md](configuration.md)): `off`, `report` (the default since v1.32.0) or `enforce`.

## `groups_delimiter`

```yaml
clients:
  ssh-fleet:
    kind: public
    requires: [devel:ssh:user]
    signing_alg: RS256
    groups_delimiter: "."   # devel:ssh:user -> devel.ssh.user
```

Rewrites every `:` in each name of that audience's `groups` claim, after scoping has decided which groups survive. Empty
(the default) leaves the claim as it is. It applies to the ID token, the access token, `/userinfo`, a token exchange and
the console's own mint alike. Which row wins follows [`signing_alg`](#signing_alg): an ID token reads the client's
value; an access token reads the resource's when a request named one, else the client's. It changes how a name is
spelled, never which groups a caller carries: `requires` and the `groups:` override still use the real names.

Refused at load:

- empty, or the separator `:` itself;
- whitespace, a quote (`"`) or a comma;
- an ASCII letter, digit or `-` (exactly `[A-Za-z0-9-]`);
- a delimiter that would make two of the policy's own declared groups the same string: every ordinary and non-grant
  `groups` key and every wildcard expansion is rewritten and compared, and the refusal names both.

Never point it at an audience whose tokens this installation reads back: the service's own two roles are parsed by
splitting on `:`. Why it exists, and the ADRs: [explanation/policy.md](../explanation/policy.md#why-a-groups-delimiter-exists).

## `signing_alg`

```yaml
clients:
  eks-cluster: { kind: exchange, requires: [prod:k8s:admin], signing_alg: RS256 }
resources:
  https://legacy.example/: { requires: [prod:k8s:admin], signing_alg: RS256 }
```

Accepts exactly `RS256`, `ES256` or `ES384`; anything else is refused at load. A row without it gets the installation
default (the algorithm of the signing key; ES384 in the chart). **The audience decides, not the client asking:**

| Token | Row read |
|---|---|
| ID token | the client's |
| access token | the resource a caller named, else the client |
| token exchange (`sluisctl kube-token`, a CI job) | the target the exchange was granted, never the client presenting it |
| Back-Channel Logout token | the client receiving it |
| the console's own short-lived mint (`MintFor`) | that call's own target |

A pin naming an algorithm the installation has no key for is refused at start, not at the first token that would need
it. Keys for other algorithms are `signingKey.additional` ([configuration.md](configuration.md)). Why a pin exists:
[explanation/policy.md](../explanation/policy.md#why-an-audience-can-pin-a-signing-algorithm).

## Clients that describe themselves

```yaml
client_documents:
  origins: [clients.example]       # the hosts that may serve a document; empty (the default) is off
  requires: [rung:engineering]     # who may use ANY such client; mandatory with origins
  ttl_cap: 5m                      # optional, and worth setting
  groups: [shop]                   # optional, as for a client row
```

Such a client presents an **HTTPS URL** as its `client_id`; the URL serves a JSON document (an OAuth Client ID Metadata
Document). sluis fetches it, validates it and treats the client as `public`. A declared client always wins: the policy
is consulted first, and a document cannot displace one. A document's `kind`, `ttl_cap` and `requires` are not read.
Every such client shares one gate, `client_documents.requires`.

| Shape | Refused because |
|---|---|
| a document whose `client_id` is not the URL it was served from | the id a person sees, the id the audit records and the id the token is minted for would be a name its holder chose |
| an origin that is not allow-listed | decided before anything is dialled, so the list also stops sluis fetching arbitrary URLs |
| a response larger than 64 KiB, or slower than 10 seconds per attempt | the URL is caller-chosen |
| a redirect to anywhere else | the document is served at its own id |
| a document with no `redirect_uris` | there would be nowhere to deliver a code |
| an `origins` entry with a scheme, a path or a `*` | an origin is a host |
| `requires` or `ttl_cap` with no `origins`, or `origins` with no `requires` | a block somebody expected to apply |

A fetched document is honoured for ten minutes and then fetched again; a transient failure (a timeout, a reset, a 5xx)
is retried once. **Stale-while-error is bounded and transport-only:** if a refresh of an already validated document
fails that way, the last good copy is served for at most one hour past its expiry and a warning is logged each time. It
is never served when the origin answered and the answer is refused (a document that no longer validates, a redirect, a
4xx, an oversized body). The display name comes from the document, so it is bounded and stripped of anything that moves
the cursor; with no name, the host is shown. The audit trail records the URL, which is the identity.
`client_id_metadata_document_supported` appears in the discovery document only while an origin is named. Why this is
proportionate: [explanation/policy.md](../explanation/policy.md#why-a-self-described-client-is-proportionate).
