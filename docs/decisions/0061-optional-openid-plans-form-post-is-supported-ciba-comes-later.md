# 0061 — Optional OpenID plans: form post is supported; front-channel logout and third-party-initiated login are not; CIBA comes later in poll mode

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). The question was [issue #928](https://github.com/truvity/sluis/issues/928).

## Context

The OpenID Foundation publishes test plans beyond the four that v1 passes (Config, Basic, RP-Initiated Logout and Back-Channel Logout). Four of them cover optional features: form post, third-party-initiated login, front-channel logout and CIBA. Until now the [conformance page](/standards/conformance/) listed them as "decision pending".

sluis's rule is that discovery is truthful and a plan is claimed only when it passes ([Conformance](/standards/conformance/)). Each optional plan therefore needs one of two answers: supported, and the plan must pass; or not served, with the reason written down so that nobody infers support from silence.

Some facts bear on the answers.

- **v1 serves the query response mode only.** The authorization code travels in the redirect URL, where it can reach browser history, logs and a `Referer` header. PKCE makes a leaked code useless without the verifier, but some security profiles and enterprise relying parties still ask for form post.
- **Front-channel logout signs relying parties out through hidden iframes.** Browsers that block third-party cookies make it unreliable. Back-channel logout, server to server, is already the supported path, and v1 passes its plan.
- **Third-party-initiated login lets the provider start a sign-in at a relying party** through the client's registered `initiate_login_uri`. Its main use would be an app launcher in the console. There is no launcher today.
- **CIBA lets a service ask a person to approve a sign-in on another device**, as a call centre or a banking app does. FAPI-CIBA is its banking profile. FAPI 2.0 comes after v2 parity, and device login (RFC 8628) is postponed.
- **Passkeys are planned** ([0048](/decisions/0048-passkeys-step-up-and-device-login/)). A passkey on the approving device is the strongest approval sluis has.

## Decision

### Form post (OP1)

| | |
|---|---|
| Answer | Supported, for the authorization code flow |
| Conformance plan | Form Post OP, joins the programme |
| Discovery | `response_modes_supported` lists `query` and `form_post` |
| When | v2, after the first-wave plans |

- **The authorization endpoint serves `response_mode=form_post` for the code flow.** The response is an auto-submitting HTML form that posts the code and `state` to the registered redirect URI.
- **The auto-submit page carries a strict Content-Security-Policy with a nonce.** Only its own inline script runs, by a nonce fresh for each response; `form-action` names the one redirect URI's origin; the page cannot be framed and is not cached.
- **A `response_mode` other than `query` and `form_post` is refused** with `invalid_request`.
- **The Form Post OP plan joins the conformance programme** and runs in the same pipeline as the first-wave plans.

### Third-party-initiated login (OP2)

| | |
|---|---|
| Answer | Not served now |
| Conformance plan | 3rd Party-Initiated Login OP, not run |
| Registration | A client's `initiate_login_uri` is not accepted |
| Revisit | If the console gets an app launcher |

- **sluis does not start sign-ins at relying parties.** A sign-in starts at the client.
- **The question comes back if the console gets an app launcher.** Then the answer would be registered clients only, with the plan run.

### Front-channel logout (OP3)

| | |
|---|---|
| Answer | Not supported |
| Conformance plan | Front-Channel Logout OP, not run |
| Discovery | Does not advertise front-channel logout; `backchannel_logout_supported` is true |
| The path | Back-channel logout, with RP-Initiated Logout |

- **Back-channel logout is the logout path for relying parties.** The logout submission for certification is RP-Initiated Logout plus Back-Channel Logout.
- **Discovery says so.** It does not advertise `frontchannel_logout_supported` or `frontchannel_logout_session_supported`, and a client registration that names a `frontchannel_logout_uri` is refused by the configuration check.

### CIBA (OP4)

| | |
|---|---|
| Answer | Later, poll mode only |
| Approval | A passkey on the approving device |
| Conformance plan | A CIBA plan for poll mode, confirmed against the suite when the flow ships |
| When | Not before v2 parity |

- **CIBA comes after v2 parity, in poll mode only.** The client polls the token endpoint; ping and push are not served.
- **The person approves with a passkey on the approving device.** A tap on a notification without a passkey assertion is not an approval.
- **Until then, discovery does not advertise CIBA** and its endpoint is not served.
- **It follows the other post-parity work:** FAPI 2.0 is after parity, and device login is postponed.

## Consequences

- **Relying parties that require form post can use sluis,** and the code stays out of URLs, history and `Referer` headers for those that choose it.
- **One more response page to keep safe.** The form post page is the only page sluis serves that runs a script on behalf of a client's response, so its CSP, nonce and headers get their own tests, and the plan's run proves the flow. (inferred)
- **A relying party that uses form post receives a cross-site POST.** A session cookie with `SameSite=Lax` is not sent with it, so the relying party keeps its `state` check without relying on that cookie. This is the relying party's concern, and the reference snippets say so. (inferred)
- **The existing records "query response mode only" and "any other response mode is refused" are replaced** in the requirements catalogue by the form post records.
- **Relying parties without a back-channel logout endpoint keep their own sessions until those expire** when a person signs out at sluis. That is the cost of not serving front-channel logout.
- **No app launcher in the console for now.** Starting an application from the console means opening its URL; the application then starts its own sign-in.
- **Banking-style customers that need CIBA wait until after parity.** When CIBA ships, it reuses the passkeys and the step-up of [0048](/decisions/0048-passkeys-step-up-and-device-login/), and a new conformance plan joins the pipeline.
- **The [standards register](/standards/), the [conformance page](/standards/conformance/) and the requirements catalogue follow this record.** The plans that run are tracked in issues: form post with its plan, and CIBA in poll mode after parity.

## Roads not taken

### Form post not supported, documented

- Pros: nothing to build or test. One response mode is the simplest surface.
- Cons: relying parties and profiles that require form post cannot use sluis. The code stays in URLs for everyone.
- Why not: the code is small, the plan exists, and the page's risk is contained by a strict CSP with a nonce.

### Form post for every response type

- Pros: one rule for every flow.
- Cons: the implicit and hybrid response types are not served, so there is nothing else to post.
- Why not: the code flow with PKCE is the only flow. Form post applies to it alone.

### Third-party-initiated login for registered clients now

- Pros: an app launcher in the console becomes possible, and its plan could join the programme.
- Cons: one more redirect path to guard against misuse, such as a login started at a client that the person did not choose. Rarely needed today.
- Why not: there is no launcher. The answer is revisited when one is proposed.

### Front-channel logout alongside back-channel

- Pros: covers relying parties that have no back-channel endpoint.
- Cons: hidden iframes depend on third-party cookies, which browsers block, so it fails silently. One more plan to keep green for a mechanism that does not work reliably.
- Why not: a logout path that silently does nothing is worse than a documented absence. Back-channel logout is the path.

### CIBA not planned at all

- Pros: no new flow and no new plan to maintain.
- Cons: services that need approval on another device, such as a call centre or a banking app, have no standard way to use sluis.
- Why not: passkeys make a strong approval cheap to add later. The flow is planned, after parity.

### CIBA in poll and ping modes

- Pros: the full profile, so clients that want a callback get one.
- Cons: the most to build and test. Ping needs a client notification endpoint that sluis calls, which is one more outbound path.
- Why not: poll mode covers the main use. Ping can be decided when a client needs it.

### CIBA before parity

- Pros: customers that need it get it sooner.
- Cons: a new flow competes with the parity work, and its banking profile depends on FAPI 2.0, which comes after parity.
- Why not: parity comes first. FAPI 2.0 comes after it too, and device login is postponed.

### CIBA approval by a notification tap

- Pros: the quickest approval for a person.
- Cons: a tap proves only that someone holds the device, and repeated prompts train people to approve them.
- Why not: an approval is a sign-in. It is a passkey assertion on the approving device.
