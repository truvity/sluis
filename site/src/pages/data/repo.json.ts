// /data/repo.json: the repository facts of this build (src/lib/repo.ts), for the status
// page's live layer and for any tool that wants them without parsing Markdown or YAML.
import { BUILT_AT, repoFacts } from '../../lib/repo';

export async function GET() {
  return new Response(JSON.stringify(await repoFacts(BUILT_AT), null, 1) + '\n', {
    headers: { 'content-type': 'application/json; charset=utf-8' },
  });
}
