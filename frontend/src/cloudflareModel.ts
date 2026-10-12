import type { CloudflarePreset, CloudflarePrototype, CloudflareStored } from "./gen/sluis/v1/cloudflare_pb";
import type { StateKind } from "./ui";

/** What a prototype's chip shows: the shared chip's kind, a word after it,
 *  and the sentence in its tooltip. */
export type PrototypeView = { kind: StateKind; label: string; title: string; usable: boolean };

/** The prototype is the token whose rights every minted token copies, so the
 *  one state that matters is whether sluis will clone it. The server reads it
 *  live at each listing and at each mint. */
export function prototypeView(proto: Pick<CloudflarePrototype, "status" | "detail"> | undefined): PrototypeView {
  const detail = proto?.detail ? ` ${proto.detail}` : "";
  switch (proto?.status) {
    case "ok":
      return { kind: "ok", label: "disabled ✓", usable: true, title: "The prototype is disabled and grants nothing sluis refuses to clone." };
    case "active":
      return {
        kind: "refused",
        label: "ACTIVE",
        usable: false,
        title: `The prototype is active, so it works as a credential in its own right. sluis refuses to clone it until it is disabled.${detail}`,
      };
    case "forbidden":
      return {
        kind: "refused",
        label: "forbidden permission",
        usable: false,
        title: `The prototype grants a permission sluis never clones (token editing, billing, account settings, memberships, identity providers).${detail}`,
      };
    case "missing":
      return { kind: "failed", label: "missing", usable: false, title: `Cloudflare has no such token.${detail}` };
    default:
      return { kind: "failed", label: "not read", usable: false, title: `sluis could not read the prototype from Cloudflare.${detail}` };
  }
}

/** How a stored credential stands against its preset's rotation.
 *
 *  Rotation happens on a tick (the schedule on Lambda, a minute on
 *  Kubernetes), so a credential older than `rotation` is not late: it is due,
 *  and the next tick replaces it. It is overdue only past
 *  `rotation + tick + slack`, which a healthy rotation never reaches; `stale`
 *  is past twice the rotation, the alert's threshold.
 *
 *  - none: nothing stored yet
 *  - fresh: within one rotation
 *  - due: past the rotation, within one tick: waiting for the tick, nothing wrong
 *  - late: overdue, the tick should have replaced it
 *  - stale: not rotated for twice its rotation */
export type Freshness = "none" | "fresh" | "due" | "late" | "stale";

/** Slack on top of the tick: the time a pass takes, and the clocks of the
 *  scheduler and of the browser. */
export const tickSlackSeconds = 60;

/** The tick when the server does not say (an older server): the Kubernetes
 *  loop's, which is the finest one, so an unknown tick never hides a late
 *  rotation. */
export const defaultTickSeconds = 60;

export function tickOf(preset: Pick<CloudflarePreset, "rotationTickSeconds">): number {
  const tick = Number(preset.rotationTickSeconds);
  return tick > 0 ? tick : defaultTickSeconds;
}

export function freshness(
  preset: Pick<CloudflarePreset, "rotationSeconds" | "rotationTickSeconds">,
  stored: Pick<CloudflareStored, "present" | "mintedAt"> | undefined,
  now: Date,
): Freshness {
  if (!stored?.present || !stored.mintedAt) return "none";
  const age = ageSeconds(stored.mintedAt, now);
  const rotation = Number(preset.rotationSeconds);
  if (!(rotation > 0)) return "fresh";
  const overdue = rotation + tickOf(preset) + tickSlackSeconds;
  if (age > 2 * rotation && age > overdue) return "stale";
  if (age > overdue) return "late";
  if (age > rotation) return "due";
  return "fresh";
}

function ageSeconds(stamp: { seconds: bigint }, now: Date): number {
  return Math.max(0, Math.round(now.getTime() / 1000 - Number(stamp.seconds)));
}

/** The sentence that goes with a freshness, or empty when nothing needs saying. */
export function freshnessNote(f: Freshness, rotationSeconds: bigint | number, tickSeconds: bigint | number = defaultTickSeconds): string {
  const rotation = span(Number(rotationSeconds));
  const tick = span(Number(tickSeconds) > 0 ? Number(tickSeconds) : defaultTickSeconds);
  switch (f) {
    case "stale":
      return `Not rotated for more than twice its rotation (${rotation}). The alert fires on this: look at the service log.`;
    case "late":
      return `Overdue: past its rotation (${rotation}) and a whole tick (${tick}), so a tick has not replaced it. Look at the service log.`;
    case "due":
      return `Due: past its rotation (${rotation}); the rotation runs every ${tick}, and the next tick replaces it.`;
    case "none":
      return "Nothing is stored yet; the next tick mints the first one.";
    default:
      return "";
  }
}

/** Seconds, as the words an operator uses: 90s, 20m, 3h, 2d. */
export function span(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "—";
  if (seconds % 86400 === 0) return `${seconds / 86400}d`;
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

/** The `credential_process` an AWS profile uses for an R2 preset. */
export function credentialProcess(preset: string): string {
  return `sluisctl cloudflare r2 ${shellWord(preset)}`;
}

/** What to put in ~/.aws/config for an R2 preset by hand, when `sluisctl
 *  aws-config` is not used. The checksum settings are what R2 needs from the
 *  SDKs that default to sending a checksum it does not accept. */
export function awsProfile(preset: string, endpoint: string): string {
  return [
    `[profile cloudflare-${preset}]`,
    `credential_process = ${credentialProcess(preset)}`,
    `endpoint_url = ${endpoint}`,
    "region = auto",
    "request_checksum_calculation = when_required",
    "response_checksum_validation = when_required",
    "s3 =",
    "  addressing_style = path",
  ].join("\n");
}

/** The shell command that prints a token, as an export a script can eval. */
export function tokenCommand(preset: string): string {
  return `sluisctl cloudflare token ${shellWord(preset)} --format env`;
}

// A preset's name is a DNS label by validation; quoting anything else keeps a
// copied command from being something other than what the page showed.
function shellWord(word: string): string {
  return /^[A-Za-z0-9._-]+$/.test(word) ? word : `'${word.replace(/'/g, `'\\''`)}'`;
}
