# Turn off recovery

Close the break-glass sign-in from the console once a group grants operator to a real identity, and know how to open it again.

## Before you start

- Sign in through your identity provider. A recovery sign-in cannot turn recovery off, and neither can a viewer or an operator of one directory.
- Check that a group grants `all:sluis:operator` to someone else too. After this, only a configuration change brings recovery back.
- The State must be DynamoDB. The legacy adapter refuses the action and tells you to set `recovery.enabled: false`.

## Steps

1. Open **Settings**. The *Recovery sign-in* section shows the state. The button appears only while recovery is on and you may press it.

2. Press **Disable recovery** and confirm. The page then says who turned it off and when.

   ```text
   Recovery disabled at 2026-10-18 20:05 UTC by ada@example.com; re-enable only via configuration.
   ```

3. To turn recovery on again, set a new generation in the configuration and deploy it. Any other string works.

   ```yaml
   recovery:
     enabled: true
     generation: "2026-11"
   ```

## Verify

The sign-in page offers no *Recovery sign-in* within three seconds on every container, and a proof is refused. The trail has `roster.recovery.disabled` with the counts of sign-ins and sessions ended ([read the audit trail](read-the-audit-trail.md)).

## Roll back

There is no button for it. A redeploy of the same configuration keeps recovery off. A new `recovery.generation` turns it on and the page says the earlier turn-off was overridden. If the trail was down, the page says the record is pending and the service logs an ERROR.

## Decided in

[ADR 0075](../../../decisions/0075-recovery-is-turned-off-from-the-console-and-on-by-configuration.md).
