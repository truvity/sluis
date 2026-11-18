## Scope

Rate and abuse limits in `entry`: per-client and per-address limits on the token endpoint, the authorization endpoint and recovery.

- A limit per client and a limit per source address on `/token`, on `/authorize` and on the recovery routes, each with a window and a refusal that discloses nothing.
- The address is counted through the keyed hash made at key@ (REQ-SEC-027), so a window never holds an address in the clear.
- The limits are in signed config, with defaults, and a refusal is counted and audited as a denial.
- The measures that ADR 0036 places inside sluis; the Cloudflare edge rules stay in the installation's own code.
- Done when a flood on each endpoint is refused after its limit, a limit of one client does not refuse another, and each case has a test.

Decision: ADR 0036 (volumetric abuse). Related: #958 (the entry actor), #1215 (entry hardening and keyed rate windows), #796 (web surface hardening).

## Decision codes

Decision A2 and H7, 2026-11-18.

## Milestone

Core: features

Linear: mirrored.
