# What did the OpenID conformance run find?

sluis targets four OpenID Foundation profiles and claims one only when the suite passes it. To produce a run, follow [run conformance](../../guides/sluis/run-conformance.md).

The last full run was on 2026-10-20 against sluis v1.77.2, on an AWS Lambda deployment, with suite 5.3.3. A second, independent installation ran Basic OP and RP-Initiated Logout on 2026-10-21 against v1.77.5 with the same suite. Both are self-runs, not certifications. No module failed in either, and each warning is accepted below.

## The 2026-10-20 run on v1.77.2

| Profile | Passed | Warning | Review | Skipped | **Failed** |
|---|--:|--:|--:|--:|--:|
| [Config OP](https://openid.net/certification/connect_op_testing/) | 1 | 0 | 0 | 0 | **0** |
| [Basic OP](https://openid.net/certification/connect_op_testing/) | 23 | 4 | 4 | 4 | **0** |
| [RP-Initiated Logout OP](https://openid.net/certification/connect_op_logout_testing/) | 3 | 0 | 8 | 0 | **0** |
| [Back-Channel Logout OP](https://openid.net/certification/connect_op_logout_testing/) | 2 | 0 | 0 | 0 | **0** |

Only a failed or interrupted module disqualifies a profile, and none failed. Config ran in the same run. A submission needs RP-Initiated Logout plus one other logout profile.

The four Basic warnings come from reruns with a real Google sign-in, which the conformance account cannot stand in for. They are listed below.

Fixed since v1.77.1 ([upgrade to v1.77.2](../../guides/sluis/upgrade/v1.77.2.md)):

| Module | Finding on v1.77.1 | Fix in v1.77.2 |
|---|---|---|
| `oidcc-server` | warning: the ID token carries `client_id` | `client_id` leaves the ID token and `claims_supported` |
| `oidcc-ensure-request-with-acr-values-succeeds` | warning: the `acr` is not one requested | discovery lists the conformance class while the account is on |
| `oidcc-codereuse-30seconds` | warning: userinfo answers 200 after a reused code | a reused code revokes its tokens |
| `oidcc-rp-initiated-logout-bad-id-token-hint` | review: *Sign out?* where an error is due | an invalid `id_token_hint` is a `400` before the page |
| `oidcc-rp-initiated-logout-no-params`, `-only-state` | review: the driver photographed *Sign out?* | the driver presses *Sign out* |
| the `profile` and `email` modules | warning: userinfo lacks the claims | userinfo returns claims that have a source, under their scope |

A run on 2026-09-12 against v1.0.0 had no failed module either.

## The 2026-10-21 run on a second installation (v1.77.5)

A second, independent installation ran two profiles against v1.77.5 with suite 5.3.3.

| Profile | Modules | Passed | Warning | Review | Skipped | **Failed** |
|---|--:|--:|--:|--:|--:|--:|
| [Basic OP](https://openid.net/certification/connect_op_testing/) | 35 | 23 | 4 | 4 | 4 | **0** |
| [RP-Initiated Logout OP](https://openid.net/certification/connect_op_logout_testing/) | 11 | 3 | 0 | 8 | 0 | **0** |

Back-Channel Logout and Config did not run there. The findings equal those of v1.77.2:

- The same four [accepted warnings](#accepted-warnings), run with a real person's sign-in.
- The same four skips, which discovery explains.
- Basic reviews: `prompt-login` and `max-age-1` show the sign-in again. `ensure-registered-redirect-uri` and `ensure-request-object-with-redirect-uri` show *Invalid request*.
- Logout reviews: five bad requests show *Invalid request*, and three valid logouts show *Signed out*.

A person checked every review screenshot. The plan exports hold client secrets, so only counts are recorded.

## Review is a screenshot handed to a person

Twelve modules end at a page the suite cannot see: the issuer must refuse, so it never redirects back. A person checked every screenshot on 2026-10-20, and again on 2026-10-21 for the second installation, and judged that it shows the page the module requires.

| What the step demands | Modules | What the screenshot shows |
|---|--:|---|
| an error page, the `redirect_uri` is not registered | 2 | *Invalid request* and a reference |
| an error page, the `id_token_hint` is not valid | 2 | *Invalid request* and a reference |
| an error page, the `post_logout_redirect_uri` is not registered | 3 | *Invalid request* and a reference |
| the successful logout page | 3 | *Signed out*, after the driver presses the button on *Sign out?* |
| the login prompt during a second authorization | 2 | the sign-in chooser |

A review with the wrong evidence passes silently. The v1.77.1 check caught three of *Sign out?*.

## Skipped means the discovery document is truthful

Three modules skip because `scopes_supported` omits `address` and `phone`: `oidcc-scope-address`, `-phone` and `-all`. One skips because the issuer answers `request_not_supported` to a request object, which the module permits. [What is not served](not-served.md) says why.

## Accepted warnings

Four Basic modules end in WARNING, each with a Google account. Each warning is a choice or a missing source.

| Module | Suite message | Why it stays |
|---|---|---|
| `oidcc-scope-email`, `oidcc-alternate-happy-flow` | an `email` in the ID token that the request did not ask for | The ID token keeps `email` and the names for their scopes, besides userinfo, even when an access token is issued |
| `oidcc-scope-profile` | userinfo lacks scope claims (OIDC Core 5.4) | Ten `profile` claims have no source and are never invented |
| `oidcc-claims-essential` | `name` not found in userinfo (OIDC Core 5.5) | The `claims` request parameter is not supported, and discovery does not advertise it |

Many relying parties read `email` and `name` from the ID token, so sluis keeps them there. Core 5.4 expects them in userinfo alone once an access token is issued, so the suite warns.

Userinfo returns `name`, `given_name`, `family_name`, `preferred_username`, `email`, `email_verified`, `sub` and `groups`, each under its scope. No source holds `birthdate`, `gender`, `zoneinfo`, `locale`, `picture`, `website`, `profile`, `nickname`, `middle_name` or `updated_at`.

Upstream zitadel/oidc has not released the `client_id` fix ([issue 1020](https://github.com/zitadel/oidc/issues/1020), [pull request 1021](https://github.com/zitadel/oidc/pull/1021)). Until it does, sluis builds a patched copy, `third_party/zitadel-oidc`, through a `replace` in `go.mod`. Dropping either earlier brings the `oidcc-server` warning back.

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
