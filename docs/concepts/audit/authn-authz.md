# How does audit authenticate and authorize callers?

An Authenticator turns a request into a Principal. An Authorizer turns a Principal into a Grant.

```go
type Authenticator interface {
    Principal(ctx, req) (Principal, error)     // issuer, subject, claims
}

type Authorizer interface {
    Grant(ctx, Principal) (Grant, error)
}

type Grant struct {
    AllTenants bool      // an operator's grant, said out loud
    Tenants    []string
    Profiles   []string
    Operations []Operation // search, facets, get, export, tail, resolve
    From, Until time.Time
    Rule       string    // stamped into the log_access record
}
```

The zero value grants nothing. An operator's all-tenant grant sets `AllTenants` explicitly.

The grant reaches a query as one more filter term, not as a check beside it. `resolve` is not implied by `get`: it undoes pseudonymisation, and a grant to read does not carry it.

## Authenticators

| authenticator | status | use |
|---|---|---|
| `jwt` | built | a list of trusted issuers, each with a required audience |
| workload tokens | built | a workload in the application's namespace presents its projected service-account token |
| `none` | built | tests only |

### jwt

The token's `iss` is read unverified only to choose which keys check it. That issuer's verifier then requires the same issuer. Verification is gateway-auth's, as for every fleet service.

Two issuers can assert the same claim, for example `all:audit:auditor` in `groups`. A rule therefore names the claim it reads and the issuer it trusts it from. With several issuers every rule must name one.

### Workload tokens

The receiver verifies the token as a JWT from the cluster's own OIDC issuer. The subject is the service account, which the kubelet vouches for. The receiver stamps it on each record as the observer. It also authenticates `RegisterCatalogue`.

### On the SQS path

A record arriving through the ingest queue carries no verified caller identity. SQS delivers the body and attributes the sender chose. The writer attributes the record to itself.

Whoever may send to the queue can therefore record under any `source` and `actor` a catalogue admits. Authenticity on this path rests on the queue policy's sender principals alone.

- `Ingest.Senders` is required. The queue policy allows `sqs:SendMessage` to those principals and denies every other (`aws:PrincipalArn`).
- `Ingest.AnySenderInAccount` allows any principal of the account with `sqs:SendMessage`, for a trial. The trail then cannot say who sent a record. The two settings are refused together.
- Name one principal per sending workload (the receiver, the query service, the notary), each with `sqs:SendMessage` on the queue only. Name a principal in another account the same way.

The observer is not taken from a message attribute, which is the sender's own claim. SQS stamps a `SenderId` the sender cannot choose, but the library cannot map a sender's role id to an observer name. To tell senders apart, give each sender its own queue.

The HTTP and stream paths differ: the receiver verifies the caller's token and stamps the observer.

## Authorizers

| authorizer | status | reads |
|---|---|---|
| `declarative` (default) | built | configuration mapping claim values to grants, plus grant presets |
| `access-roster` grants preset | built | `<scope>:audit:<role>` from the groups claim |
| policy-engine adapter | optional | an engine whose query plan returns a filter and whose decision log names the rule |

An authorizer answers with every grant the caller holds. `auth.Effective` picks, per request, the grants covering the profile and operation asked. Tenants union within a profile and never across profiles. A time window never unions.

The `access-roster` grants preset (legacy identifier, renamed in v1.75–v1.76) reads the estate's grant grammar, which sluis defines. `authn.SplitGroup` splits it. Roles bind to the framework profiles a profile is composed from, not to profile names. `viewer` must be tenant-scoped. `resolve` and time-boxed grants are explicit rules only. The reference lists the role table.

## The Audit page

The application's own console hosts the page and passes the token it already holds. See [the Audit page](audit-page.md).

## Resolution

`resolve` maps a pseudonym back to a person through the identity map. Grant it to as few roles as possible. It emits `audit.get`-class records naming the rule.

With `keys.provider: none`, or no `keys` block, there is nothing to resolve. The operation is refused as unimplemented. A grant may still name it.

## Decided in

- [0049 Authentication and authorization plug points](../../decisions/0049-authentication-and-authorization-plug-points.md).
- [0053 One installation per service or product](../../decisions/0053-one-installation-per-service-or-product.md).
- [0055 No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md).
