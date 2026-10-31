// The rows of the status page (/status/, ADR 0063): one group per option of the project's
// `Product milestone` field, in the field's order, then "Not in a milestone".
//
// - An issue belongs to the group of its own milestone value.
// - A requirement belongs to the group of the issues it links (`issues:`); with several, the
//   earliest group in option order. A requirement whose issues have no milestone, or that
//   links none, is "Not in a milestone".
// - A decision record belongs to the group of its proposal issue.
// - A decision still "to write" is an open `type:decision` issue that no record names as
//   its proposal.
// Requirement and decision facts come from the repository; issue facts from the board,
// read at build time (src/lib/project.ts). Without the board every requirement and record
// is "Not in a milestone", and the issue figures are unknown (null).
import { decisionFacts, isAccepted, isProposed, listDecisions, type DecisionFacts } from '../lib-decisions';
import { autoVerifiable, catalogue, counted, type Area, type Req } from './requirements';
import { project, type BoardIssue } from './project';

export const NO_MILESTONE = 'Not in a milestone';
export const LABEL_DECISION = 'type:decision';
export const LABEL_BLOCKED = 'status:blocked';
export const LABEL_NEEDS_DECISION = 'status:needs-decision';

export interface ReqCounts { total: number; open: number; planned: number; partial: number; done: number; auto: number }
export interface AreaRow { area: Area; counts: ReqCounts }
export interface Group {
  name: string;
  slug: string;
  milestone: boolean;
  current: boolean;
  reqs: ReqCounts;
  areas: AreaRow[];
  adrs: { accepted: number; proposed: number; toWrite: number | null; records: DecisionFacts[] };
  issues: { open: number; closed: number; blocked: number; needsDecision: number } | null;
  openDecisionIssues: BoardIssue[];
  otherOpenIssues: BoardIssue[];
}

const countReqs = (rs: Req[]): ReqCounts => ({
  total: rs.length,
  open: rs.filter((r) => r.status === 'open').length,
  planned: rs.filter((r) => r.status === 'planned').length,
  partial: rs.filter((r) => r.status === 'partial').length,
  done: rs.filter((r) => r.status === 'done').length,
  auto: rs.filter(autoVerifiable).length,
});

const slugOf = (name: string) => name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'group';

export async function statusGroups() {
  const board = await project();
  const c = catalogue();
  const records = (await listDecisions()).map(decisionFacts);
  const options = board.available ? board.options : [];
  const names = [...options, NO_MILESTONE];
  const last = names.length - 1;
  const issue = (n: number) => (board.available ? board.issues.get(n) : undefined);
  const indexOfIssue = (n: number | null) => {
    const m = n === null ? null : issue(n)?.milestone;
    return m ? options.indexOf(m) : last;
  };

  const reqsBy = names.map(() => [] as Req[]);
  for (const r of c.reqs.filter(counted)) reqsBy[Math.min(last, ...(r.issues ?? []).map(indexOfIssue))].push(r);

  const recordsBy = names.map(() => [] as DecisionFacts[]);
  for (const d of records) recordsBy[indexOfIssue(d.proposal)].push(d);
  const proposals = new Set(records.map((d) => d.proposal).filter((n) => n !== null));

  const issuesBy = names.map(() => [] as BoardIssue[]);
  if (board.available) for (const i of board.issues.values()) issuesBy[indexOfIssue(i.number)].push(i);

  const byNumber = (a: BoardIssue, b: BoardIssue) => a.number - b.number;
  const groups: Group[] = names.map((name, gi) => {
    const rs = reqsBy[gi];
    const is = issuesBy[gi];
    const open = is.filter((i) => i.open).sort(byNumber);
    const decisionIssue = (i: BoardIssue) => i.labels.includes(LABEL_DECISION);
    return {
      name,
      slug: slugOf(name),
      milestone: gi < last,
      current: false,
      reqs: countReqs(rs),
      areas: c.areas
        .map((area) => ({ area, counts: countReqs(rs.filter((r) => r.area === area.code)) }))
        .filter((a) => a.counts.total > 0),
      adrs: {
        accepted: recordsBy[gi].filter((d) => isAccepted(d.status)).length,
        proposed: recordsBy[gi].filter((d) => isProposed(d.status)).length,
        toWrite: board.available ? open.filter((i) => decisionIssue(i) && !proposals.has(i.number)).length : null,
        records: recordsBy[gi],
      },
      issues: board.available
        ? {
            open: open.length,
            closed: is.length - open.length,
            blocked: open.filter((i) => i.labels.includes(LABEL_BLOCKED)).length,
            needsDecision: open.filter((i) => i.labels.includes(LABEL_NEEDS_DECISION)).length,
          }
        : null,
      openDecisionIssues: open.filter(decisionIssue),
      otherOpenIssues: open.filter((i) => !decisionIssue(i)),
    };
  });
  // The current milestone: the first option, in order, that still has open items.
  const current = groups.find((g) => g.milestone && g.issues && g.issues.open > 0);
  if (current) current.current = true;
  return { board, groups };
}
