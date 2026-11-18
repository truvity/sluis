## Scope

A config kind `ExchangeTrust` declares the one upstream issuer that an installation trusts for exchange: a realm of the group's single sign-on service, the realm of the installation's boundary. It names the issuer, how its key set is found, the audiences a subject token may carry, and the mapping from the upstream token's claims to `customer`, `org` and `product` ({{ref:52}}).

- One installation serves one boundary and trusts one realm. A token of another realm is refused.
- The kind is registered with its checks (kind registry), has an example in the bootstrap documents and is rendered into the service configuration.
- Done when a document for the test realm loads and a token of a second realm is refused, with a test.

Decision: ADR 0045 (accepted 2026-11-12); ADR 0022 (the kind). Requirement: REQ-EXCH-027. Related: #782, #785 and #961 stay in Core: features (the exchange actor and the grant).

## Decision codes

D1 B, D3, D7 A (ADR 0045); maintainer decision of 2026-11-18: Customer identity comes right after Core.

## Milestone

Customer identity

Linear: mirrored.
