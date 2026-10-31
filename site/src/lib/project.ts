// The v2 project board, read once per build with the GitHub GraphQL API (ADR 0063): the
// options of its `Product milestone` field, in their order, and every sluis issue on the
// board with its state, labels and milestone. The status page groups by those options,
// so a new or reordered option changes the page with no code change.
//
// The token is GH_TOKEN or GITHUB_TOKEN from the environment, tried in that order; it is
// sent only to api.github.com and never logged. Reading an organisation's project needs a
// token with that permission: a workstation's `gh auth token` has it, the GITHUB_TOKEN of
// GitHub Actions does not. Without one the build does not fail: it says so in one log line,
// and the page shows that the project data was not available at build time.
export const ORG = 'truvity';
export const PROJECT_NUMBER = 9;
export const FIELD = 'Product milestone';
export const REPO = 'truvity/sluis';

export interface BoardIssue {
  number: number;
  title: string;
  open: boolean;
  labels: string[];
  milestone: string | null;
}

export type Project =
  | { available: true; options: string[]; issues: Map<number, BoardIssue>; items: number }
  | { available: false; reason: string };

const QUERY = `query($org: String!, $number: Int!, $field: String!, $after: String) {
  organization(login: $org) {
    projectV2(number: $number) {
      field(name: $field) { ... on ProjectV2SingleSelectField { options { id name } } }
      items(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          content { __typename ... on Issue { number title state labels(first: 50) { nodes { name } } repository { nameWithOwner } } }
          fieldValueByName(name: $field) { ... on ProjectV2ItemFieldSingleSelectValue { name } }
        }
      }
    }
  }
}`;

// At most this many pages of 100 items; a board that grows past it fails closed with a reason.
const MAX_PAGES = 20;

async function page(token: string, after: string | null) {
  const res = await fetch('https://api.github.com/graphql', {
    method: 'POST',
    headers: { authorization: `bearer ${token}`, 'content-type': 'application/json', 'user-agent': 'sluis-site-build' },
    body: JSON.stringify({ query: QUERY, variables: { org: ORG, number: PROJECT_NUMBER, field: FIELD, after } }),
    signal: AbortSignal.timeout(30_000),
  });
  if (!res.ok) throw new Error(`GitHub GraphQL answered HTTP ${res.status}`);
  const body = await res.json();
  const project = body?.data?.organization?.projectV2;
  if (!project) {
    const kinds = [...new Set((body?.errors ?? []).map((e: { type?: string }) => e.type ?? 'error'))].join(', ');
    throw new Error(`the token cannot read the ${ORG} project ${PROJECT_NUMBER}${kinds ? ` (${kinds})` : ''}`);
  }
  return project;
}

async function read(token: string): Promise<Project> {
  const issues = new Map<number, BoardIssue>();
  let options: string[] | null = null;
  let after: string | null = null;
  let items = 0;
  for (let n = 0; n < MAX_PAGES; n++) {
    const project = await page(token, after);
    if (!options) {
      options = (project.field?.options ?? []).map((o: { name: string }) => o.name);
      if (!options!.length) throw new Error(`the project has no single-select field "${FIELD}" with options`);
    }
    for (const node of project.items.nodes) {
      items++;
      const c = node.content;
      if (c?.__typename !== 'Issue' || c.repository?.nameWithOwner !== REPO) continue;
      const value: string | null = node.fieldValueByName?.name ?? null;
      issues.set(c.number, {
        number: c.number,
        title: c.title,
        open: c.state === 'OPEN',
        labels: (c.labels?.nodes ?? []).map((l: { name: string }) => l.name),
        milestone: value && options!.includes(value) ? value : null,
      });
    }
    if (!project.items.pageInfo.hasNextPage) return { available: true, options: options!, issues, items };
    after = project.items.pageInfo.endCursor;
  }
  throw new Error(`the project has more than ${MAX_PAGES * 100} items`);
}

let cached: Promise<Project> | undefined;
export function project(): Promise<Project> {
  cached ??= (async () => {
    const tokens = [process.env.GH_TOKEN, process.env.GITHUB_TOKEN].filter((t): t is string => !!t);
    if (!tokens.length) return say({ available: false, reason: 'no GH_TOKEN or GITHUB_TOKEN in the environment' });
    let reason = '';
    for (const token of tokens) {
      try {
        return say(await read(token));
      } catch (e) {
        reason = e instanceof Error ? e.message : String(e);
      }
    }
    return say({ available: false, reason });
  })();
  return cached;
}

// One line in the build log, worded so that the strict build (which fails on "warning:")
// does not mistake the expected fallback for a fault.
function say(p: Project): Project {
  if (p.available) console.log(`status page: read the project board (${p.items} items, ${p.issues.size} sluis issues, options: ${p.options.join(' | ')})`);
  else console.log(`status page: project data not available at build time: ${p.reason}`);
  return p;
}
