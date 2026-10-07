# Policy: validation at load

What the loader refuses, and the one thing it warns about. Part of [the policy](policy.md). A typo fails the rollout, not
a login. Test a file before it ships: [how-to/test-the-policy.md](../how-to/test-the-policy.md).

## Refused at load

- Unknown keys, `memberships` among them.
- A key in `claims` or `lifetimes` that names no declared group, and a `requires` entry that names none.
- A member address without a domain.
- A scalar conflict across any two `claims` fragments.
- A matcher with no field; a `service_account` matcher without `namespace` and `name`; an `aws` matcher without a
  twelve-digit `account` or with a pattern that does not compile; a `github` matcher whose `visibility` is not `public`,
  `private` or `internal`.
- A client's `display_name` or `description` that is not one bounded line; `sign_in_exchange` on a client that is not
  `public`; a confidential client without a `secret`; an empty `requires`.
- `session: agent` on a client or on `client_documents`, in this release: agent-class sessions become available with
  the release that adds their consent page ([policy-clients.md](policy-clients.md#agent-class-sessions)).
- `session` on an `exchange` client, and `session: agent` together with `sign_in_exchange: true`. A `session: agent` client
  with `signed_out` or `backchannel_logout_uri` only warns ([policy-clients.md](policy-clients.md#agent-class-sessions)).
- A resource whose id is not an absolute URI without a fragment, or whose `requires` names no declared group.
- `client_documents` with an origin carrying a scheme, a path or a wildcard, with `origins` and no `requires`, or with
  `requires` or `ttl_cap` and no `origins`.
- Every vocabulary check ([policy-vocabulary.md](policy-vocabulary.md)), the `groups_delimiter` and `signing_alg`
  checks ([policy-clients.md](policy-clients.md)), and the binding checks for GitHub, people and Slack
  ([policy-bindings.md](policy-bindings.md)).
- A document with a `version` other than 1, or an `apiVersion` other than `sluis.truvity.github.io/policy/v2`.

Conflicts across files are refused when the layers merge, and the result is validated once.

## Groups nothing consumes

At start, sluis warns when an internal group is declared in `groups` but referenced by none of:

- any client's `requires`
- any resource's `requires`
- `client_documents` `requires`
- any GitHub organisation `members` binding
- any GitHub team `members` or `maintainers` binding
- any Slack channel's `from` binding
- any `groups:` override on a client, a resource or `client_documents`
- a GitHub App catalogue grant's group
- the service's own roles, `<scope>:access-roster:operator` and `<scope>:access-roster:viewer`, which the service reads
  directly from the token

A group referenced only by a `claims` or `lifetimes` key does not count: those decorate a group and do not consume it.
`rung:` and `emp:` groups are never reported. A group a relying party maps for its own roles is declared with a
`groups:` override on that client ([ADR 0006](../decisions/0006-groups-claim-scoped-per-audience.md)), and this lint
counts it. The warning is never an error: a group may be declared ahead of the client that will use it, but one that is
never used is usually a typo or a leftover, and the log at load is where an operator reads it.
