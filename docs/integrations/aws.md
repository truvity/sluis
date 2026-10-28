---
title: AWS
description: How people and CI jobs assume AWS roles with sluis as the OIDC provider, how AWS roles prove themselves to sluis, and how sluis's own roles reach AWS.
status: target
sidebar:
  order: 6
---

AWS appears in three directions, and it helps to keep them apart.

| Direction | Who proves what to whom | Mechanism |
|---|---|---|
| **Accounts trust sluis** | A person or a CI job proves its identity to sluis, then to an AWS account. | An IAM OIDC provider for sluis's issuer; `AssumeRoleWithWebIdentity` at STS. |
| **AWS roles prove themselves to sluis** | A Lambda function, container task or instance running as an IAM role calls a service that trusts sluis. | AWS outbound identity federation; sluis verifies the token (a `WorkloadVerifier`). |
| **sluis's own roles reach AWS** | A sluis role on a node writes to its bucket or uses its key. | The workload identity of [Identity](/concepts/identity/): a per-node issuer and `AssumeRoleWithWebIdentity`. |

## What it's for

- **An engineer assumes a role in an account** with their normal sign-in, without an identity-centre product. `aws --profile deployer@<account-id> s3 ls` works because the profile asks sluis for a token and STS accepts it.
- **A CI job deploys** with a role limited to that job. The job exchanges its CI identity token for a sluis token for one role's audience, and assumes the role. No access key is stored in CI.
- **A container registry and a package repository work on the same profile.** ECR's credential helper and `aws codeartifact login` use the profile that sluis wrote.
- **A Lambda function calls an internal service.** It asks AWS for an identity token for the service's audience, the service passes it to sluis, and sluis returns a token for the function's role and groups.
- **A sluis role writes its audit archive to a bucket** with credentials from `AssumeRoleWithWebIdentity`, so a compromised node can reach only the roles it runs.

## Integration roles

| Role | What it does for AWS |
|---|---|
| `WorkloadVerifier` | Verifies an AWS outbound-federation token: issuer is a declared account's issuer, audience matches, signature checks against the account's key set, the token is fresh, the account in the role ARN matches, and the subject is a role ARN. It rejects assumed-role sessions, users and the account root. |

The first and third directions need no integration process: the account trusts sluis's issuer, and the driver exchanges a workload token. They are **client profiles** and cloud-driver configuration. AWS does not play the `CredentialMinter` role: STS mints the credentials, and sluis only issues the token that STS accepts.

## What sluis asks the vendor for

Nothing long-lived. sluis holds no AWS access key for the federation directions. For the account side, the operator creates in AWS:

- an **IAM OIDC provider** for sluis's issuer URL (HTTPS, no port, no query);
- **one role per purpose**, with a trust policy that names the audience, and a `sub` condition when the role is for one workload only.

For the verifier, sluis reads an account's public key set and needs outbound access to the AWS token hosts. Each AWS account must have outbound identity federation turned on, and the role needs permission to ask for a token: the statement should limit the audience, the algorithm (ES384) and the duration.

Signing: AWS accepts ES384, the algorithm sluis uses by default, so no RSA key is needed for AWS.

## What policy controls

- **Who may assume which role.** Each role is a client of kind exchange, and `requires` names the groups that may ask for it. The decision rides in the token's `aud`; a trust policy per role reads it, so AWS holds no per-user statements.
- **For a workload:** matchers on account, role name and path, so a function in one account and path is a different identity from one in another.
- **Lifetime.** The shortest of the group lifetime and the client's cap, no refresh token. A revocation reaches AWS when the token expires, so give the client a short cap.
- **Trusted accounts**, for the verifier, are a declared list. An empty list verifies no AWS token.

## What is audited

- Every exchange, with the proof (person, CI or workload), the audience, the outcome and the groups.
- A refusal: a role in no group, an unknown audience, a token from an undeclared account (the caller sees one fixed answer for all of these).
- For sluis's own roles, AWS's own CloudTrail records each `AssumeRoleWithWebIdentity`, and the trust policy names the node's issuer and the role's subject.

## Setup

An example; see [the AWS pattern](/integrations/connecting/#aws-oidc-provider-and-role-trust).

1. Run `sluis integrations render aws --account example-prod`. It writes the IAM OIDC provider and one trust policy per role declared in the install file, as a template for your infrastructure-as-code tool, so the trust comes from the same file as the clients and cannot drift from them.
2. Apply it in the account.
3. Declare the roles as clients, with `requires`.
4. A person runs `sluis login`, then `sluis aws-config`, which writes a profile `<role>@<account>` per role they are granted.
5. A CI job uses the exchange action with `audiences: aws:<account>:<role>`.

```yaml
# /etc/sluis/install.yaml (example)
integrations:
  aws:
    example-prod:
      account: <account-id>
      roles:
        deployer: { requires: [ci-platform] }
        power:    { requires: [sre] }
      verifier:                      # accept AWS roles as workloads
        issuer: https://<id>.tokens.sts.global.api.aws
        algs: [ES384]
```

```ini
# ~/.aws/config written by the command (example)
[profile deployer@<account-id>]
credential_process = sluis aws --audience aws:<account-id>:deployer
region = eu-example-1
```

## v1 → v2 notes

- v1 does the account side and the verifier today. See [Connect an AWS account without Identity Center](/v1/guides/sluis/connect/aws-account/), [Connect AWS workloads](/v1/guides/sluis/connect/aws-workloads/) and [Use ECR and CodeArtifact](/v1/guides/sluis/connect/registries-and-artifacts/). The commands are `sluisctl aws-config` and `sluisctl aws`.
- v1 writes the IAM objects by hand or with its Pulumi library. In v2 the provider and trust policies come from the same configuration as the clients (the permission-manifest approach).
- The per-node workload identity for sluis's own roles is new in v2 ([#870](https://github.com/truvity/sluis/issues/870)).
- Parity issues: [#819](https://github.com/truvity/sluis/issues/819) roles through web identity, [#784](https://github.com/truvity/sluis/issues/784) AWS role tokens as a proof.

:::note[Proposed]
Generating the IAM OIDC provider and the trust policies from the configuration (`sluis integrations render aws`) is a design, not a decided command. How a template is delivered (CloudFormation, Terraform, plain JSON) is not yet decided.
:::
