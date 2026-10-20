# What did the OpenID conformance run find?

sluis targets four OpenID Foundation profiles and claims one only when the suite passes it. To produce a run, follow [run conformance](../../guides/sluis/run-conformance.md).

The last full run was on 2026-10-20 against sluis v1.77.1, on an AWS Lambda deployment, with suite 5.3.3. It is a self-run, not a certification. A re-run on v1.77.2 is pending.

## The 2026-10-20 run on v1.77.1

| Profile | Plan | Passed | Warning | Review | Skipped | **Failed** |
|---|---|--:|--:|--:|--:|--:|
| [Config OP](https://openid.net/certification/connect_op_testing/) | `c8JDtGp8cCoQL` | 1 | 0 | 0 | 0 | **0** |
| [Basic OP](https://openid.net/certification/connect_op_testing/) | `hEgLDyg14iIez` | 20 | 7 | 4 | 4 | **0** |
| [RP-Initiated Logout OP](https://openid.net/certification/connect_op_logout_testing/) | `5I5FLjBnciJb3` | 3 | 0 | 8 | 0 | **0** |
| [Back-Channel Logout OP](https://openid.net/certification/connect_op_logout_testing/) | `4EVYlSFBN15vw` | 2 | 0 | 0 | 0 | **0** |

Only a failed or interrupted module disqualifies a profile, and none failed. A submission needs RP-Initiated Logout plus one other logout profile.

v1.77.2 fixes each finding ([upgrade to v1.77.2](../../guides/sluis/upgrade/v1.77.2.md)):

| Module | Finding on v1.77.1 | Fix in v1.77.2 |
|---|---|---|
| `oidcc-server` | warning: the ID token carries `client_id` | `client_id` leaves the ID token and `claims_supported` |
| `oidcc-scope-profile`, `oidcc-alternate-happy-flow`, `oidcc-claims-essential` | warning: userinfo lacks the `profile` claims | the conformance account answers `profile` with `name` and `preferred_username` |
| `oidcc-scope-email` | warning: userinfo lacks `email` | email and names follow the granted scopes; the account still has no address |
| `oidcc-ensure-request-with-acr-values-succeeds` | warning: the `acr` is not one requested | discovery lists the conformance class while the account is on |
| `oidcc-codereuse-30seconds` | warning: userinfo answers 200 after a reused code | a reused code revokes its tokens |
| `oidcc-rp-initiated-logout-bad-id-token-hint` | review: *Sign out?* where an error is due | an invalid `id_token_hint` is a `400` before the page |
| `oidcc-rp-initiated-logout-no-params`, `-only-state` | review: the driver photographed *Sign out?* | the driver presses *Sign out* |

A run on 2026-09-12 against v1.0.0 had no failed module either.

## Review is a screenshot handed to a person

Twelve modules end at a page the suite cannot see: the issuer must refuse, so it never redirects back. A person checks every screenshot.

| What the step demands | Modules | What the screenshot shows |
|---|--:|---|
| an error page, the `redirect_uri` is not registered | 2 | *That sign-in request was not valid* |
| an error page, the `id_token_hint` is not valid | 2 | *That sign-out request was not valid: id_token_hint invalid* |
| an error page, the `post_logout_redirect_uri` is not registered | 3 | *That sign-out request was not valid: post_logout_redirect_uri invalid* |
| the successful logout page | 3 | *Signed out*, after the driver presses the button on *Sign out?* |
| the login prompt during a second authorization | 2 | the sign-in chooser |

A review with the wrong evidence passes silently. The 2026-10-20 check caught three of *Sign out?*.

## Skipped means the discovery document is truthful

Three modules skip because `scopes_supported` omits `address` and `phone`. One skips because the issuer answers `request_not_supported`.

## Warnings are mostly personal data sluis does not hold

Four warnings say userinfo omits claims the `profile` and `email` scopes permit. It returns those with a real source.

| Claim | Scope | A person | The conformance account |
|---|---|---|---|
| `email`, `email_verified` | `email` | the address the directory vouched for | absent, it has no address |
| `preferred_username` | `profile` | the address | `conformance`, its username |
| `name` | `profile` | the directory's names, joined | `OpenID conformance test account` |
| `given_name`, `family_name` | `profile` | the directory's, when it supplies them | absent |

A person's claims need their scope. The conformance account's arrive in userinfo only, and only with `profile`.

No source holds `birthdate`, `gender`, `zoneinfo`, `locale`, `picture`, `website`, `profile`, `nickname`, `middle_name` or `updated_at`, so the `profile` warning stays. The `email` warning stays for the conformance account only.

The other two come from one module: the ID token carries claims no requested scope covers. One is `groups`, which Kargo and ArgoCD read, so gating it would break every consumer. The other was `client_id`, a duplicate of `azp`, now gone from the ID token and `claims_supported`.

Upstream zitadel/oidc has not released that fix ([issue 1020](https://github.com/zitadel/oidc/issues/1020), [pull request 1021](https://github.com/zitadel/oidc/pull/1021)). Until it does, sluis builds a patched copy, `third_party/zitadel-oidc`, through a `replace` in `go.mod`. Dropping either earlier brings the warning back in `oidcc-server` and every module that runs `EnsureIdTokenDoesNotContainNonRequestedClaims`.

`oidcc-ensure-request-with-acr-values-succeeds` asks for every `acr` class discovery lists, which includes the conformance class while `conformance.enabled` is on.

| Sign-in | `acr` |
|---|---|
| a directory provider | `urn:truvity:sluis:acr:directory` |
| recovery, by token or by password | `urn:truvity:sluis:acr:recovery` |
| the conformance account | `urn:truvity:sluis:acr:conformance` |

A token never claims a class its sign-in did not meet. An exchange carries no `acr`.

## Back-channel logout needs the suite in the cluster

In the Back-Channel module the issuer must reach the suite, because a logout token is a server-to-server POST. A suite on a laptop resolves to the pod's own loopback, and pods cannot reach the tailnet.

The suite therefore runs in the cluster, at a hostname of its own, while the conformance client rows are declared. Only its relying-party paths are public. See [back-channel logout](back-channel-logout.md).

## How a run happens

A person runs the whole suite at every major and minor release. One profile alone can pass while the two that sign somebody in have not run.

The Config profile (`just conformance-config`) stays one click away. It needs no credential and guards the discovery document.

[`hack/conformance_drive.py`](../../../hack/conformance_drive.py) drives headless Chrome through the browser half: it signs in through recovery (a token on a cluster, the test account's password on Lambda) and answers the manual steps with a screenshot. Thirty-five modules are one command. Pass `--manual` to hand one module's browser step to a person, say to check claims with a real account.

## A reused code revokes what it issued

Suite 5.3.3 warned in `oidcc-codereuse-30seconds`: after a reuse, userinfo still answered 200 to the first access token. The suite's client asks for no refresh token, so no session stood behind it.

A redemption now leaves a tombstone of the code, keyed by its hash, naming the access token and any session it issued. A reuse revokes both while that token lives ([the key](../../reference/sluis/keys.md)).
