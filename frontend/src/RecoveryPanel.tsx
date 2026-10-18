import { useState } from "react";
import Alert from "@mui/material/Alert";
import Button from "@mui/material/Button";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";

import { access, at, type Me } from "./api";
import { useAsync } from "./hooks";
import { disabledNotice, recoveryHeadline, showDisable, supersededNotice, withheldReason, type Recovery } from "./recoveryModel";
import { ConfirmDialog, Failure, Mono, Section } from "./ui";

/** The recovery sign-in: whether it is on, and the one thing the console can do
 *  about it, which is turn it off (docs/decisions/0075).
 *
 *  There is no button to turn it on, here or anywhere: that is a change of
 *  `recovery.generation` in the configuration, so the console an attacker with
 *  a stolen session reaches cannot open the break-glass door. The button is
 *  shown to an operator who signed in through the identity provider while
 *  recovery can be used; the server refuses anyone else on its own. */
export function RecoveryPanel({ me, operator }: { me?: Me; operator: boolean }) {
  // Only an operator may read it; anyone else gets nothing here, not an error.
  const state = useAsync<Recovery | undefined>(async () => {
    if (!operator) return undefined;
    const got = await access.getRecovery({});
    return {
      configured: got.configured,
      effective: got.effective,
      kind: got.kind,
      disabledAt: at(got.disabledAt),
      disabledBy: got.disabledBy,
      supersededAt: at(got.supersededAt),
      supersededBy: got.supersededBy,
      generation: got.generation,
      durable: got.durable,
      canDisable: got.canDisable,
      cannotDisableReason: got.cannotDisableReason,
    };
  }, [operator]);
  const [confirming, setConfirming] = useState(false);
  const [done, setDone] = useState<string | undefined>();

  const current = state.value;
  const headline = recoveryHeadline(current);
  if (!operator || (!headline && !state.error)) return null;

  const disable = async () => {
    const got = await access.disableRecovery({});
    const stamp = at(got.disabledAt);
    setDone(
      `${stamp ? disabledNotice(stamp, got.disabledBy) : "Recovery disabled; re-enable only via configuration."}` +
        ` ${got.signInsEnded + got.sessionsEnded} recovery sessions ended.` +
        (got.auditPending ? " The audit record could not be written yet and is being retried." : ""),
    );
    setConfirming(false);
    state.reload();
  };

  const why = withheldReason(current, operator, me?.source);
  const superseded = supersededNotice(current);

  return (
    <Section
      title="Recovery sign-in"
      hint="the way in on the day the identity provider is broken; it can be turned off here, never back on"
      action={
        showDisable(current, operator, me?.source) ? (
          <Button size="small" variant="outlined" color="warning" onClick={() => setConfirming(true)}>
            Disable recovery
          </Button>
        ) : undefined
      }
    >
      <Failure error={state.error} />
      <Paper variant="outlined" sx={{ p: 2 }}>
        <Stack spacing={1}>
          <Typography variant="body2">{headline}</Typography>
          {current?.disabledAt ? (
            <Typography variant="caption" color="text.secondary">
              To turn it back on, change <Mono>recovery.generation</Mono> in the configuration (now <Mono>{current.generation || "unset"}</Mono>) and deploy it. A redeploy of the same
              configuration keeps it off.
            </Typography>
          ) : null}
          {why ? (
            <Typography variant="caption" color="text.secondary">
              {why}
            </Typography>
          ) : null}
          {superseded ? (
            <Typography variant="caption" color="text.secondary">
              {superseded}
            </Typography>
          ) : null}
        </Stack>
      </Paper>
      {done ? (
        <Alert severity="success" sx={{ mt: 1 }}>
          {done}
        </Alert>
      ) : null}
      {confirming ? (
        <ConfirmDialog title="Disable recovery for this installation?" confirm="Disable recovery" danger run={disable} onCancel={() => setConfirming(false)}>
          Recovery stops working at once, everywhere: the sign-in page stops offering it, a proof is refused, and every session recovery opened ends, refresh tokens included. Nobody can turn
          it back on from this console: that is a change of <Mono>recovery.generation</Mono> in the configuration. Make sure a group in your identity provider grants operator before you
          do this. It is recorded in the audit trail under your name.
        </ConfirmDialog>
      ) : null}
    </Section>
  );
}
