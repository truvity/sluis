# What did the OpenID conformance run find?

sluis targets four OpenID Foundation profiles and claims one only when the suite passes it. To produce a run, follow [run conformance](../../guides/sluis/run-conformance.md).

The last full run was on 2026-09-12 against the deployed issuer at v1.0.0 (plans `xywkLCaSn66Wo`, `QQw1OfSOykbnC`, `Ps1nixuqZlBQp`, `6hKMyvxwF8baj`).

| Profile | Passed | Review | Skipped | Warning | **Failed** |
|---|--:|--:|--:|--:|--:|
| [Config OP](https://openid.net/certification/connect_op_testing/) | 1 | 0 | 0 | 0 | **0** |
| [Basic OP](https://openid.net/certification/connect_op_testing/) | 21 | 4 | 4 | 6 | **0** |
| [RP-Initiated Logout OP](https://openid.net/certification/connect_op_logout_testing/) | 3 | 8 | 0 | 0 | **0** |
| [Back-Channel Logout OP](https://openid.net/certification/connect_op_logout_testing/) | 2 | 0 | 0 | 0 | **0** |

Only a failed or interrupted module disqualifies a profile, so all four are certifiable. A submission needs RP-Initiated Logout plus one other logout profile, and that pair is green.

## Review is a screenshot handed to a person

Twelve modules end at a page the suite cannot see: the issuer must refuse, so it never redirects back. A person confirmed all twelve screenshots on 2026-09-12.

| What the step demands | Modules | What the screenshot shows |
|---|--:|---|
| an error page, the `redirect_uri` is not registered | 2 | *That sign-in request was not valid* |
| an error page, the `id_token_hint` is not valid | 2 | *That sign-out request was not valid: id_token_hint invalid* |
| an error page, the `post_logout_redirect_uri` is not registered | 3 | *That sign-out request was not valid: post_logout_redirect_uri invalid* |
| the successful logout page | 3 | *Signed out*, after the driver presses the button on *Sign out?* |
| the login prompt during a second authorization | 2 | the sign-in chooser |

Check the screenshots: a review with the wrong evidence passes silently. A 2026-10-20 run against v1.77.1 (plan `5I5FLjBnciJb3`) caught three of *Sign out?*: the issuer's fault in `oidcc-rp-initiated-logout-bad-id-token-hint`, the driver's in `-no-params` and `-only-state`.

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
