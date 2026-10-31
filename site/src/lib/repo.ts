// The repository facts the build publishes as /data/repo.json (ADR 0063): the decision
// records with their status and proposal issue, the requirements with the issues that
// deliver them, and the coverage figure. They change only by a merged pull request.
// The shape is public/data/repo.schema.json; site/scripts/check-repo-json.mjs validates
// the built file against it. Bump `schema` when a field is removed or changes meaning.
import { decisionFacts, listDecisions } from '../lib-decisions';
import { catalogue, coverage } from './requirements';

export const REPO_SCHEMA = 1;

export async function repoFacts(builtAt: string) {
  const decisions = (await listDecisions()).map(decisionFacts);
  const c = catalogue();
  return {
    $schema: 'https://sluis.id/data/repo.schema.json',
    schema: REPO_SCHEMA,
    builtAt,
    source: { decisions: 'docs/decisions/NNNN-*.md', requirements: 'docs/requirements/*.yaml' },
    decisions,
    requirements: c.reqs.map((r) => ({ id: r.id, area: r.area, level: r.level, status: r.status, issues: r.issues ?? [] })),
    coverage: coverage(),
  };
}

// One build time for every page and endpoint of a build.
export const BUILT_AT = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z');
