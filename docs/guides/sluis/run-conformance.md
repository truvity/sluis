# Run the OpenID conformance suite

Run the four OpenID Foundation profiles on Kubernetes or AWS Lambda. Results: [conformance findings](../../concepts/sluis/conformance-findings.md).

| profile | plan | attended |
|---|---|---|
| Config | `oidcc-config-certification-test-plan` | no |
| Basic OP | `oidcc-basic-certification-test-plan` | yes |
| RP-Initiated Logout | `oidcc-rp-initiated-logout-certification-test-plan` | yes |
| Back-Channel Logout | `oidcc-backchannel-rp-initiated-logout-certification-test-plan` | yes |

## Before you start

- Run at every major and minor release, by a person. A patch needs one only when it touches the issuing path.

- Deploy the suite so the issuer reaches its `/test/` paths over public HTTPS. In a cluster it is Service `conformance-suite` in namespace `conformance`.

- Have `curl`, `jq`, `python3`, headless Chrome, and `kubectl` or `aws`. Run one plan at a time: a second with the same `alias` interrupts the first.

- Send `X-Forwarded-Proto: https` on every hand-typed call to the suite, and drive a real browser.

The scripts default to Kubernetes:

| variable | default | on Lambda |
|---|---|---|
| `SECRETS` | `kubernetes`: Secret `<id>-client` in `CLIENT_NAMESPACE` (`access-issuer`) | `ssm`, with `INSTANCE` |
| `SUITE` | ClusterIP of `conformance-suite`, read with `KUBECTL` | the suite's control-plane URL |
| `RECOVERY` | `kubernetes`: a token for `SERVICE_ACCOUNT` in `NAMESPACE` (`access-issuer-recovery`) | `password`, with `RECOVERY_PASSWORD_CMD` |
| `ALIAS` | `access-issuer` | the path segment of the registered redirects |

## 1. Declare the clients

Add four temporary rows to `access.clients`, render and upgrade ([install](operate/install-with-helm.md)) or apply the Lambda stack.

```yaml
conformance:       # and conformance-2
  kind: confidential
  secret: {generate: true}   # or a name, delivered at clients/<id>/secret
  redirects: [https://conformance.example.com/test/a/access-issuer/callback]
  signed_out: [https://conformance.example.com/test/a/access-issuer/post_logout_redirect]
  requires: [all:sluis:operator]
conformance-bc:    # and conformance-bc-2
  kind: confidential
  secret: {generate: true}
  redirects: [https://conformance.example.com/test/a/access-issuer/callback]
  signed_out: [https://conformance.example.com/test/a/access-issuer/post_logout_redirect]
  backchannel_logout_uri: https://conformance.example.com/test/a/access-issuer/backchannel_logout
  requires: [all:sluis:operator]
```

`requires` must name a group recovery holds: `operator`, not `viewer`. The Back-Channel plan needs its own pair ([client fields](../../reference/sluis/policy-clients.md), [secrets](../../reference/sluis/secrets.md#the-names)).

```sh
curl -s https://<issuer>/.well-known/openid-configuration | jq .backchannel_logout_supported
kubectl -n access-issuer get secret conformance-client   # Lambda: aws ssm describe-parameters, name /sluis/<instance>/external/oidc/conformance
```

The check prints `true` and the secret exists. On Lambda it appears within five minutes.

## 2. Create the plans

```sh
ISSUER=https://access.example.com KUBECTL="kubectl --context prod@oidc" hack/conformance-plans.sh v1.64.0
ISSUER=https://access.example.com SECRETS=ssm INSTANCE=prod SUITE=http://10.0.0.7:8080 hack/conformance-plans.sh v1.77.0
```

It prints `SUITE=http://<ip>:8080` and four lines `config|basic|rp-logout|backchannel <plan-id>`. With `ssm`, your AWS role must decrypt `external/oidc/*`.

| plan | variant the script posts |
|---|---|
| Basic OP | `server_metadata=discovery`, `client_registration=static_client` |
| both logout plans | `client_registration=static_client`, `response_type=code` |
| on an error instead of a plan id | `GET /api/plan/available` lists the valid variants |

## 3. Run Config

```sh
SUITE=http://<ip>:8080 ISSUER=https://access.example.com hack/conformance_drive.py <config-plan-id>
```

Config needs no browser and ends `FINISHED / PASSED`. `just conformance-config <issuer>` starts its own suite, with no client.

## 4. Run the attended plans

Run `basic`, `rp-logout` and `backchannel` in that order with the same command. Flags: `--from <module>` resumes, `--only <module>` runs one, `--manual` leaves the sign-in to a person.

```sh
CONTEXT=prod@oidc ...                                                       # Kubernetes
RECOVERY=password RECOVERY_PASSWORD_CMD="sluisctl recovery password --instance prod --region eu-west-1" ...   # Lambda
```

On Lambda, turn recovery on for the run ([recover on Lambda](operate/recover-on-lambda.md)).

FAILED and INTERRUPTED disqualify a profile. Check that each REVIEW's screenshot shows the right page.

Recovery has no address or name, so `oidcc-scope-email`, `oidcc-scope-profile` and `oidcc-alternate-happy-flow` warn. Those and the screenshot modules need a person's Google sign-in.

A Back-Channel module in WAITING with `connection refused` in the issuer's log cannot reach the suite.

## 5. Clean up

Export each plan's results to the ticket. Remove the four rows, render and upgrade, turn recovery off. Verify: `kubectl -n access-issuer get secret conformance-client` returns `NotFound`. On Lambda, `sluisctl clients purge <id>` clears each record. Record the date, version and plan ids on the findings page.

## Roll back

Plans are records inside the suite. To run again, repeat step 1.
