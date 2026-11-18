# 0020 — Grant names come from a declared vocabulary, and a token carries fixed claims for one audience

**Status:** Accepted; amended by 0045
**Date:** 2026-11-12

Proposal: [issue #999](https://github.com/truvity/sluis/issues/999).

## Context

[0017](/decisions/0017-identity-and-sign-in/#subjects) says a token carries only the grant names of its own boundary, that grant names are declared per audience, and that lifetimes are set per issuer. It leaves open how the names are declared, which claims a token has, what a token carries for each audience, what happens to a name nobody declared, and what the lifetimes are.

Two things are called a grant in the design, and they are not the same. A **grant name** is an entry of the vocabulary: a named permission that a policy may allow and a token may carry. A **sealed grant** is one decision of `policy`, sealed by key@ and carried in flight for about a minute ([0007](/decisions/0007-policy-and-sealed-grants/#sealed-grants)). This record is about grant names. It uses "grant" alone only for the sealed grant.

v1 answered these questions in a policy document (v1 ADR 0010, on `master`): grants named `<scope>:<thing>:<role>`, an optional declared vocabulary with role ladders and sensitive scopes, a `groups` claim narrowed to the audience's needs, and token lifetimes with a default and a cap per client. Relying parties already depend on the shape: AWS roles, the Kubernetes API server and Argo CD read the `groups` claim, and some of them split a group name on `:`. The parity milestone keeps those relying parties working ([#787](https://github.com/truvity/sluis/issues/787), [#788](https://github.com/truvity/sluis/issues/788)); the first working version needs the rules they stand on.

What a token may carry is config, and a run-time write cannot widen it: a tenant's client is an instance under a template and adds no grant name and no claim ([0004](/decisions/0004-the-data-model/#profiles), [principle 17](/principles/#17-where-a-thing-is-written-decides-its-class)). A claim of a token is never taken from anything an operator or a vendor sends at run time.

## Decision

### Grant names and the vocabulary

- **A grant name is `<scope>:<thing>:<role>`,** read as the role on the thing in the scope. Each segment is a non-empty string of ASCII letters, digits, `-` and `_`. A scope is an environment, a company or `all`; a thing is a system, project or application; a role is a rung of that thing's ladder.
- **Every grant name is declared in the vocabulary,** the config kind `GrantVocabulary`, a signed document that the publishing pipeline writes and that `policy`, `exchange`, `session`, `signer` and the console read ([0005](/decisions/0005-the-store/#configuration-loading)). It lists the scopes, the things with their roles, and for each role what it implies. The shape is on the [Policy](/reference/policy/) reference page. Unlike v1, where a vocabulary is optional, a v2 installation always has one.
- **A role ladder is explicit and does not inherit across scopes.** `admin: [operator]` means that holding `<scope>:<thing>:admin` holds `<scope>:<thing>:operator` too, transitively and on the same scope and thing. `all:x:admin` never implies `dev:x:admin`. A role may not cover more scopes than a role it implies; a role may be narrowed to some scopes.
- **A scope can be `sensitive`.** A wildcard never reaches a sensitive scope: a sensitive scope is named in full or not granted.
- **A wildcard `*` is allowed only in the scope or the thing position of a declared group binding.** It is refused in `requires`, in a token's claims, in lifetimes and in the role position, and a wildcard that expands to nothing fails the load.
- **An undeclared name is refused when the snapshot loads.** The store validates the whole set of config documents together, so a policy, a client `requires` or a binding that names an undeclared grant name fails the swap, with an error that names the path and never the value ([0005](/decisions/0005-the-store/#configuration-loading), [0004](/decisions/0004-the-data-model/#schemas-are-protobuf)). A tenant client's `requires` is checked against the vocabulary when `exchange` writes it. The grant names of a person are therefore always names of the vocabulary.

### The claims of a token

A token carries a fixed set of claims. A claim outside the set is not issued, and a declared key for one is refused at load.

| Claim | ID token | Access token | Value |
|---|---|---|---|
| `iss` | yes | yes | The issuer URL |
| `sub` | yes | yes | The person's subject ([0019](/decisions/0019-person-and-upstream-link/)); for a workload, its SPIFFE ID |
| `aud` | yes | yes | Exactly one registered client id ([0017](/decisions/0017-identity-and-sign-in/#audiences)) |
| `exp`, `iat` | yes | yes | Time of expiry and of issue |
| `jti` | no | yes | A random id |
| `client_id` | no | yes | The client that asked |
| `nonce` | when sent | no | The request's nonce |
| `auth_time` | when requested, and always in an access token of a session | yes | When the person last signed in at the upstream; unchanged by a refresh |
| `acr`, `amr` | yes | yes | As the upstream sent them; absent when it sent none |
| `sid` | yes | when a session backs the token | The id of the session chain the console lists |
| `groups` | yes | yes | The person's grant names, narrowed to the audience (below) |
| `email`, `email_verified` | scope `email` | no | The upstream's claims |
| `name`, `preferred_username` | scope `profile` | no | The upstream's claims, and the directory's account name where there is a directory |
| `cnf` | no | when bound | `jkt` of the client's DPoP key, only for an audience that declares binding |

- **The header of an access token carries `typ` `at+jwt`, a `kid` and the algorithm of the audience** ([REQ-TOKEN-001](/requirements/token/#REQ-TOKEN-001), [REQ-TOKEN-017](/requirements/token/#REQ-TOKEN-017)). `signer` signs it through key@ and needs no grant for a token ([0006](/decisions/0006-keys-and-sealing/#sign-and-mac)).
- **Personal data is released only where the granted scopes ask for it,** and never in an access token for another resource ([REQ-TOKEN-006](/requirements/token/#REQ-TOKEN-006), [REQ-TOKEN-022](/requirements/token/#REQ-TOKEN-022)).
- **An absent `acr`, `amr` or `auth_time` stays absent.** sluis never fills in a value the upstream did not send, so a relying party reads absence as unknown.
- **A client of a regulated customer's tenant carries no organisation or tenant claim beyond what `iss` and `aud` already name.** `iss` is the tenant's own issuer host, which reveals no organisation beyond what the tenant chose as its host, and an `aud` of `<tenant>:<name>` names the tenant ([0032](/decisions/0032-customer-identity-installations/#client-names)). The fixed set has no further such claim, and a grant name whose scope names a tenant is never issued to a regulated audience, whatever the person holds. A regulated audience is a client of a tenant declared regulated ([0008](/decisions/0008-boundaries-and-tenants/#tenants)), and the organisation is the product's data ([0032](/decisions/0032-customer-identity-installations/#tenants)).
- **`groups` is flat,** a JSON array of strings, one grant name each, with the ladder expanded. A token carries the grant names of its own boundary only.
- **`groups` is narrowed to the audience.** A token carries each held grant name whose `<scope>:<thing>` pair appears among the pairs of the audience's `requires`, in any role, so a relying party never reads a grant name meant for another system. Narrowing never changes what `requires` admits: admission is checked against the full set. The audience is the client of the request, or for a token exchange the target the exchange was granted, never the client that presents the token.
- **A client may name more things in its `groups` override.** `groups: [thing, ...]` in the declaration of a client adds to its token every held grant name of each thing named, in every scope and role, beside the pairs of its `requires`. An entry is a thing of the vocabulary: the load refuses a grant name, a wildcard, a repeat and a thing nobody declared. The override only adds: `requires` is still what admits, and it is checked against the full set. A thing is matched whole, never by a prefix, and a held name of another shape than a grant name is never carried, whatever the override names ([REQ-TOKEN-046](/requirements/token/#REQ-TOKEN-046), [REQ-CLIENT-049](/requirements/client/#REQ-CLIENT-049)).
- **A client may have a `groups_delimiter`,** one to eight printable ASCII characters that replace each `:` of a name in `groups`, such as `.` for a verifier that splits a group on `:` and reads the last part. It applies to the claim after narrowing, so it changes how a kept name is written and never which names are kept, in the ID token, the access token and the access token of an exchange. The load refuses a delimiter made of a letter, a digit, `-`, `_`, `:`, a double quote or a comma, and so no two names can become one after the rewrite: the loader has no table of declared names to search for a collision, which v1 needed because its names had no fixed alphabet. `signer` reads the delimiter from the client of the audience and signs a group only when it is a grant name written with it ([REQ-TOKEN-047](/requirements/token/#REQ-TOKEN-047), [REQ-TOKEN-048](/requirements/token/#REQ-TOKEN-048), [REQ-CLIENT-048](/requirements/client/#REQ-CLIENT-048)).
- **Claims beyond this set, such as claim fragments merged from groups,** are the parity milestone's ([#786](https://github.com/truvity/sluis/issues/786)). When they arrive they are declared and added to this set, and nothing outside the set can be issued before.

### The `sub` of a token

- **A `sub` has one of two forms and no other** ([REQ-SUBJ-071](/requirements/subj/#REQ-SUBJ-071)). A person's own subject is 128 random bits in 26 lower-case base32 characters ([0019](/decisions/0019-person-and-upstream-link/#assigning-a-subject)). A workload's `sub` is its SPIFFE ID ([0022](/decisions/0022-workload-identity-and-host-enrolment/#spiffe-identities)). `kits/go/subject` classifies a string by these forms, and `kits/conformance/token/sub-vectors.json` fixes them, with the strings that have none: an e-mail address, a name or number of the upstream, a pairwise subject, a person's subject whose spare bits are set, a malformed ID and every v1 spelling.
- **A person has one `sub`, the same for every client and made of nothing about the client or the tenant, and no token carries a pairwise one** (maintainer decision SB1 A, 2026-11-18; [REQ-SUBJ-072](/requirements/subj/#REQ-SUBJ-072), [REQ-SUBJ-073](/requirements/subj/#REQ-SUBJ-073)). `session` gives the person's own subject at sign-in and refresh, and `exchange` gives the same one for an exchanged token; neither asks `key@` for it, and the `pairwise` data key of a tenant is retired ([0006](/decisions/0006-keys-and-sealing/#data-keys)). The reason: staff who work across vendors must be one person across the vendors' apps, audit and access review, and vendors are kept apart by grants and by separate installations, not by obfuscating the subject. The removal came before any relying party moved, because removing the form later would change every stored `sub`. The `public` subject type of OpenID Connect is what discovery lists.
- **A relying party that read v1's e-mail `sub` reads the `email` claim of the ID token for a readable name** (maintainer decision SB2 A, 2026-11-18). A store keyed on the old e-mail `sub` is re-keyed through directory sync ([#805](https://github.com/truvity/sluis/issues/805)), and no client declares a `sub_format: email`. When the switch inventory finds a consumer that cannot be re-keyed, the exception is an extra claim declared for that one client, never another spelling of `sub`.
- **A workload's ID is made from the claims of its verified proof, and names the repository or the account, never the run.** A CI job is `spiffe://<trust-domain>/ci/github/<org>/<repo>` from the `repository` claim of a GitHub Actions token, with the case GitHub wrote; the ref, the event and the environment are facts for policy ([REQ-WORKLOAD-007](/requirements/workload/#REQ-WORKLOAD-007)), not part of who the subject is. A ServiceAccount is `spiffe://<trust-domain>/k8s/<cluster>/ns/<namespace>/sa/<name>`, and the cluster is always named, so the same namespace and name on two clusters are two subjects. A part or a trust domain outside the limits of the SPIFFE ID specification is refused, never repaired ([REQ-WORKLOAD-008](/requirements/workload/#REQ-WORKLOAD-008)). The workload exchange that issues them is [#961](https://github.com/truvity/sluis/issues/961); until it exists the spelling is the kit and its vectors.
- **v2 does not reproduce a v1 spelling.** v1 wrote the e-mail address for a person, `github:<org>/<repo>` for a CI job and `<cluster>:k8s:<namespace>:<name>` for a ServiceAccount. The cutover table in [0015](/decisions/0015-evolution-and-compatibility/#cutover-from-v1) says what each relying party does about each.

### Binding

- **Binding is declared per audience, and the default is a bearer token.** An audience that declares DPoP or mutual TLS gets a bound token with `cnf`; an audience that does not gets a bearer token without `cnf`. The parity relying parties (AWS web identity, the Kubernetes API server, Argo CD) accept bearer JWTs only. The first working version issues bearer tokens and always binds authorization codes with PKCE; DPoP and mutual TLS follow in [#912](https://github.com/truvity/sluis/issues/912).

### Lifetimes

| Token | Default | Maximum |
|---|---|---|
| Access and ID token of a person | 1 hour | 1 hour |
| Access token of an agent session | 30 minutes ([0018](/decisions/0018-sessions/#agent-sessions)) | 1 hour |
| Access token of a workload exchange | 1 hour | 1 hour |

- **A token's `exp` is the earliest of its default, the issuer's lifetime, the client's `ttl_cap`, the end of the session chain that backs it and the end of the proof that was exchanged.** An exchange never yields a token that outlives the token it was made from ([REQ-EXCH-004](/requirements/exch/#REQ-EXCH-004)).
- **The lifetimes are a layered kind.** The maximum is built in, the default of each issuer is the config kind `TokenLifetime`, and a tenant may shorten it for its own tenant with the durable kind `TokenLifetimeTenant`, which `exchange` owns. The combine rule is shortest wins, and the store refuses a tenant value longer than config ([0004](/decisions/0004-the-data-model/#layered-and-linked-kinds)). A client lowers its tokens further with `ttl_cap`. The configuration check refuses a value above the maximum.
- **The rule is written once and held to shared vectors.** Session (sign-in and refresh) and exchange take `exp` from `kits/go/lifetime`, and signer holds `exp` minus `iat` to the same cap; `kits/conformance/token/lifetime-vectors.json` fixes it ([REQ-TOKEN-049](/requirements/token/#REQ-TOKEN-049)). It is v1's: the lifetime of the deployment narrowed by the client's `ttl_cap`, and the end of what the token came from. The ID token and the access token of one audience each follow it with their own issuer lifetime, and the `ttl_cap` of the client shortens both, so a relying party that declared a cap of five minutes never reads an ID token of an hour. A limit that is not set, or is zero, is not a limit, and the exp is in whole seconds from `iat`: a chain or a proof with less than a second left issues nothing.
- **The configuration check holds the lifetimes together** ([REQ-TOKEN-050](/requirements/token/#REQ-TOKEN-050)): a token lifetime above 1 hour, an absolute session limit above 24 hours, an idle limit above the absolute limit and a token lifetime above the absolute limit (v1: the absolute limit is at least the token lifetime) are refused with the path of the field, so the file says what runs, and session no longer has to cut a value at run time.
- **A resource's cap joins the client's when resources arrive.** A request that names a resource ([#791](https://github.com/truvity/sluis/issues/791)) is held to the shorter of the client's `ttl_cap` and the resource's, as in v1, because each says "not longer than this" and the longer would answer neither ([REQ-TOKEN-051](/requirements/token/#REQ-TOKEN-051), planned). The vectors already hold the rule; no client declares a resource yet, so there is no code for it ([principle 8](/principles/#8-nothing-without-a-consumer)).
- **The defaults and the maximum are v1's** (`lifetimes.token` is 1 hour, an agent's access token 30 minutes with a ceiling of 1 hour), so a relying party that tuned its refresh to them keeps working. An issuer, a tenant and a client can only shorten; a longer token is a new record. Refresh tokens and sessions have their own limits ([0018](/decisions/0018-sessions/)).

### Amendment of 2026-11-12: customer, organisation and product

Maintainer decisions D1 B and D3, 2026-11-12 ([0045](/decisions/0045-customers-sign-in-upstream-and-the-sluis-token-carries-customer-org-and-product/)). The claims table above gains three rows:

| Claim | ID token | Access token | Value |
|---|---|---|---|
| `customer`, `org`, `product` | when the token came from an exchange of an upstream token | when the token came from an exchange of an upstream token | The customer, the organisation and the product that `exchange` set from the claims of the upstream token, through the mapping the installation declares for its realm. Never taken from the request. Absent on a token that did not come from such an exchange, such as a staff sign-in at sluis |

- **This replaces the bullet above about a regulated customer's tenant.** An installation serves one boundary and trusts the one realm of that boundary, so the three claims name only that boundary's customer, organisation and product. A grant name whose scope names a tenant is still never issued to a regulated audience.
- **The `memberships` claim of [0032](/decisions/0032-customer-identity-installations/) stays a road not taken.** A token carries the one organisation of the exchange, not a list.

### Amendment of 2026-11-18: one identity per tenant, and several sessions on the client (SB5 B)

2026-11-18 (maintainer decision SB5 B; [#1894](https://github.com/truvity/sluis/issues/1894)). A person who works for several vendors keeps one identity per tenant and holds several sessions in parallel on the client.

- **The `sub` is one per tenant, and the same for every client of that tenant.** It is the id of the `Person` item, which is scoped to the boundary and the tenant ([0019](/decisions/0019-person-and-upstream-link/#assigning-a-subject)), so a person known to two tenants has two subjects. The removal of the pairwise `sub` stands (SB1 A). What is replaced is the reason that "staff who work across vendors must be one person across the vendors' apps, audit and access review", here, in [0006](/decisions/0006-keys-and-sealing/#amendment-of-2026-11-18-the-pairwise-data-key-is-retired) and in [0045](/decisions/0045-customers-sign-in-upstream-and-the-sluis-token-carries-customer-org-and-product/): vendors are kept apart by grants, by separate installations and by a subject of their own, so each vendor sees only its own subject and can disable it on its own. No person is linked across tenants.
- **The client holds the sessions.** One browser keeps sessions for several tenants at once. The sign-in page of `session` shows an account picker and honours `prompt=select_account` and `login_hint`; switching does not sign the other session out, and logout of one leaves the others. The `sluis` command line keeps named profiles, one per tenant, chosen by `--profile` or an environment variable, like the contexts of a Kubernetes client. A relying party trusts one issuer and one tenant and keeps a cookie per site, so it needs nothing new. A token never carries data from another tenant's session. The build is [#1894](https://github.com/truvity/sluis/issues/1894), on the session work of [#793](https://github.com/truvity/sluis/issues/793).
- **Road not taken: one subject across the tenants of an installation.** A `Person` item at the scope of the boundary would hold the subject, and each tenant a membership item that points to it, with a link made by signing in with a second upstream identity while signed in with the first. It was the first version of the proposal. It lost because it needs a linking flow, an administrator's view of links and an audit of every link and unlink, and it shares one subject across vendors who must be able to keep each other out.
- **Requirements:** [REQ-SESS-058](/requirements/sess/#REQ-SESS-058) to [REQ-SESS-063](/requirements/sess/#REQ-SESS-063), and [REQ-OPS-147](/requirements/ops/#REQ-OPS-147) for the `sluisctl` profiles.

## Consequences

- A relying party reads a token that holds only the grant names it asked for, and a typo in a grant name stops the load, not the first sign-in of the person who needed it.
- The cost is a vocabulary to write and keep, which v1 let an installation skip; a new thing needs a line in it before any policy can name it, and a tenant cannot add a name at run time.
- A relying party that reads a grant name beyond its `requires` stops seeing it. Its `requires` must name that thing, or its `groups` override must, for the cases where one audience must read more.
- Short tokens mean more refreshes, and each is a signing operation on the token key. The load is small beside a card's rate ([Board capacity](/runtimes/bare-metal/#board-capacity)).
- A bearer token copied from a log is useful until it expires, at most one hour; an issuer, a tenant or a client that wants less sets it.
- Requirements: [REQ-TOKEN-033](/requirements/token/#REQ-TOKEN-033) to [REQ-TOKEN-039](/requirements/token/#REQ-TOKEN-039) and [REQ-POLICY-010](/requirements/policy/#REQ-POLICY-010) to [REQ-POLICY-013](/requirements/policy/#REQ-POLICY-013) are added.

## Roads not taken

- **An optional vocabulary, as in v1.** A name nobody declared would load, and a typo would grant nothing or the wrong thing until someone noticed.
- **Free-form claims merged from groups in the first working version.** Every merge is a way for configuration to widen a token; the fixed set lets the claims be tested as a whole.
- **A check of the declared names against each delimiter, as v1 makes.** v1 let a name hold any character but `:`, so no delimiter was safe in itself and the load tried every declared name. A v2 name holds letters, digits, `-` and `_`, so a delimiter outside them is safe whatever is declared, and the check cannot fail.
- **A pairwise `sub` per tenant or per client, kept as a per-client opt-in.** Maintainer decision SB1 A, 2026-11-18: no token carries one. It split one person into a subject for each tenant, so staff who work across vendors were several people in the apps, the audit log and the access review; the separation it gave is already given by grants and by separate installations. It may return as a per-client opt-in if an outside party ever needs unlinkable staff subjects, with its own decision.
- **A `sub` per client in the spelling of v1, such as the e-mail address.** A relying party that keys on the address would keep working, but a second spelling of every subject would be issued and tested for ever, an address is reused and renamed ([0019](/decisions/0019-person-and-upstream-link/#roads-not-taken)), and a person's `email` is a claim of its own that a relying party can read.
- **Every held grant name in every token.** A relying party would read names meant for other systems, and tokens would grow with the person's rights.
- **The v1 override word `groups: all`, and the override of a `rung:` or `emp:` family.** Maintainer decision G1 A, 2026-11-18: not carried into v2. A client lists the things it reads in `groups`, v2 issues no `rung:` or `emp:` names, and narrowing is always on. A client that needs every held grant name lists every thing; the load still refuses `groups: all`, which is not a list of things, so a v1 file is not read in a new meaning.
- **v1's lifetime by group (`lifetimes.<group>`, the shortest across the groups a caller holds).** Maintainer decision L1 A, 2026-11-18: not carried into v2 ([#1817](https://github.com/truvity/sluis/issues/1817)). The client's `ttl_cap` per audience and the issuer's lifetimes already shorten a token, and v2 has no `rung:` names to key a lifetime by.
- **A report mode before narrowing is enforced, as in v1.** v2 has no installation to migrate; the report mode only delays the rule.
- **Role inheritance across scopes.** `all:x:admin` reaching every environment is the surprise the explicit ladder avoids.
- **Wildcards in `requires`, in token claims or in the role position.** A wildcard there would admit or grant by pattern where the rule should name the thing.
- **A default shorter than v1's, such as 15 minutes.** A leaked bearer token would live a quarter as long, but every relying party tuned to v1's hour would refresh four times as often and each refresh is a signing operation; an issuer or a client can still choose it.
- **A maximum above an hour.** A leaked bearer token would stay useful for longer, and v1 itself caps an agent's access token at an hour.
- **Sender-constrained tokens for every audience.** The parity relying parties cannot check a binding, so those tokens would be bearer anyway.
- **A vocabulary that a tenant extends at run time.** A tenant could then name a permission that no reviewer saw, against the rule that a tenant's instance holds no authority its template did not carry.
- **A sealed grant as the claim a relying party reads.** A sealed grant is read only by sluis and lives about a minute; a relying party needs a signed token with names it can match.

## Follow-ups

- DPoP and mutual-TLS binding: [#912](https://github.com/truvity/sluis/issues/912).
