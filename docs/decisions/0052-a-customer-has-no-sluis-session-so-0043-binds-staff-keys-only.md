# 0052 — A customer has no sluis session, so 0043 binds the keys of staff and operators only

**Status:** Accepted
**Layer:** add-on
**Date:** 2026-11-22

Proposal: [issue #1719](https://github.com/truvity/sluis/issues/1719).

## Context

[0043](/decisions/0043-a-person-binds-their-own-keys-to-their-identity-in-sluis/) is a proposal, and nothing in it is built. Its section "Customers" lets a customer bind a key inside a company's tenant. It assumes that the customer signs in to sluis through the upstream identity provider: binding "needs a fresh sign-in at the customer's upstream", the customer ends a lost key "with a fresh re-authentication at their upstream", and the rationale of the rows leans on REQ-SESS-036 and REQ-SESS-037.

[0051](/decisions/0051-exchange-targets-resource-clients-only-and-policy-decides-who-gets-what/) decided that only staff and operators sign in to sluis. A customer's token from the group's identity provider enters sluis only as the subject of an exchange, and a customer gets no sluis sign-in session. It withdrew REQ-SESS-035, REQ-SESS-036 and REQ-SESS-037. So the customer path of 0043 has no way in: there is no session for the customer to bind from, no sign-in to make fresh, and no sluis step-up to ask for.

The staff and operator parts of 0043 do not depend on any of this. They bind keys after a hardware sign-in that sluis itself verified ([0017](/decisions/0017-identity-and-sign-in/), [0018](/decisions/0018-sign-in-sessions/)), and 0051 leaves that sign-in as it was.

## Decision

- **0043 binds the keys of staff and operators only.** A customer has no sluis session, so a customer cannot bind a key through a sluis sign-in. Customer key binding is out of the scope of 0043 until a later record brings it back through the exchange.
- **Staff and operators are unchanged.** Everything in 0043 outside the parts named below stands as it is written, including the classes, the gate, the challenge, the OpenPGP certification, the staff keyring and the life of a binding.
- **A later record may bring customer binding back** through the exchange, that is, by a request that carries the customer's token as the subject token of an exchange. Until such a record is accepted, nothing in 0043 lets a customer start a binding, and none of the customer rows is to be built.

### What this supersedes

The parts of 0043 that assume a customer signs in to sluis or holds a sluis session:

- **In "Customers", the sentence "The customer signs in at the customer upstream ([0032](/decisions/0032-customer-identity-installations/))."** A customer does not sign in to sluis.
- **In "Customers", the clause of the bullet "It is optional" that says the rule "agrees with [REQ-SESS-037](/requirements/sess/#REQ-SESS-037): no factor is enrolled for a person who signs in through a customer upstream".** That requirement is withdrawn. The rest of the bullet stands: a bound key is a signing key and never a sign-in factor.
- **In "Customers", the whole bullet "Binding needs a fresh sign-in at the customer's upstream".** It is the customer's fresh sign-in and the step-up for customers, with the `acr` and `max_age` that policy asked for ([REQ-SESS-036](/requirements/sess/#REQ-SESS-036)), and it goes.
- **In "Customers", in the bullet "Every binding of a customer is gated", the clause "so a stolen customer session cannot add a key" and the sentence "A customer ends a lost key with a fresh re-authentication at their upstream; ending is the safe direction, because binding again needs the tenant admin."** There is no customer session to steal and no customer re-authentication to ask for.

The requirement rows that follow from these parts are listed under Consequences.

## Consequences

- A customer has no path in 0043 to start a binding, extend one, change one or end one. The other bullets of "Customers" (the JWK, the shape of what a customer signs, the algorithms, the audit record, the approval by a tenant admin) are left as written, because they say how a customer's key is checked and not how a customer signs in. They have no entry until a later record gives them one, and they are not scheduled.
- The requirement rows REQ-KEYS-184, REQ-KEYS-185 and REQ-KEYS-204 assume a customer sign-in: the use of a bound key to sign in, a re-authentication at the upstream before a binding, and a customer's ending of a binding after one. They are withdrawn and keep their ids, and `superseded_by` names REQ-CIAM-020, the row that says a customer's token is only the subject of an exchange. REQ-KEYS-183 and REQ-KEYS-203 stay and lose their words about a customer's sign-in and session.
- The other customer rows of the keybinding area (REQ-KEYS-180, 181, 182, 186 to 189, 191, 217 to 221 and 227) stay `planned` in the backlog. They describe how a customer's key is verified and approved, and they wait for the record that brings a customer entry back.
- 0043 stays `Proposed`. Its status line says that it is partly superseded by this record.
- The decisions README allows the status `Proposed; partly superseded by NNNN`, and the schema of the site's data accepts it. A proposal can have a part replaced before it is accepted, and its status says so.

## Roads not taken

- **Edit 0043 in place.** A record is append-only, so the new record names the parts and 0043 keeps its text.
- **Supersede the whole of "Customers" and withdraw every customer row.** The way a customer's key is verified and approved does not depend on a sign-in, and a later record can reuse it behind an exchange.
- **A sluis sign-in for customers, only to bind keys.** It is the second sign-in that 0051 turned down: the group's identity provider already holds the customer's session.
- **Bind a customer's key from the customer's token alone, with no sluis step.** It would skip the freshness and the tenant admin's approval that the gate asks of every binding, and the exchange is a better place for a later record to put them.
- **Withdraw 0043 as a whole.** The staff and operator design does not depend on customers.

## Follow-ups

- Build the staff and operator binding of 0043, as that record says ([#1719](https://github.com/truvity/sluis/issues/1719)).
- Open: whether and how a customer's key is bound through the exchange. It needs its own record, and no issue asks for it yet.
