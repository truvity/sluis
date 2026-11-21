---
title: AWS
description: "How AWS accounts trust sluis as an OIDC issuer, how AWS roles prove themselves to sluis, and how sluis's own actors reach AWS, with no stored key."
status: target
sidebar:
  order: 5
---

AWS **federates**: it trusts sluis as an OpenID Connect issuer and takes its tokens, so sluis stores nothing of AWS and mints nothing for it ([0025](/decisions/0025-integrations/#credential-minters)). The one AWS integration kind is the `WorkloadVerifier`. Cloudflare, which cannot federate, is on [its own page](/integrations/cloudflare/).

AWS appears in three directions, and it helps to keep them apart.

| Direction | Who proves what to whom | Mechanism |
|---|---|---|
| **Accounts trust sluis** | A person or a CI job proves its identity to sluis, then to an AWS account. | An IAM OIDC provider for sluis's issuer; `AssumeRoleWithWebIdentity` at STS. |
| **AWS roles prove themselves to sluis** | A Lambda function, container task or instance running as an IAM role calls a service that trusts sluis. | AWS outbound identity federation; a workload verifier in `exchange` checks the token. |
| **sluis's own actors reach AWS** | A sluis actor writes to its bucket or uses its key. | The boundary's workload issuer and `AssumeRoleWithWebIdentity` ([Identity](/concepts/identity/#how-an-actor-reaches-a-cloud)). |

Nothing of this page is built; it rests on the issuer, the clients and the token exchange of the core. The second row is a v1 parity item ([#784](https://github.com/truvity/sluis/issues/784)). Open: [0022](/decisions/0022-workload-identity-and-host-enrolment/#proofs-and-the-exchange) lists `aws-iid` (the instance identity document) as the AWS proof and no record names the outbound-federation verifier of the second row.

## What AWS is for

- **An engineer assumes a role in an account** with their normal sign-in, without an identity-centre product. `aws --profile deployer@123456789012 s3 ls` works because the profile asks sluis for a token and STS accepts it.
- **A CI job deploys** with a role limited to that job. It exchanges its CI identity token for a sluis token for one role's audience and assumes the role. No access key is stored in CI.
- **A Lambda function calls an internal service.** It asks AWS for an identity token for the service's audience; sluis verifies it and returns a token for the function's role and groups.
- **A sluis actor writes to its bucket** with credentials from `AssumeRoleWithWebIdentity`, so a compromised node reaches only what the actors it runs may reach ([#870](https://github.com/truvity/sluis/issues/870)).

## Kinds of AWS integration

| Kind | What it does for AWS |
|---|---|
| `WorkloadVerifier` | The verifier driver of `exchange` verifies an AWS outbound-federation token: the issuer is a declared account's, the audience matches, the signature checks against the account's key set, the token is fresh, the account in the role ARN matches, and the subject is a role ARN. It refuses assumed-role sessions, users and the account root. |

The other two directions need no adapter: the account trusts sluis's issuer (a client profile), and a cloud driver exchanges a workload token. AWS has no minter: STS mints the credentials, and sluis only issues the token STS accepts.

## What sluis asks AWS for

Nothing long-lived: sluis holds no AWS access key for any direction. In each account the operator creates:

- an **IAM OIDC provider** for sluis's issuer URL (HTTPS, no port, no query);
- **one role per purpose**, whose trust policy names the audience, plus a `sub` condition when the role is for one workload.

For the verifier, sluis reads each account's public key set over outbound access to the AWS token hosts. Each account turns on outbound identity federation, and the role's policy allows it to ask for a token, limited to one audience, the ES384 algorithm and a short duration. AWS accepts ES384, sluis's default, so no RSA key is needed.

## What policy controls

- **Who may assume which role.** Each role is a client, and its `requires` names the groups that may ask for it. The decision rides in the token's `aud`, which the role's trust policy reads, so AWS holds no per-user statements.
- **For a workload:** matchers on account, role name and path.
- **Lifetime.** The shorter of the group lifetime and the client's cap, with no refresh token. A revocation reaches AWS when the token expires, so give the client a short cap.
- **Trusted accounts** for the verifier, as a declared list in the config kind `ExchangeTrust`. An empty list verifies no AWS token.

## What is audited

- Every exchange: the proof (person, CI or workload), the audience, the outcome and the groups.
- Each refusal: a role in no group, an unknown audience, a token from an undeclared account. The caller sees one fixed answer for all of them.
- For sluis's own actors, CloudTrail records each `AssumeRoleWithWebIdentity`.

## Setup

The provider and the role trust policies are rendered from the configuration, so trust cannot drift from the clients ([the AWS pattern](/integrations/connecting/#aws-oidc-provider-and-role-trust)). Names and keys are [illustrative](/integrations/#examples-are-illustrative).

1. Run `sluis integrations render aws --account acme-prod`, apply the template with your infrastructure tool, and declare the roles as clients with `requires`.
2. A person runs `sluis login`, then `sluis aws-config`, which writes a profile `<role>@<account>` per role they are granted.
3. A CI job uses the exchange action with `audiences: aws:<account>:<role>`, where `<account>` is the account's declared name, never its number ([0017](/decisions/0017-identity-and-sign-in/#audiences)).

```yaml
# ExchangeTrust and InstallationClient, config kinds (example)
accounts:
  acme-prod:
    account: "123456789012"
    verifier:                      # accept AWS roles as workloads
      issuer: https://<id>.tokens.sts.global.api.aws
      algs: [ES384]
clients:
  aws:acme-prod:deployer: { requires: [ci-platform] }
  aws:acme-prod:power:    { requires: [sre] }
```

```ini
# ~/.aws/config written by the command (example)
[profile deployer@acme-prod]
credential_process = sluis aws --audience aws:acme-prod:deployer
region = eu-example-1
```
