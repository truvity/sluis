## Scope

The sluis token gains three claims: `customer`, `org` and `product`. They are added to the fixed claim set (ADR 0020, amended).

- `exchange` sets them from the claims of the upstream token, through the mapping that the installation declares in its `ExchangeTrust` document ({{ref:50}}), and never from the request.
- The ID token and the access token both carry them.
- A token that did not come from an exchange of an upstream token, such as a staff sign-in at sluis, carries none of them; an absent claim stays absent.
- A wrong mapping is a configuration error of the installation and shows in its audit log.
- Done when the claims appear on both tokens after an exchange, and are absent after a staff sign-in.

Decision: ADR 0045. Requirements: REQ-AUTHN-112, REQ-TOKEN-045.

## Decision codes

D1 B, D3, D7 A (ADR 0045); maintainer decision of 2026-11-18: Customer identity comes right after Core.

## Milestone

Customer identity

Linear: mirrored.
