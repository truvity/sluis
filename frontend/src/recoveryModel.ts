/** The recovery sign-in, as the Settings page tells it (docs/decisions/0075).
 *
 *  The console can turn recovery OFF, once, and has no control that turns it
 *  on: that is a change of `recovery.generation` in the configuration. The
 *  server decides who may press the button (`canDisable`); this file decides
 *  what the page says and shows, so the rules are one place and a test. */
export type Recovery = {
  /** the configuration builds a recovery sign-in at all */
  configured: boolean;
  /** it can be used now */
  effective: boolean;
  /** "token", "password", or empty */
  kind: string;
  disabledAt?: Date;
  disabledBy: string;
  /** an earlier turn-off the configuration has since overridden */
  supersededAt?: Date;
  supersededBy: string;
  generation: string;
  /** this installation can keep a turn-off */
  durable: boolean;
  /** the caller may turn it off from this sign-in */
  canDisable: boolean;
  cannotDisableReason: string;
};

/** How the sign-in that made this page's requests was established, from
 *  whoami's `source`: "recovery" is the break-glass sign-in. */
export const recoverySource = "recovery";

/** "2026-10-18 20:15 UTC", the way every time on this console is read back. */
export function whenText(when: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${when.getUTCFullYear()}-${pad(when.getUTCMonth() + 1)}-${pad(when.getUTCDate())} ${pad(when.getUTCHours())}:${pad(when.getUTCMinutes())} UTC`;
}

/** Whether the "Disable recovery" button is shown.
 *
 *  All of: recovery can be used now (there is something to turn off), the
 *  viewer is an operator, and the viewer did NOT sign in by recovery. The
 *  server refuses the call on its own account; hiding the button here only
 *  spares a person a refusal, and `canDisable` is the server's word for it. */
export function showDisable(state: Recovery | undefined, operator: boolean, source: string | undefined): boolean {
  if (!state || !operator || source === recoverySource) return false;
  return state.configured && state.effective && state.canDisable;
}

/** The sentence shown after a turn-off, and on the page ever after. */
export function disabledNotice(at: Date, by: string): string {
  return `Recovery disabled at ${whenText(at)} by ${by}; re-enable only via configuration.`;
}

/** The state in a line, or undefined when there is nothing to say (recovery
 *  is not enabled on this installation at all). */
export function recoveryHeadline(state: Recovery | undefined): string | undefined {
  if (!state || !state.configured) return undefined;
  if (state.disabledAt) return disabledNotice(state.disabledAt, state.disabledBy);
  if (!state.effective) return "Recovery cannot be used right now: the state that keeps its switch could not be read, so it is refused until it can be.";
  return state.kind === "password" ? "Recovery is on: a password signs in without the identity provider." : "Recovery is on: proof of access to the cluster signs in without the identity provider.";
}

/** Why the button is not offered to an operator who is looking, when there is
 *  a reason worth saying; undefined when it is offered or there is nothing to
 *  turn off. */
export function withheldReason(state: Recovery | undefined, operator: boolean, source: string | undefined): string | undefined {
  if (!state || !operator || !state.configured || !state.effective) return undefined;
  if (showDisable(state, operator, source)) return undefined;
  if (source === recoverySource) return "You signed in with recovery. Sign in through your identity provider to turn it off.";
  return state.cannotDisableReason || undefined;
}

/** What an earlier turn-off says once the configuration has overridden it. */
export function supersededNotice(state: Recovery | undefined): string | undefined {
  if (!state || !state.supersededAt || state.disabledAt) return undefined;
  return `Recovery was turned off at ${whenText(state.supersededAt)} by ${state.supersededBy}; the configuration has since named another generation (${state.generation || "none"}), which turned it back on.`;
}
