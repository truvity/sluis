#!/usr/bin/env node
// Markdown everywhere; MDX only for a page that places components (docs/contributing/pages.md,
// "Page format"). A tracked docs/**/*.mdx file must import at least one component (a name that
// starts with a capital letter) and place it as <Name ...>. One without fails: it is Markdown that
// should be named .md, and a broken component in .mdx breaks the whole build.
//
// usage: tools/checks/mdx-pages.mjs [repo-root]
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { reporter, repoRoot, trackedFiles } from './lib.mjs';

const root = repoRoot();
const r = reporter('check-mdx-pages');

// The names an `import` statement binds: `import A from`, `import { B, C as D } from`,
// `import * as E from`, and any mix of the first two. A bare `import 'x.css'` binds none.
function importedNames(text) {
  const names = [];
  for (const m of text.matchAll(/^import\s+([^'"\n]+?)\s+from\s+['"][^'"]+['"]/gm)) {
    const clause = m[1];
    const named = /\{([^}]*)\}/.exec(clause)?.[1] ?? '';
    for (const part of named.split(',')) {
      const name = part.trim().split(/\s+as\s+/).pop();
      if (name) names.push(name);
    }
    const rest = clause.replace(/\{[^}]*\}/, '').replace(/,/g, ' ').trim();
    for (const word of rest.split(/\s+/)) {
      const name = word.replace(/^\*\s*as\s+/, '');
      if (name && name !== '*' && name !== 'as') names.push(name);
    }
    const star = /\*\s+as\s+(\w+)/.exec(clause)?.[1];
    if (star) names.push(star);
  }
  return [...new Set(names)].filter((n) => /^[A-Z]/.test(n));
}

let mdx = 0;
for (const path of trackedFiles(root)) {
  if (!/^docs\/.*\.mdx$/.test(path)) continue;
  mdx++;
  const text = readFileSync(join(root, path), 'utf8');
  const body = text.replace(/^import\s.*$/gm, '');
  const placed = importedNames(text).filter((n) => new RegExp(`<${n}[\\s/>.]`).test(body));
  if (placed.length === 0) r.error(`${path}: use .md: MDX only for pages that place components`);
}
r.finish(mdx, 'docs/**/*.mdx files');
