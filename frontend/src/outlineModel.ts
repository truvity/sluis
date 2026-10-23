import type { StateKind } from "./ui";

/** The Outline controller's report on one workspace: the status document of
 *  version 1 (internal/outlineroster/status), as the console reads it from
 *  `report_json`. Field names are the document's own. */
export type OutlineReport = {
  version: number;
  workspace: string;
  enabled: boolean;
  url: string;
  key_user: string;
  /** Why the whole workspace is held, when it is. */
  held: string;
  tick: OutlineTick;
  users: OutlineUser[];
  groups: OutlineGroup[];
  collections: OutlineCollection[];
  reported: OutlineFinding[];
  shares_scanned_at: string;
  breakers: OutlineBreaker[];
};

export type OutlineTick = { at: string; outcome: string; error: string; changes: number; held: number; retrying: number };

export type OutlineUser = { email: string; id: string; name: string; role: string; target: string; suspended: boolean; state: string; action: string; reason: string };

export type OutlineMember = { email: string; user_id: string; permission: string; state: string; action: string; reason: string };

export type OutlineGroup = { name: string; id: string; state: string; action: string; reason: string; members: OutlineMember[] };

export type OutlineGrant = { group: string; group_id: string; permission: string; target: string; state: string; action: string; reason: string };

export type OutlineCollection = {
  name: string;
  id: string;
  mode: string;
  directory: string;
  state: string;
  action: string;
  reason: string;
  grants: OutlineGrant[];
  members: OutlineMember[];
};

export type OutlineFinding = { kind: string; subject: string; collection: string; detail: string };

export type OutlineBreaker = { scope: string; affected: number; total: number; reason: string; fingerprint: string; confirmed: boolean };

/** The document version this console reads. */
export const reportVersion = 1;

/** A workspace's report, read: absent before the first pass, or the reason
 *  it could not be read. */
export type ParsedReport = { report?: OutlineReport; error?: string };

type Loose = Record<string, unknown>;

const str = (value: unknown): string => (typeof value === "string" ? value : "");
const num = (value: unknown): number => (typeof value === "number" && Number.isFinite(value) ? value : 0);
const list = (value: unknown): Loose[] => (Array.isArray(value) ? value.filter((v): v is Loose => typeof v === "object" && v !== null) : []);
const obj = (value: unknown): Loose => (typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Loose) : {});

function member(m: Loose): OutlineMember {
  return { email: str(m.email), user_id: str(m.user_id), permission: str(m.permission), state: str(m.state), action: str(m.action), reason: str(m.reason) };
}

/** Reads `report_json`. The document leaves out what is empty, so every
 *  list and string is filled in here and the page never asks twice. */
export function parseReport(raw: string): ParsedReport {
  if (!raw.trim()) return {};
  let doc: Loose;
  try {
    doc = obj(JSON.parse(raw));
  } catch {
    return { error: "The controller's last report could not be read." };
  }
  if (doc.version !== reportVersion) {
    return { error: `The controller's last report is of version ${String(doc.version ?? "none")}, and this console reads version ${reportVersion}.` };
  }
  const tick = obj(doc.tick);
  return {
    report: {
      version: reportVersion,
      workspace: str(doc.workspace),
      enabled: doc.enabled === true,
      url: str(doc.url),
      key_user: str(doc.key_user),
      held: str(doc.held),
      tick: { at: str(tick.at), outcome: str(tick.outcome), error: str(tick.error), changes: num(tick.changes), held: num(tick.held), retrying: num(tick.retrying) },
      users: list(doc.users).map((u) => ({
        email: str(u.email),
        id: str(u.id),
        name: str(u.name),
        role: str(u.role),
        target: str(u.target),
        suspended: u.suspended === true,
        state: str(u.state),
        action: str(u.action),
        reason: str(u.reason),
      })),
      groups: list(doc.groups).map((g) => ({
        name: str(g.name),
        id: str(g.id),
        state: str(g.state),
        action: str(g.action),
        reason: str(g.reason),
        members: list(g.members).map(member),
      })),
      collections: list(doc.collections).map((c) => ({
        name: str(c.name),
        id: str(c.id),
        mode: str(c.mode),
        directory: str(c.directory),
        state: str(c.state),
        action: str(c.action),
        reason: str(c.reason),
        grants: list(c.grants).map((g) => ({
          group: str(g.group),
          group_id: str(g.group_id),
          permission: str(g.permission),
          target: str(g.target),
          state: str(g.state),
          action: str(g.action),
          reason: str(g.reason),
        })),
        members: list(c.members).map(member),
      })),
      reported: list(doc.reported).map((f) => ({ kind: str(f.kind), subject: str(f.subject), collection: str(f.collection), detail: str(f.detail) })),
      shares_scanned_at: str(doc.shares_scanned_at),
      breakers: list(doc.breakers).map((b) => ({
        scope: str(b.scope),
        affected: num(b.affected),
        total: num(b.total),
        reason: str(b.reason),
        fingerprint: str(b.fingerprint),
        confirmed: b.confirmed === true,
      })),
    },
  };
}

/** A time in the document, or undefined for none (Go's zero time included). */
export function when(stamp: string): Date | undefined {
  if (!stamp) return undefined;
  const date = new Date(stamp);
  return Number.isNaN(date.getTime()) || date.getUTCFullYear() <= 1 ? undefined : date;
}

/** How a row state is drawn: the chip, and what it means in Outline. */
export type StateView = { kind: StateKind; title: string };

const stateViews: Record<string, StateView> = {
  ok: { kind: "ok", title: "As the policy says." },
  "will-add": { kind: "will-add", title: "Belongs and is not there: the controller adds it." },
  "will-remove": { kind: "will-remove", title: "There, does not belong, and the directory vouches for that: the controller removes it." },
  "will-change": { kind: "will-change", title: "A role or a permission differs from the policy's: the controller changes it." },
  "will-suspend": { kind: "will-suspend", title: "No longer in the directory: the controller suspends the account." },
  "will-activate": { kind: "will-activate", title: "Suspended, and the directory has the person back: the controller reactivates the account." },
  "will-create": { kind: "will-create", title: "The policy names it and Outline has none: the controller creates it." },
  "will-adopt": { kind: "will-adopt", title: "One of this name exists: the controller adopts it and manages it from now on." },
  held: { kind: "held", title: "Something is to be done and waits for a person. The reason says what." },
  retrying: { kind: "retrying", title: "The directory could not answer for this one this pass: nothing changes, and the next pass tries again." },
  reported: { kind: "slack-reported", title: "Said and never acted on." },
  ignored: { kind: "slack-ignored", title: "On the workspace's ignore list, or the API key's own account: never demoted, removed or suspended." },
};

/** The chip for a row state. A state this console does not know is shown
 *  as it is spelled, so a newer controller is never drawn as "OK". */
export function stateView(state: string): StateView & { label?: string } {
  return stateViews[state] ?? { kind: "unrecognised", label: state || "no state", title: "A state this console does not know." };
}

const outcomeViews: Record<string, StateView> = {
  "in-sync": { kind: "in-sync", title: "The last pass found nothing to do." },
  applied: { kind: "applied", title: "The last pass made changes." },
  "dry-run": { kind: "dry-run", title: "The workspace is not enabled: the last pass derived everything and changed nothing. The rows below say what it would do." },
  held: { kind: "held", title: "The last pass held something back until a person acts." },
  retrying: { kind: "retrying", title: "The directory could not answer for somebody; the next pass tries again." },
  waiting: { kind: "not-connected", title: "Nothing to do yet: the workspace is not connected." },
  failed: { kind: "failed", title: "The last pass could not complete. Nothing was changed on its account." },
};

/** The chip for the last pass's outcome. */
export function outcomeView(outcome: string): StateView & { label?: string } {
  return outcomeViews[outcome] ?? { kind: "unrecognised", label: outcome || "no outcome", title: "An outcome this console does not know." };
}

/** Whether a row is worth a reader's attention: anything not as the
 *  policy says. */
export function needsAttention(state: string): boolean {
  return state !== "ok";
}

/** A role, and what it becomes when the policy says otherwise. */
export function roleChange(role: string, target: string): string {
  if (!target || target === role) return role || "—";
  return `${role || "none"} → ${target}`;
}

/** A grant's permission now, and what it becomes: empty is no grant. */
export function permissionChange(permission: string, target: string): string {
  if (permission === target) return permission || "none";
  return `${permission || "none"} → ${target || "none"}`;
}

/** The report narrowed to what needs attention: rows not "ok", and a group
 *  or collection kept, with only its rows that need attention, when it or
 *  any of them does. `all` keeps everything. */
export function narrowed(report: OutlineReport, all: boolean): Pick<OutlineReport, "users" | "groups" | "collections"> {
  if (all) return { users: report.users, groups: report.groups, collections: report.collections };
  const keep = <T extends { state: string }>(rows: T[]) => rows.filter((row) => needsAttention(row.state));
  return {
    users: keep(report.users),
    groups: report.groups
      .map((g) => ({ ...g, members: keep(g.members) }))
      .filter((g) => needsAttention(g.state) || g.members.length > 0),
    collections: report.collections
      .map((c) => ({ ...c, grants: keep(c.grants), members: keep(c.members) }))
      .filter((c) => needsAttention(c.state) || c.grants.length > 0 || c.members.length > 0),
  };
}

/** How many rows of the report need attention, across every table. */
export function attentionCount(report: OutlineReport): number {
  const count = (rows: { state: string }[]) => rows.filter((row) => needsAttention(row.state)).length;
  return (
    count(report.users) +
    report.groups.reduce((n, g) => n + count([g]) + count(g.members), 0) +
    report.collections.reduce((n, c) => n + count([c]) + count(c.grants) + count(c.members), 0)
  );
}

/** The breakers still waiting for a confirmation. */
export function tripped(report: OutlineReport | undefined): OutlineBreaker[] {
  return (report?.breakers ?? []).filter((b) => !b.confirmed);
}

/** What a breaker's scope names, in words. */
export function scopeLabel(scope: string): string {
  if (scope === "admins") return "admin roles";
  if (scope === "suspensions") return "suspensions";
  const [kind, ...name] = scope.split(":");
  if ((kind === "group" || kind === "collection") && name.length > 0) return `${kind} ${name.join(":")}`;
  return scope;
}

/** The first line of a breaker's banner. */
export function breakerSentence(breaker: OutlineBreaker): string {
  return `Held, ${scopeLabel(breaker.scope)}: ${breaker.affected} of ${breaker.total} at once.`;
}

/** What the card says after a key is kept: whose it is, and whether the
 *  policy already leaves that account alone. */
export function keyUserNotice(keyUser: string, ignored: boolean): { severity: "success" | "warning"; text: string } {
  const who = keyUser || "an account Outline did not name";
  if (ignored) return { severity: "success", text: `The key is kept. It belongs to ${who}, which the workspace's ignore list already covers.` };
  return {
    severity: "warning",
    text: `The key is kept. It belongs to ${who}: add this address to the workspace's ignore list in the policy, or the controller may demote, remove or suspend the account it acts as.`,
  };
}

/** What the card says after Refresh. */
export function passNotice(workspace: string, kept: boolean): { severity: "success" | "info"; text: string } {
  return kept
    ? { severity: "success", text: `A pass over ${workspace} is requested. The controller notices within a minute.` }
    : { severity: "info", text: "Asked; at most once a minute. A pass over this workspace was requested less than a minute ago." };
}
