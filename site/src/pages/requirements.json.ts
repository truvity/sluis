// The catalogue as one JSON file, /requirements.json, for test tools in any language:
// they read this, never the YAML or the HTML pages.
import { exportJson } from '../lib/requirements';

export function GET() {
  return new Response(JSON.stringify(exportJson(), null, 1) + '\n', { headers: { 'content-type': 'application/json; charset=utf-8' } });
}
