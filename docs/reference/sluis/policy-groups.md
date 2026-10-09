# Policy: proofs, groups and claims

How a caller becomes internal groups and what those groups put in a token, part of [the policy](policy.md). Rationale: [why the token is built this way](../../concepts/sluis/policy.md#why-the-token-is-built-this-way).

## Proof to groups

Sluis verifies every proof and resolves it to internal groups. A token describes one account, never a person.

| Proof | Becomes the groups... |
|---|---|
| corporate sign-in | whose `members` hold a directory group the service confirms authoritatively |
| CI identity token | whose `matchers` the claims satisfy (fields below) |
| Kubernetes ServiceAccount token | whose `matchers` name the namespace and ServiceAccount (`service_account`: `namespace`, `name`, optional `cluster`) |
| AWS IAM role federation token | whose `matchers` (`aws`) name the account and role: [the `aws` matcher](#the-aws-matcher) |
| signed-in address | whose `matchers` hold its `email` or `email_domain` |

| Rule | Value |
|---|---|
| CI glob fields | `repository`, `owner`, `ref`, `workflow`, `environment`, `workflow_ref`, `job_workflow_ref`, `sha`, `event_name`, `ref_type` |
| CI literal field | `visibility`: `public`, `private` or `internal` |
| Glob syntax | Go `path.Match`; `*` does not cross `/` |
| Omitted field | matches anything |
| `job_workflow_ref` | workflow file defining the job, e.g. `example-org/app/.github/workflows/release.yml@refs/heads/main` |
| `workflow_ref` | file the run started from |
| Pin a write grant | `job_workflow_ref` with `ref` and `event_name`: [pinning a grant to one workflow](../../guides/sluis/connect/github-app-tokens.md) |
| `email`, `email_domain` | for populations no directory group describes; liveness does not gate them |
| Attributes | only in matchers, at the front door |
| `cluster` in a matcher | `name` of an `exchange.clusters` row of the policy document; renaming a row changes what its rules match |
| Cluster token check | against its published key set, never a call to the cluster |
| Two Workspace accounts | two identities, two subjects |

### The `aws` matcher

```yaml
groups:
  otlp:billing:writer:
    matchers:
      - aws: { account: "111122223333", role: "billing-*" }
      - aws: { account: "111122223333", path: /telemetry/, role: "*" }
      - aws:
          account: "444455556666"
          role: otel-writer
          function: "arn:aws:lambda:eu-west-1:444455556666:function:billing-*"
          org_id: o-example1234
```

| Field | Match | Meaning |
|---|---|---|
| `account` | literal, required | 12-digit AWS account id |
| `role` | glob | role name without path |
| `path` | glob | IAM path: `/` for none, `/service/team/` otherwise; omit to match any; `path: "*"` does not match `/` |
| `function` | glob | Lambda function ARN; a proof with no function never matches a rule setting this |
| `org_id` | glob | AWS Organizations id |

| Rule | Value |
|---|---|
| Token subject | `aws:<account>:role/<path><name>`, e.g. `aws:111122223333:role/telemetry/otel-writer`; constant per invocation |
| Console name | `workload` |
| Refused at load | empty `aws: {}`; account not twelve digits; pattern that does not compile |
| Older build | refuses the key as unknown; roll the service first |
| Verified accounts | `exchange.aws` of the [policy document](policy-document.md) |
| How | [connect AWS workloads](../../guides/sluis/connect/aws-workloads.md) |

## Groups to token, by deep merge

The claims are the fixed identity claims plus the deep merge of the `claims` fragments of every group the caller holds.

| Claim | Value |
|---|---|
| `sub` | person: their email; ServiceAccount: `<cluster>:k8s:<namespace>:<name>`, or `k8s:<namespace>:<name>` with no cluster |
| `email`, `email_verified`, `preferred_username` | the address |
| `name`, `given_name`, `family_name` | from the directory; absent for a workload |
| `groups` | internal group names, flat, one string each; all of the authorization |
| `auth_time` | sign-in time; unchanged across refreshes, `iat` is fresh |
| `sid` | session id as the console lists it; absent when no session backs the token |

The ID token carries all of them. The userinfo endpoint answers the same set.

| Kind | Merge rule |
|---|---|
| lists | union, de-duplicated, sorted |
| maps | merged recursively |
| scalars | two groups setting one key differently is a load-time error |
| lifetime | shortest across the caller's groups, then client `ttl_cap`, then `lifetimes.default` |

Conflicts are refused at policy load. A lifetime cannot depend on a pair such as group and client; use a new internal group.
