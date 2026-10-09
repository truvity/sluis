# What did the OpenID conformance run find?

sluis targets four OpenID Foundation profiles and claims a profile only when the suite says so. To produce a run, follow [run conformance](../../guides/sluis/run-conformance.md).

The last run was on 2026-09-12 against the deployed issuer at v1.0.0, with the suite running in the cluster (plans `xywkLCaSn66Wo`, `QQw1OfSOykbnC`, `Ps1nixuqZlBQp`, `6hKMyvxwF8baj`).

| Profile | Passed | Review | Skipped | Warning | **Failed** |
|---|--:|--:|--:|--:|--:|
| [Config OP](https://openid.net/certification/connect_op_testing/) | 1 | 0 | 0 | 0 | **0** |
| [Basic OP](https://openid.net/certification/connect_op_testing/) | 21 | 4 | 4 | 6 | **0** |
| [RP-Initiated Logout OP](https://openid.net/certification/connect_op_logout_testing/) | 3 | 8 | 0 | 0 | **0** |
| [Back-Channel Logout OP](https://openid.net/certification/connect_op_logout_testing/) | 2 | 0 | 0 | 0 | **0** |

Passed, review, warning and skipped all count. Only failed or interrupted disqualifies a profile. All four are certifiable. The Foundation requires RP-Initiated Logout plus one other logout profile for a submission, and that pair is green.

## Review is a screenshot handed to a person

Twelve modules end at a page the suite cannot see, because the issuer must refuse and so never redirects back. Each asks a person to confirm the screenshot. All twelve were reviewed on 2026-09-12.

| What the step demands | Modules | What the screenshot shows |
|---|--:|---|
| an error page, the `redirect_uri` is not registered | 2 | *That sign-in request was not valid* |
| an error page, the `id_token_hint` is not valid | 2 | *That sign-out request was not valid: id_token_hint invalid* |
| an error page, the `post_logout_redirect_uri` is not registered | 3 | *That sign-out request was not valid: post_logout_redirect_uri invalid* |
| the successful logout page | 3 | *Signed out* |
| the login prompt during a second authorization | 2 | the sign-in chooser |

Check the screenshots, because a review whose evidence is wrong passes silently. The review has caught two pages that were wrong and ten screenshots of the wrong page, from a driver fault.

## Skipped means the discovery document is truthful

Three modules skip because `scopes_supported` omits `address` and `phone`. One skips because the issuer answers `request_not_supported`. The suite read discovery, believed it, and skipped.

## Warnings are mostly personal data sluis does not hold

Four warnings say userinfo omits claims that the `profile` and `email` scopes permit. A real Google sign-in returned `sub`, `name`, `given_name`, `family_name`, `preferred_username`, `email`, `email_verified` and `groups`.

Missing are `birthdate`, `gender`, `zoneinfo`, `locale`, `picture`, `website`, `profile`, `nickname`, `middle_name` and `updated_at`. The service has no source for them.

The other two warnings come from one module noting that the ID token carries claims the client did not request by scope. One is `groups`, which Kargo and ArgoCD read, so gating it behind a scope would break every consumer. The other is `client_id`, which duplicates `azp` and comes from the OpenID library's claims struct.

## Back-channel logout needs the suite in the cluster

The Back-Channel module is the one place where the issuer must reach the suite, because a logout token is a server-to-server POST. A suite on a laptop resolves to the pod's own loopback, and pods cannot reach the tailnet.

The suite therefore runs in the cluster, at a hostname of its own, while the conformance client rows are declared. Only its relying-party paths are public. See [run conformance](../../guides/sluis/run-conformance.md) and [back-channel logout](back-channel-logout.md).

## How a run happens

A person runs the whole suite at every major and minor release. One profile run alone reads as passing while the two profiles that sign somebody in have not run.

The Config workflow ([`conformance.yaml`](../../../.github/workflows/conformance.yaml)) stays one click away. It needs no credential and guards the discovery document.

[`hack/conformance_drive.py`](../../../hack/conformance_drive.py) drives headless Chrome through the browser half: it signs in through recovery and answers the manual steps with a screenshot. Thirty-five modules are one command. Pass `--manual` to hand one module's browser step to a person, for example to check claims with a real account.
