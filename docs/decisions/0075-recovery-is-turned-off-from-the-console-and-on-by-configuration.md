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
   - **established by the identity provider**: a gateway's forwarded identity, the hub's own directory or OIDC sign-in,
     or the issuer's browser sign-in made through a provider. A recovery sign-in (the console's cookie or the issuer's
     sign-in made by recovery), a workload and an AWS role are refused. A break-glass session does not decide the
     break-glass door's fate;
   - **a person** (it has an address), and
   - **an operator over the installation** (`access.RoleOperator`, not a role over one workspace). It is the narrowest
     role this console has that already changes what signs people in: connecting a directory, registering the OAuth
     client and revoking sessions need it. Recovery itself grants exactly it. A viewer or a scoped operator may read
     nothing here and cannot act.

   A refused call is recorded as a denied `roster.recovery.disabled`.

3. **The switch is a State record that names the configuration generation it overrode.** The record `rec.recovery.off`
   (kind `recovery`, id `off`, in the oidc module's table, permanent) holds `{generation, at, by}`. Recovery is switched
   off while a record exists whose `generation` equals the document's `recovery.generation` (default `""`). A record for
   any other generation covers nothing.
   - A cold start, a redeploy or a rolled pod that reads the same document finds the record and stays off. A stale
     document cannot turn recovery back on.
   - An operator turns recovery back on by a deliberate, reviewed change of the document: `recovery.generation` to any
     other string. Nothing compares generations for order, so a revert to an old value is "on again" only if no record
     covers that value, and the record of the current one is replaced by the next disable.
   - A record that cannot be parsed counts as covering every generation, and a read of the State that fails counts as
     off. A door that cannot be checked stays shut.
   - The State must be able to keep the record: DynamoDB or the memory adapter. The legacy adapter and a process with no
     State refuse `DisableRecovery` and say to set `recovery.enabled: false` instead, since a disable lost at the next
     restart would be a lie.
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
   - the issuer's recovery form, its post, and the completion of an authorization request made by recovery;
   - the issuer's browser sign-in made by recovery (ended when found, which is the first request that reads it);
   - any token minted for a recovery session: a code redeemed, a refresh (refused as `invalid_grant`, and the session
     revoked).

   After the switch the console also sweeps: `Issuer.EndRecovery` ends every sign-in and revokes every client session
   whose method is recovery, and counts them. The sweep is hygiene and the count; the gate is what makes the sessions
   unusable in every container, including one that took a sign-in while the switch was spreading. A relying party is not
   sent a back-channel logout and finds out at its next refresh. An access token already issued stays valid until it
   expires.

6. **Audit.** The action `roster.recovery.disabled` (catalogue 1.22.0) records the operator as the actor, the
   installation, the generation, and how many sign-ins and sessions ended. It is declared `block`, and the console tries
   it durably after the switch. If the trail cannot keep it the disable still stands, the failure is logged at ERROR,
   the record is queued through the ordinary path to be retried, and the response says the record is pending. A
   disable only reduces what can get in, so an outage of the trail must not stop an operator from closing the door;
   this is the opposite of a recovery sign-in, which is refused without its record. A repeat that finds recovery
   sessions to end is recorded with `repeat`; a repeat that finds none is not.

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
- **A TTL on the disable.** A door that opens again by itself is an enable.
- **Letting a recovery session disable recovery.** It is the session most likely to be a stolen or a stale credential,
  and the one least entitled to decide the fate of its own door.
- **Reading the record on every request.** One State read per request on every container for a rare event; the
  3-second cache bounds the lag at a cost of about one read per container per 3 seconds, and only on the requests that
  involve recovery.
- **Revoking only, without the gate.** A session created on another container while the switch spreads would survive. The
  gate is asked at use, so it cannot.
