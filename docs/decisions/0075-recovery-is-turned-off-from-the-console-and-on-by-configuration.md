# 0075 — Recovery is turned off from the console and turned on by configuration

**Status:** Accepted (2026-10-18). Refines [0036](0036-configuration-is-immutable-per-instance.md) (configuration is
immutable and State is read live: this is a decision kept in State that the configuration overrides only by naming a new
generation). Applies [0027](0027-the-state-port-nats-jetstream-and-dynamodb.md) (the State port keeps the record).
**Date:** 2026-10-18

## Context

Recovery is the sign-in that needs no directory: a ServiceAccount token proven against the API server in a cluster, a
generated password anywhere else. It exists for the day the identity provider cannot vouch for anybody, and it is an
operator by construction. Once a directory works it is a standing way in that bypasses the directory, and the
[setup checklist](../guides/sluis/operate/day-one.md) tells an operator to turn it off.

Today that takes a change of `recovery.enabled`, a review, a render and an apply. Until then the door stays open, and the
people best placed to close it (an operator signed in through the identity provider, looking at the checklist) cannot.

The owner's request was a button, in the console, that turns recovery off, available only to an operator who signed in
through the identity provider, with no button to turn it on.

Three constraints shape the rest:

- **Configuration is immutable per execution environment** ([0036](0036-configuration-is-immutable-per-instance.md)). A
  runtime switch cannot be a field of the document, and a Lambda cold start or a redeploy reads the same document again.
  A switch kept only in a process would be undone by the next one.
- **The console and the issuer are one binary on many containers** ([0071](0071-least-privilege-modules-one-credential-holder-per-function.md)):
  a disable that one container knows about is not a disable.
- **A button that opens the break-glass door is the thing an attacker with a stolen operator session wants.** The act
  must only ever reduce what can get in.

## Decision

1. **One action, and it only closes.** The console's `AccessService` gains `GetRecovery` (state) and `DisableRecovery`.
   There is no enable action in the UI, the API or `sluisctl`. Turning recovery on is `recovery.enabled: true` in the
   configuration, and, after a disable, a new `recovery.generation` (below).

2. **Who may call it is decided in the RPC, not by hiding the button.** The caller's identity must be:
   - **established by the identity provider**: a gateway's forwarded token, which the console verifies against the
     issuer's keys, the hub's own directory or OIDC sign-in, or the issuer's browser sign-in made through a provider. An
     address a gateway put in a header (`login.forwarded.emailHeader`) is its own source, `forwarded-header`, because
     the console cannot verify it; it may read the state and may not turn recovery off. A recovery sign-in (the console's cookie or the issuer's
     sign-in made by recovery), a workload and an AWS role are refused. A break-glass session does not decide the
     break-glass door's fate;
   - **a person** (it has an address), and
   - **an operator over the installation** (`access.RoleOperator`, not a role over one workspace). It is the narrowest
     role this console has that already changes what signs people in: connecting a directory, registering the OAuth
     client and revoking sessions need it. Recovery itself grants exactly it. A viewer or a scoped operator may read
     nothing here and cannot act.

   A refused call is recorded as a denied `roster.recovery.disabled`, once per caller and reason per minute with the
   count, so asking in a loop cannot flood the trail.

   This stops an accident and a stolen recovery session. It does not stop a determined recovery operator: recovery
   is an operator, so it can connect a directory or register a client, sign in through it, and then press the
   button. The rule keeps the decision off the break-glass session; it does not make the break-glass session unable
   to cause it.

3. **The switch is a State record holding the set of disabled generations.** The record `rec.recovery.off` (kind
   `recovery`, id `off`, in the oidc table, permanent) holds `{disabled: [{generation, at, by}, ...]}`. Recovery is off
   while the document's `recovery.generation` (default `""`) is in the set.
   - A cold start, a redeploy or a rolled pod that reads the same document finds its generation and stays off. A stale
     document cannot turn recovery back on.
   - **The set only grows.** A disable adds the caller's generation by compare-and-swap and rewrites no entry.
     Generations are strings with no order, so a container still on generation A during a rolling deploy that is
     asked to disable adds A and leaves B off: nothing it does can re-enable B.
   - An operator turns recovery back on by a deliberate, reviewed change of the document: `recovery.generation` to a
     string that was never disabled. Reusing one that was keeps recovery off. The page shows the latest earlier
     disable as overridden.
   - A record that cannot be parsed covers every generation. A disable replaces it with a readable record of the
     one generation, which is the only way to narrow it, so re-enabling from an unreadable record is a disable and
     then a new generation.
   - A read that fails refuses recovery and destroys nothing: no session is ended, no cookie cleared, no refresh
     token revoked on an error. Only a record that was read ends a session.
   - The State must be able to keep the record: DynamoDB or the memory adapter. If its `Get` answers that the key is
     unsupported (the legacy adapter), the gate treats recovery as never off and the action as unavailable: it is a
     State with no switch, not a lockout. `DisableRecovery` refuses and says to set `recovery.enabled: false`, since a
     disable lost at the next restart would be a lie.
   - The record is neither carried by a backup nor written by a restore (the kind `recovery` is regenerated, like
     `maintenance`): restoring an older archive does not open a door an operator closed.

4. **Every container sees it within seconds, and the process that threw it at once.** A gate reads the record when it is
   asked whether recovery is off and trusts "on" for 3 seconds ([`recoverygate.DefaultTTL`](../reference/sluis/keys.md)).
   Once it has seen the record it never reads again. So a Lambda container that was warm when an operator pressed the
   button refuses recovery within 3 seconds of its next request, and the container that took the call refuses it at once.
   A reader costs one State read per container per 3 seconds, only on requests that involve recovery.

5. **Recovery ends everywhere it is used.** The gate is asked at each place recovery is read:
   - the console's recovery page (not offered) and its form post (refused, recorded);
   - the console's own recovery session (a signed cookie, so there is no table to find it in): `ConsoleServer.principal`
     treats a recovery principal as no principal while recovery is off, and clears the cookie;
   - a verified bearer at the console that was minted under a recovery sign-in. The issuer marks such an access token
     with the private claim `sluis_recovery`, `identity.Verified.Recovery` reads it, and the console refuses it while
     recovery is off. Without this a token minted before the switch would keep its authority until it expired. The
     issuer's own bearer readers (`sluisctl`, the session service) and the sign-in exchange never accept a recovery
     session's token at all;
   - the issuer's recovery form, post, and request completion;
   - the issuer's browser sign-in made by recovery (ended when found, which is the first request that reads it);
   - any token minted for a recovery session: a code redeemed, a refresh (refused as `invalid_grant`, and the session
     revoked).

   After the switch the console also sweeps: `Issuer.EndRecovery` ends every sign-in and revokes every client session
   whose method is recovery, counts them, and tells the clients that declared a back-channel logout URI (one logout
   token per client and sign-in, including a client that was only ever issued an ID token). The sweep is hygiene,
   the count and the notification; the gate is what makes the sessions unusable in every container, including one that
   took a sign-in while the switch was spreading.

   **What remains after the switch**, stated with its bound: an access token already issued is valid until it expires
   wherever it is verified offline without this issuer's claim check (a resource server that reads the JWT and
   nothing else): at most `lifetimes.token`, 1 hour by default. A relying party's own cookie session lasts as long as
   that application keeps it unless the client declares `backchannel_logout_uri`; a client that does not finds out at its
   next refresh, which is refused. The console, the issuer and the session service are closed at once.

6. **Audit.** The action `roster.recovery.disabled` (catalogue 1.22.0) records the operator as the actor, the
   installation, the generation, and how many sign-ins and sessions ended. It is declared `block`, and the console tries
   it durably after the switch. If the trail cannot keep it the disable still stands, the failure is logged at ERROR,
   the record is queued through the ordinary path to be retried, and the response says the record is pending. A
   disable only reduces what can get in, so an outage of the trail must not stop an operator from closing the door;
   this is the opposite of a recovery sign-in, which is refused without its record. A repeat that finds recovery
   sessions to end is recorded with `repeat`; a repeat that finds none is not.

7. **It works while a restore runs.** `DisableRecovery` is exempt from the console's maintenance interceptor, which
   refuses writes while the oidc table is being restored: closing the break-glass door only reduces what can get in,
   and a restore never writes the record.

## Consequences

- **Consumers.** The configuration gains one optional key, `recovery.generation` (a string). An installation that never
  sets it behaves as before until an operator presses the button; after that the key is how it re-enables. The Pulumi
  library and `sluisctl render` pass `recovery` through, so the key reaches the document without a library change;
  the estate that wants the knob in its installation file renders it there. Nothing is required of a consumer to adopt
  this release.
- **The IAM** of the issuer's role is unchanged: it already writes its own table, and the library's deny covers the
  `maintenance` kind only.
- **Re-enabling is a deploy.** That is the point, and it costs a review. The runbook says it plainly; the console says
  "re-enable only via configuration".
- **What it does not cover.** An operator account stolen from the identity provider can close the door. That removes
  break-glass when the owner may need it, and is recoverable only by a configuration change; this is the price of a
  button that cannot open anything, and the audit trail names who pressed it. A restore of the whole table from a
  point-in-time backup of DynamoDB (not a sluis backup) brings the record back as it was. Access tokens already issued
  to relying parties outlive the switch by their lifetime. The legacy State adapter cannot keep the record.
- **Cost.** The sweep walks every session of the installation once; it is an act an operator performs once.

## Alternatives considered

- **A field in the document the console rewrites.** [0028](0028-nothing-writes-configmaps-or-secrets.md) says nothing
  writes ConfigMaps, and on Lambda a layer is immutable. Rejected.
- **A flag with no generation (a disable that wins until someone deletes the record).** Re-enabling would then be an
  operator action outside the configuration, an enable in all but name, and a stale document could never re-enable
  but a deliberate one could not either without touching the State by hand. The generation puts "on" back in the one
  place a reviewed change is made.
- **A digest of the whole `recovery` block as the generation.** Any edit of the block (an audience, a service account)
  would silently turn recovery back on. A named generation changes only when someone means it to.
- **One record per generation overwritten by the latest disable.** Generations have no order, so a disable from a
  container on an older generation would replace a newer one and turn it back on. Rejected for the set.
- **A TTL on the disable.** A door that opens again by itself is an enable.
- **Letting a recovery session disable recovery.** It is the session most likely to be a stolen or a stale credential,
  and the one least entitled to decide the fate of its own door.
- **Reading the record on every request.** One State read per request on every container for a rare event; the
  3-second cache bounds the lag at a cost of about one read per container per 3 seconds, and only on the requests that
  involve recovery.
- **Revoking only, without the gate.** A session created on another container while the switch spreads would survive. The
  gate is asked at use, so it cannot.
