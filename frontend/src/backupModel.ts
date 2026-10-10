import { moduleName } from "./maintenanceModel";

/** How long after a completed backup the page says the schedule has missed:
 *  the daily run plus the 12 hours the alarm allows. */
export const staleAfterHours = 36;

export type Tone = "success" | "info" | "warning" | "error" | "default";

type Stamp = { seconds: bigint; nanos?: number } | undefined;

const ms = (s: Stamp): number | undefined => (s ? Number(s.seconds) * 1000 : undefined);

/** The chip of a run's state. An unknown word is shown as it is. */
export function runState(state: string): { label: string; tone: Tone } {
  switch (state) {
    case "completed":
      return { label: "completed", tone: "success" };
    case "running":
      return { label: "running", tone: "info" };
    case "paused":
      return { label: "paused", tone: "warning" };
    case "failed":
      return { label: "failed", tone: "error" };
    default:
      return { label: state || "unknown", tone: "default" };
  }
}

/** How far an unfinished run has come: "3 of 8 units (38%)", or nothing
 *  when the module did not say how many there are. */
export function progress(run: { units: number; done: number } | undefined): string | undefined {
  if (!run || run.units <= 0) return undefined;
  const done = Math.min(Math.max(run.done, 0), run.units);
  return `${done} of ${run.units} units (${Math.floor((done * 100) / run.units)}%)`;
}

/** What the last success says about the schedule: nothing yet, fresh, or
 *  stale (older than the alarm's limit). */
export type Freshness = "none" | "fresh" | "stale";

export function freshness(lastCompleted: { finished?: Stamp; started?: Stamp } | undefined, now: Date): Freshness {
  const when = ms(lastCompleted?.finished) ?? ms(lastCompleted?.started);
  if (when === undefined) return "none";
  return now.getTime() - when > staleAfterHours * 3600_000 ? "stale" : "fresh";
}

export function freshnessNote(f: Freshness): string {
  switch (f) {
    case "none":
      return "No backup has completed yet.";
    case "stale":
      return `The last completed backup is more than ${staleAfterHours} hours old. The alarm fires on this: look at the backup function's log.`;
    default:
      return "";
  }
}

/** Newest first by creation time, then id. The server sorts as well; this
 *  keeps the page right against a server that does not. */
export function newestFirst<T extends { id: string; created?: Stamp }>(backups: T[]): T[] {
  return [...backups].sort((a, b) => (ms(b.created) ?? 0) - (ms(a.created) ?? 0) || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0));
}

/** "4 modules · 90 records · 6 chunks" for a backup or a run; a manifest that
 *  could not be read says so instead. */
export function counts(b: { modules: number; records: bigint | number; chunks: number; error?: string }): string {
  if (b.error) return "manifest unreadable";
  return `${b.modules} modules · ${b.records} records · ${b.chunks} chunks`;
}

/** The sentence under the retention row. */
export function retentionText(r: { keep: number; maxAge: string; kept: number; removed: number; cleaned: number; refused: number } | undefined): string {
  if (!r) return "No retention pass has run yet.";
  const parts = [`kept ${r.kept}`, `removed ${r.removed}`];
  if (r.cleaned > 0) parts.push(`cleaned ${r.cleaned} partial`);
  if (r.refused > 0) parts.push(`${r.refused} deletes refused`);
  return `The newest ${r.keep} always stay, and another goes when older than ${r.maxAge || "the limit"}. Last pass: ${parts.join(", ")}.`;
}

/** What the restore card says of a module whose maintenance flag is set. */
export function maintenanceLine(m: { module: string; state: string; by: string; unreadable: boolean }): string {
  const name = moduleName(m.module);
  if (m.unreadable) return `${name}: the flag could not be read, so it is treated as set`;
  return `${name}: ${m.state || "under maintenance"}${m.by ? `, set by ${m.by}` : ""}`;
}

/** Where a restore is started. The console does not offer it. */
export const restoreHowTo =
  "A restore is started with sluisctl by an administrator or the break-glass role, not from this console. It sets maintenance in every module, writes the backup back, reads it back and only then lifts maintenance.";
