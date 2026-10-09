# Policy: proofs, groups and claims

How a caller becomes a set of internal groups, and what those groups put in a token. Part of [the policy](policy.md).

## Proof to groups

Every caller arrives with a proof that sluis verifies but did not produce, and every proof resolves to a set of
internal groups. After that point a person and a job are the same thing.

| Proof | Becomes the groups... |
|---|---|
| a corporate sign-in | whose `members` contain a directory group the service confirms the account is in, **authoritatively** |
| a CI identity token | whose `matchers` the token's claims satisfy: `repository`, `owner`, `ref`, `workflow`, `environment`, `workflow_ref`, `job_workflow_ref`, `sha`, `event_name` and `ref_type` as globs, and `visibility` (`public`, `private` or `internal`) exactly |
| a Kubernetes ServiceAccount token | whose `matchers` name that namespace and ServiceAccount (`service_account`: `namespace` and `name`, `cluster` optional) |
| an AWS IAM role's outbound identity federation token | whose `matchers` (`aws`) name that account and role: [the `aws` matcher](#the-aws-matcher) |
| a signed-in address | whose `matchers` hold its `email`, or its `email_domain` |

Globs are Go's `path.Match`, where `*` does not cross a `/`. Every field left out matches anything, so a matcher written
before a field existed keeps meaning what it meant. `job_workflow_ref`
(`example-org/app/.github/workflows/release.yml@refs/heads/main`) is the workflow file the job is defined in (the called
file, for a reusable workflow), and `workflow_ref` the file the run started from. Together with `ref` and `event_name`
they pin a group to one reviewed workflow on one branch. That is the shape a group behind a write grant of a
[catalogue App](../../guides/sluis/connect/github-app-tokens.md#pinning-a-grant-to-one-workflow) should have.

`email` and `email_domain` matchers are the escape hatch for the day before any directory group exists, and for a
population no group describes. `members` is the normal way, because it is the one the directory can confirm and
therefore the one liveness gates. Attributes exist only in matchers, at the front door; there is no policy engine
behind it.

A token describes **one account**, never a person. Someone with accounts in two Workspaces has two identities with two
subjects. Linking accounts is the consumer's concern.

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
| `account` | exact, **required** | the 12-digit AWS account id |
| `role` | glob | the role's name, without its path |
| `path` | glob | the role's IAM path as AWS spells it: `/` for none, `/service/team/` otherwise. `*` does not cross a `/`; omit it to match any path (`path: "*"` does not match the root path `/`) |
| `function` | glob | the Lambda function ARN the token was requested from. A proof with no function never matches a rule that sets this |
| `org_id` | glob | the AWS Organizations id of the account |

The subject of the minted token is `aws:<account>:role/<path><name>` (`aws:111122223333:role/telemetry/otel-writer`);
it does not change per invocation. A matcher is `workload` in the console. At load, an empty `aws: {}`, an account that
is not twelve digits and a pattern that does not compile are refused. A build older than the one that introduced this
matcher refuses the key as unknown, so roll the service before the policy that uses it. The accounts whose tokens are
verified at all are `exchange.aws` of the policy document ([policy-document.md](policy-document.md));
the walk-through is [how-to/connect/aws-workloads.md](../../guides/sluis/connect/aws-workloads.md).

## Groups to token, by deep merge

The token's claims are the fixed identity claims plus the deep merge of the `claims` fragments of every group the caller
is in.

| Claim | What it says |
|---|---|
| `sub` | the account. A person is their **email**; a ServiceAccount (a workload, or a recovery sign-in) is **`<cluster>:k8s:<namespace>:<name>`**, or `k8s:<namespace>:<name>` when the installation names no cluster |
| `email`, `email_verified`, `preferred_username` | the address again |
| `name`, `given_name`, `family_name` | who they are, when the directory says. Absent for a workload |
| `groups` | the internal group names: **the whole of the authorization**. Flat, one string per group |
| `auth_time` | when the person signed in. A refresh an hour later carries the same `auth_time` and a fresh `iat` |
| `sid` | the session this token belongs to, the same id the console lists and revokes. Absent on a token no session backs |

The ID token carries all of them, and the userinfo endpoint answers the same set. A `service_account` matcher may name a
`cluster` to narrow to one; naming none matches any. The clusters whose ServiceAccount tokens count are not in the
policy tables: they are `exchange.clusters` of the policy document, one `{name, issuer, jwksUri}` row each, and a
`cluster` named in a matcher is the `name` of one of those rows. Renaming a row silently changes what every rule about
it matches. A cluster's tokens are checked against the key set it publishes, never by a call to the cluster.

| Kind | Merge rule |
|---|---|
| lists | union, de-duplicated, sorted |
| maps | merged recursively |
| scalars | may not conflict: two groups setting one key to different values is a **load-time error** |
| lifetime | the shortest across the caller's groups, then the client's `ttl_cap`, then `lifetimes.default` |

Conflicts are refused when the policy loads, not when someone in both groups signs in. Nothing in the file may make a
lifetime a function of a pair such as group and workspace, group and client, or group and person; an exception is a new
internal group. The reasoning is in [explanation/policy.md](../../concepts/sluis/policy.md#why-the-token-is-built-this-way).
