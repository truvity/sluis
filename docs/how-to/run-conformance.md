# Run the OpenID conformance suite

## Purpose

Run the four OpenID Foundation profiles sluis claims against an installation, and decide whether the run is clean. What
the last run said is on [the findings page](../explanation/conformance-findings.md); this page is the procedure.

| profile | plan | attended |
| -- | -- | -- |
| Config | `oidcc-config-certification-test-plan` | no |
| Basic OP | `oidcc-basic-certification-test-plan` | yes |
| RP-Initiated Logout | `oidcc-rp-initiated-logout-certification-test-plan` | yes |
| Back-Channel Logout | `oidcc-backchannel-rp-initiated-logout-certification-test-plan` | yes |

The Foundation certifies logout only as RP-Initiated **plus at least one** of Session Management, Front-Channel or
Back-Channel. sluis serves Back-Channel only ([why](../explanation/back-channel-logout.md)),
so the RP-Initiated and Back-Channel pair is the smallest set that can be submitted; neither half counts alone.

## Preconditions

- A person, for the attended profiles, at every major and minor release, as a whole. Not on a schedule and not in CI: one
  profile passing daily reads as "conformance passes" while the two that sign somebody in have not run for weeks. A patch
  release needs no run, unless it touches the issuing path.
- The suite running in the cluster of the installation under test, from the Foundation's image, as Service
  `conformance-suite` in namespace `conformance` (the scripts assume both; deploying it is the estate's, not this
  chart's). It must be reachable by the issuer: a logout token is a server-to-server POST, and a suite on a laptop at a
  name that resolves to `127.0.0.1` is, from the pod, the pod's own loopback.
- Only the suite's relying-party half (`/test/`: the callback, the post-logout landing page, the back-channel endpoint) is
  published on its hostname. The control plane (UI, API, stored client secrets) is on the ClusterIP only, because the
  suite has no login of its own: reach it over the tailnet.
- `kubectl` for that cluster (a context that authenticates through OIDC needs `kubectl-oidc_login` on PATH; set
  `KUBECTL="kubectl --context ..."` to wrap it), `curl`, `jq`, `python3`, headless Chrome.
- The installation under test uses the namespace `access-issuer` and the recovery ServiceAccount `access-issuer-recovery`:
  `hack/conformance_drive.py` and `hack/conformance-plans.sh` hard-code both, and read the client secrets from
  Secrets named `conformance-client`, `conformance-2-client`, `conformance-bc-client` and `conformance-bc-2-client`
  (key `client-secret`) in that namespace.

## Before you start

- **Run one plan at a time.** The suite serves each plan's callback under its `alias`. A second plan with the same alias
  interrupts the running one, and says so only in its own log ("Stopping test due to alias conflict"), which from
  outside looks like a hang. A different alias is refused a redirect the clients never registered.
- **The alias in the client's paths must equal the plan's `alias`** (`access-issuer` below): a mismatch reads as the
  issuer refusing the redirect.
- **Every call to the suite carries `X-Forwarded-Proto: https`.** At the ClusterIP there is no nginx to add it, and the
  suite refuses a request that does not claim HTTPS. The scripts add it; a hand-typed `curl` has to.
- **The driver must be a real browser.** The suite's callback is an HTML page that posts the result back with JavaScript;
  `curl` reaches it, never runs it, and the module sits in WAITING after the authorization succeeded.
- **`requires` is mandatory on a client** (the policy refuses to load a client that requires no group), and it must
  name a group the driver holds. The driver signs in as the recovery ServiceAccount, which has no email: it holds
  `all:access-roster:operator` through a `service_account` matcher and not `viewer` (email domain), so a row requiring
  only viewer refuses the driver and every module fails at the sign-in.
- **The Back-Channel plan needs its own client pair.** A client that declares `backchannel_logout_uri` is POSTed a token
  at every sign-out, which the RP-Initiated modules count as a failure; a client that declares none is never contacted
  and every Back-Channel module waits. Hence two pairs.
- **A recovery sign-in cannot prove the claim-bearing modules.** A ServiceAccount has no address or name, so
  `oidcc-scope-email`, `oidcc-scope-profile` and `oidcc-alternate-happy-flow` warn for a reason that does not exist for a
  person. The unattended run is evidence for the protocol modules; those and the screenshot modules
  (`oidcc-prompt-login`, the missing-response-type error page) need a person's pass with a real Google sign-in.
- **Preview the policy change before it is applied, and read the preview**: the four client rows are temporary and go in
  and out through the estate's normal change (`sluisctl render`, then upgrade), [install](install-with-helm.md).
- **The freeze before a migration** (skip-reconcile, [day two](cutover.md)) is not
  involved: never run the suite during a cutover.

## Steps

### 1. Declare the clients

**Run** add four rows to the installation's `access.clients`, render and upgrade. They are temporary: a standing client
whose only purpose was a certification is surface with no consumer. Each pair is `kind: confidential`, with its secret
`clients/<id>/secret` delivered like any client's ([configuration](../reference/secrets.md#the-names)).

```yaml
conformance:       # and conformance-2: slots `client`/`client_secret_post` and `client2`
  kind: confidential
  redirects: [https://conformance.example.com/test/a/access-issuer/callback]
  signed_out: [https://conformance.example.com/test/a/access-issuer/post_logout_redirect]
  requires: [all:access-roster:operator, all:access-roster:viewer]
conformance-bc:    # and conformance-bc-2: the Back-Channel pair
  kind: confidential
  redirects: [https://conformance.example.com/test/a/access-issuer/callback]
  signed_out: [https://conformance.example.com/test/a/access-issuer/post_logout_redirect]
  backchannel_logout_uri: https://conformance.example.com/test/a/access-issuer/backchannel_logout
  requires: [all:access-roster:operator, all:access-roster:viewer]
```

The plans want three client slots that map onto two clients: `client` and `client_secret_post` are the same client (the
credential comes from the Basic header or the form), `client2` is a second, for the tests that check a code issued to one
cannot be redeemed by the other. Fields: [policy](../reference/policy.md).

**Expect** the rollout completes.

**Verify** `kubectl -n access-issuer get secret conformance-client` exists, and
`curl -s https://<issuer>/.well-known/openid-configuration | jq .backchannel_logout_supported` is `true`.

**Rollback** remove the rows (this is also step 5).

### 2. Create the plans

**Run**

```sh
ISSUER=https://access.example.com KUBECTL="kubectl --context prod@oidc" hack/conformance-plans.sh v1.64.0
```

It finds the suite's ClusterIP, reads the secrets at the moment of use into a temporary directory that is removed on
exit (they are never printed), and creates the four plans, with these variants (ask `GET /api/plan/available` rather than
guess): Basic OP `server_metadata=discovery`, `client_registration=static_client`; the two logout plans
`client_registration=static_client`, `response_type=code` (sluis serves only `code`). Posting the Basic variant to a
logout plan returns an error with no plan id.

**Expect** `SUITE=http://<ip>:8080` and four lines `config|basic|rp-logout|backchannel <plan-id>`.

**Verify** each id resolves: `curl -s -H 'X-Forwarded-Proto: https' $SUITE/api/plan/<id> | jq .planName`.

**Rollback** none needed: a plan is a record inside the suite. It holds the client secrets, which is why the control
plane is not public.

### 3. Run Config (unattended)

**Run**

```sh
SUITE=http://<ip>:8080 ISSUER=https://access.example.com CONTEXT=prod@oidc hack/conformance_drive.py <config-plan-id>
```

Config needs no client and no browser: it reads the discovery document and the key set. It is also what the workflow
`.github/workflows/conformance.yaml` runs on a manual dispatch with an `issuer` input, which needs no credential.

**Expect** `FINISHED / PASSED`.

**Verify** repeat it after anything that changes discovery; that is all it reads.

**Rollback** none, because it changes nothing in the installation.

### 4. Run the three attended plans, one at a time

**Run**, for each of `basic`, `rp-logout`, `backchannel`, in that order:

```sh
SUITE=http://<ip>:8080 ISSUER=https://access.example.com CONTEXT=prod@oidc hack/conformance_drive.py <plan-id>
```

The suite has no "run all": the driver starts each module in order (Basic OP has thirty), waits for it, and prints the
result. `--from <module>` resumes after a failure; `--only <module>` runs one; `--manual` leaves the sign-in to a person.
The driver starts a **fresh browser per module**, because several say "remove any cookies you may have received from
the OpenID Provider": a carried session fails `oidcc-prompt-none-not-logged-in`, correct behaviour recorded as a
defect. It signs in as recovery whenever the issuer asks, and answers the suite's screenshot requests from the browser
it drives (eight of the eleven RP-Initiated modules end on a refusal page the issuer served; it photographs every
issuer page passed through and the latest answers).

**Expect** every module PASSED, REVIEW, WARNING or SKIPPED. Only FAILED and INTERRUPTED disqualify a profile. REVIEW
means a person must look at the attached evidence, a screenshot.

**Verify** open the evidence of each REVIEW and check it shows the right page: a REVIEW whose screenshot shows the
wrong page is a failure the suite cannot see. A Back-Channel module stuck in WAITING after "Received front channel
redirect; waiting for back channel request", with `connection refused` in the issuer's log, means the issuer cannot
reach the suite (see Preconditions).

**Rollback** none, because a run only signs in and out; a stuck module is interrupted by starting the next.

### 5. Clean up

**Run** export each plan's results from the suite and attach them to the ticket (a green run nobody kept cannot be
checked). Then remove the four client rows, render and upgrade.

**Expect** the rows and their secrets are gone.

**Verify** `curl -s https://<issuer>/.well-known/openid-configuration` is unchanged, and
`kubectl -n access-issuer get secret conformance-client` is `NotFound`.

**Rollback** to run again, repeat step 1. Forgetting this step is the only way a run leaves anything behind.

## Afterwards

- Update [the findings page](../explanation/conformance-findings.md) with the date, version and plan ids.
- Tell whoever submits to the Foundation which plan ids are the pair.
- A grant withdrawn from discovery must also stop answering: the half worth checking is that nothing the design names as
  out is served ([endpoints](../reference/endpoints.md)).
