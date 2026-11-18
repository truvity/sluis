## Scope

`exchange` accepts a token that the trusted realm issued to a person as the subject token, and issues a sluis token for one audience. Today it refuses any subject token that is not its own.

- Verify the subject token against the issuer and key set of the `ExchangeTrust` document ({{ref:50}}): signature, issuer, audience, expiry. Refuse every other token with the same answer.
- The subject of the issued token is the person's sluis subject (ADR 0019), not the upstream subject.
- Exchange only narrows: audience, scope and lifetime are never wider than the subject token allows.
- Every exchange and every refusal is audited.
- Done when a token from the upstream test realm is exchanged for a sluis token on the local installation.

Decision: ADR 0045; phase P2 of the roadmap. Requirement: REQ-EXCH-027. Related: #782 (the grant), #785 (exchange controls) and #961 (the exchange actor) stay in Core: features, because the exchange of workload proofs needs them.

## Decision codes

D1 B, D3, D7 A (ADR 0045); maintainer decision of 2026-11-18: Customer identity comes right after Core.

## Milestone

Customer identity

Linear: mirrored.
