#!/usr/bin/env node
// Page-status lint: every page under docs/ must carry `status: target |
// partial | shipped` in its front matter, and every aside titled
// `Open decision OD-<n>` must name an id that is a row of docs/open-decisions.md.
// The ADRs in docs/decisions/ are not pages of the site and are not linted here.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const STATUSES = ['target', 'partial', 'shipped'];
const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const docs = join(root, 'docs');
const registry = join(docs, 'open-decisions.md');

function* walk(dir) {
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      if (path === join(docs, 'decisions')) continue;
      yield* walk(path);
    } else if (/\.(md|mdx)$/.test(name) && !name.startsWith('_')) {
      yield path;
    }
  }
}

function frontMatter(text) {
  const m = /^---\r?\n([\s\S]*?)\r?\n---(\r?\n|$)/.exec(text);
  return m ? m[1] : null;
}

// The value of a top-level `status:` key on one line, unquoted.
function status(fm) {
  const m = /^status:[ \t]*(.*)$/m.exec(fm);
  return m ? m[1].trim().replace(/^(["'])(.*)\1$/, '$2') : undefined;
}

// The ids that are rows of the registry table: `| OD-3 | ...`.
function registeredIds() {
  const ids = new Set();
  for (const line of readFileSync(registry, 'utf8').split('\n')) {
    const m = /^\|\s*(OD-\d+)\s*\|/.exec(line);
    if (m) ids.add(m[1]);
  }
  return ids;
}

// The titles of the `:::caution[Open decision ...]` asides outside code fences.
function* openDecisionAsides(text) {
  let fence = null;
  for (const [i, line] of text.split('\n').entries()) {
    const f = /^\s*(`{3,}|~{3,})/.exec(line);
    if (f) {
      if (fence === null) fence = f[1][0];
      else if (f[1][0] === fence) fence = null;
      continue;
    }
    if (fence !== null) continue;
    const m = /^\s*:{3,}\s*caution\[\s*(Open decision\b[^\]]*)\]/i.exec(line);
    if (m) yield { line: i + 1, title: m[1] };
  }
}

let ids;
try {
  ids = registeredIds();
} catch {
  console.error(`check-status: cannot read ${relative(root, registry)}`);
  process.exit(1);
}
if (ids.size === 0) {
  console.error(`check-status: no OD-<n> rows in ${relative(root, registry)}; an empty registry proves nothing`);
  process.exit(1);
}

let failures = 0;
let checked = 0;
let asides = 0;
for (const file of walk(docs)) {
  const rel = relative(root, file);
  const text = readFileSync(file, 'utf8');
  const fm = frontMatter(text);
  const problems = [];
  if (fm === null) problems.push('no front matter');
  else {
    const s = status(fm);
    if (s === undefined) problems.push(`no status, want one of ${STATUSES.join(', ')}`);
    else if (!STATUSES.includes(s)) problems.push(`status "${s}", want one of ${STATUSES.join(', ')}`);
  }
  for (const { line, title } of openDecisionAsides(text)) {
    asides++;
    const m = /^Open decision (OD-\d+)(?::|$)/.exec(title);
    if (!m) problems.push(`line ${line}: "${title}" must read "Open decision OD-<n>: short title"`);
    else if (!ids.has(m[1])) problems.push(`line ${line}: ${m[1]} is not a row of ${relative(root, registry)}`);
  }
  checked++;
  for (const p of problems) {
    failures++;
    console.error(`${rel}: ${p}`);
  }
}

if (checked === 0) {
  console.error(`check-status: no pages found under ${relative(root, docs)}; an empty sweep proves nothing`);
  process.exit(1);
}
if (failures > 0) {
  console.error(`check-status: ${failures} problems in ${checked} pages`);
  process.exit(1);
}
console.log(`check-status: ${checked} pages with a valid status, ${asides} open-decision asides, ${ids.size} registered ids`);
