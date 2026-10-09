# Run the OpenID conformance suite

Run the four OpenID Foundation profiles against an installation. Results: [conformance findings](../../concepts/sluis/conformance-findings.md).

| profile | plan | attended |
|---|---|---|
| Config | `oidcc-config-certification-test-plan` | no |
| Basic OP | `oidcc-basic-certification-test-plan` | yes |
| RP-Initiated Logout | `oidcc-rp-initiated-logout-certification-test-plan` | yes |
| Back-Channel Logout | `oidcc-backchannel-rp-initiated-logout-certification-test-plan` | yes |

## Before you start

- Run at every major and minor release, by a person. A patch release needs a run only when it touches the issuing path.

- Deploy the suite in the installation's cluster as Service `conformance-suite` in namespace `conformance`. The issuer must reach it.

- Have `kubectl`, `curl`, `jq`, `python3` and headless Chrome.

- The scripts hard-code the namespace `access-issuer`, the ServiceAccount `access-issuer-recovery` and four Secrets named `conformance[-2|-bc|-bc-2]-client` with key `client-secret`.

- Run one plan at a time. A second plan with the same `alias` interrupts the first and looks like a hang.

- Send `X-Forwarded-Proto: https` on every hand-typed call to the suite, and drive a real browser.

## 1. Declare the clients

Add four temporary rows to `access.clients`, render and upgrade ([install](operate/install-with-helm.md)). Deliver each secret at `clients/<id>/secret` ([secrets](../../reference/sluis/secrets.md#the-names)).

```yaml
conformance:       # and conformance-2
  kind: confidential
  redirects: [https://conformance.example.com/test/a/access-issuer/callback]
  signed_out: [https://conformance.example.com/test/a/access-issuer/post_logout_redirect]
  requires: [all:access-roster:operator, all:access-roster:viewer]
conformance-bc:    # and conformance-bc-2
  kind: confidential
  redirects: [https://conformance.example.com/test/a/access-issuer/callback]
  signed_out: [https://conformance.example.com/test/a/access-issuer/post_logout_redirect]
  backchannel_logout_uri: https://conformance.example.com/test/a/access-issuer/backchannel_logout
  requires: [all:access-roster:operator, all:access-roster:viewer]
```

`requires` must name a group the recovery ServiceAccount holds: `operator`, not `viewer`. The Back-Channel plan needs its own pair ([client fields](../../reference/sluis/policy-clients.md)).

```sh
kubectl -n access-issuer get secret conformance-client
curl -s https://<issuer>/.well-known/openid-configuration | jq .backchannel_logout_supported
```

The Secret exists and the check prints `true`.

## 2. Create the plans

```sh
ISSUER=https://access.example.com KUBECTL="kubectl --context prod@oidc" hack/conformance-plans.sh v1.64.0
```

It prints `SUITE=http://<ip>:8080` and four lines `config|basic|rp-logout|backchannel <plan-id>`.

| plan | variant the script posts |
|---|---|
| Basic OP | `server_metadata=discovery`, `client_registration=static_client` |
| both logout plans | `client_registration=static_client`, `response_type=code` |
| on an error instead of a plan id | `GET /api/plan/available` lists the valid variants |

## 3. Run Config

```sh
SUITE=http://<ip>:8080 ISSUER=https://access.example.com CONTEXT=prod@oidc hack/conformance_drive.py <config-plan-id>
```

Config needs no browser and ends `FINISHED / PASSED`. The `conformance.yaml` workflow runs it on a manual dispatch with an `issuer` input.

## 4. Run the attended plans

Run `basic`, `rp-logout` and `backchannel` in that order with the same command and the plan id. Flags: `--from <module>` resumes, `--only <module>` runs one, `--manual` leaves the sign-in to a person.

FAILED and INTERRUPTED disqualify a profile. Open each REVIEW's screenshot and check it shows the right page.

The recovery sign-in has no address or name, so `oidcc-scope-email`, `oidcc-scope-profile` and `oidcc-alternate-happy-flow` warn. Those and the screenshot modules need a person's pass with a Google sign-in.

A Back-Channel module stuck in WAITING with `connection refused` in the issuer's log cannot reach the suite.

## 5. Clean up

Export each plan's results and attach them to the ticket. Remove the four client rows, render and upgrade. Verify: `kubectl -n access-issuer get secret conformance-client` returns `NotFound`. Record the date, version and plan ids on the findings page.

## Roll back

Plans are records inside the suite. To run again, repeat step 1.
