# 0039 — The issuer generates confidential client secrets

**Status:** Accepted; amended by [0041](0041-the-secret-contract.md) (proposed): the secret and its previous value live at `external/oidc/<client>`; extends [0038](0038-estates-render-through-sluis.md) (what an estate
declares for a client) and refines the client table of the policy document
**Date:** 2026-10-06

## Context

A confidential client's secret is always supplied by the operator. The policy row
names it (`secret`), the service reads it as the input `clients/<id>/secret`, and
the token endpoint compares the value presented with it as plain text
([policy-clients](../reference/sluis/policy-clients.md)). The console cannot set it
([rotate keys and credentials](../guides/sluis/operate/rotate-keys-and-credentials.md)), and
dynamic client registration is refused as "an endpoint that mints trust"
([what is not served](../concepts/sluis/not-served.md)).

In practice an operator generates each secret ad hoc outside the issuer and copies
the same value to two places: the issuer's input and the relying party's own
configuration. Nothing records that the two agree, and there is no rotation story:
changing one side before the other breaks every sign-in until the second catches up.

## Decision

**A client row may declare that the issuer generates its secret.**

1. **The row.** A confidential client may declare `secret: {generate: true}`. The
   client is still declared in git, reviewed and merged like any other row. This is
   not dynamic registration: nothing new is trusted at run time, and no endpoint
   accepts a registration.
2. **The issuer owns the credential.** The secret is stored as the issuer's own
   credential, `credentials/oidc-client/<id>/secret`, written create-only. It is 32
   random bytes, encoded base64url without padding.
3. **Adoption, not rotation.** When the issuer first sees a generated client and an
   input for it already exists, it adopts that value as the credential. Migrating a
   client from operator-supplied to generated never changes the secret in use.
4. **Export.** The issuer publishes `client-id` and `client-secret` through the
   Export port, where the relying party reads them with its own secret operator.
5. **Rotation with overlap.** The token endpoint accepts the current secret and,
   for an overlap window after a rotation, the previous one. Rotation is an operator
   action and is recorded in the audit trail. The relying party picks up the new
   value from the export and no sign-in fails in between.
6. **Removal is not purge.** Removing a client row keeps the credential and the
   export, reported as orphaned, until an operator purges them explicitly.
7. **Adapters.** The legacy Kubernetes-Secrets adapter has no create-only write.
   A policy that asks for generated secrets on it is refused at start.

Details of the fields, the window and the commands arrive with the code; until then
see this record.

## Consequences

- One store of truth for each secret, and a rotation that does not break logins.
- The issuer holds every relying party's secret. This is acceptable: whoever
  controls the issuer can already mint tokens for every relying party.
- Losing the secrets store loses the generated secrets. Recovery is a rotation, and
  the relying party reads the new value from the export.
- An older binary refuses the new policy shape, so a rollback past this change
  needs the row returned to `secret: <name>` first.
- Once a client is adopted, the operator should remove the old input; it is no
  longer read and would only be a second copy.

## Alternatives considered

- **Operator-generated secrets through an external generator** (a secret operator's
  generator plus a push, or a random value in infrastructure code). It works with no
  change to the issuer, but it needs two mechanisms, keeps the client list in two
  places, and cannot rotate with an overlap window.
- **`private_key_jwt` or public PKCE clients.** Preferred wherever the relying party
  supports them, and nothing here changes that. Many gateways' OIDC filters and
  consoles can only present a confidential secret, so the case remains.
