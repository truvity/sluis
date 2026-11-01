#!/usr/bin/env node
// Example files shown on a page are the files themselves. A fenced block in docs/ that follows the
// line `<!-- example: <path> -->` must equal the file at <path> (relative to the repository root),
// byte for byte, so the page and the file the tests run cannot drift apart.
//
// usage: tools/checks/examples.mjs [repo-root]
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { reporter, repoRoot, trackedFiles } from './lib.mjs';

const root = repoRoot();
const { error, finish } = reporter('check-examples');
const MARK = /^<!-- example: (\S+) -->\n(`{3,})[^\n]*\n([\s\S]*?)^\2\s*$/gm;
let checked = 0;
for (const page of trackedFiles(root).filter((f) => f.startsWith('docs/') && /\.mdx?$/.test(f))) {
  const text = readFileSync(join(root, page), 'utf8');
  const marks = text.split('\n').filter((l) => l.startsWith('<!-- example:')).length;
  const hits = [...text.matchAll(MARK)];
  if (hits.length !== marks) error(`${page}: an \`<!-- example: path -->\` line is not followed by a fenced block`);
  for (const [, path, , body] of hits) {
    checked++;
    if (!existsSync(join(root, path))) error(`${page}: the example names ${path}, which does not exist`);
    else if (readFileSync(join(root, path), 'utf8') !== body) error(`${page}: the example differs from ${path}; copy the file into the page`);
  }
}
finish(checked, 'examples');
