# Turn off recovery

Close the break-glass sign-in from the console once a group grants operator to a real identity, and know how to open it again.

## Before you start

- Sign in through your identity provider. A recovery sign-in, a viewer, an operator of one directory and an address a gateway put in a header cannot turn recovery off.
- Check that a group grants `all:sluis:operator` to someone else too. After this, only a configuration change brings recovery back.
- The State must be DynamoDB. The legacy adapter refuses the action and tells you to set `recovery.enabled: false`.

## Steps

1. Open **Settings**. The *Recovery sign-in* section shows the state. The button appears only while recovery is on and you may press it.

2. Press **Disable recovery** and confirm. The page then says who turned it off and when.

   ```text
   Recovery disabled at 2026-10-18 20:05 UTC by ada@example.com; re-enable only via configuration.
   ```

3. To turn recovery on again, set a generation that was never disabled and deploy it. Reusing an old one keeps recovery off.

   ```yaml
   recovery:
     enabled: true
     generation: "2026-11"
   ```

## Verify

The sign-in page offers no *Recovery sign-in* within three seconds on every container, and a proof is refused. Recovery tokens stop at the console at once. Access tokens a resource server verifies offline live up to `lifetimes.token`, one hour by default. The trail has `roster.recovery.disabled` with the counts of sign-ins and sessions ended ([read the audit trail](read-the-audit-trail.md)).

## Roll back

There is no button for it. A redeploy of the same configuration keeps recovery off. A new `recovery.generation` turns it on. If the page says the state is unreadable, press the button again, then name a new generation. If the trail was down, the page says the record is pending and the service logs an ERROR.

## Decided in

[ADR 0075](../../../decisions/0075-recovery-is-turned-off-from-the-console-and-on-by-configuration.md).
