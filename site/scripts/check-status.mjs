#!/usr/bin/env node
// Page-status lint: every page under docs/ must carry `status: target |
// partial | shipped` in its front matter, and every aside titled
// `Open decision OD-<n>` (a `:::caution[...]` aside in Markdown, an
// `<OpenDecision id="OD-<n>"` component in MDX) must name an id that is a row
// of the "Open" table of docs/open-decisions.md. An id of the "Decided" table
// is settled: a page states it as plain prose, never as an aside.
// The ADRs in docs/decisions/ are not pages of the site and are not linted here. Nor is docs/requirements/ (see check-requirements.mjs).
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
      if (path === join(docs, 'decisions') || path === join(docs, 'requirements')) continue;
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

// The ids that are rows of the registry tables: `| OD-3 | ...`. The rows under
// the "## Open" heading are open; the rows under "## Decided" are decided.
function registeredIds() {
  const open = new Set();
  const decided = new Set();
  let section = null;
  for (const line of readFileSync(registry, 'utf8').split('\n')) {
    const h = /^##\s+(.*?)\s*$/.exec(line);
    if (h) {
      section = /^open$/i.test(h[1]) ? open : /^decided$/i.test(h[1]) ? decided : null;
      continue;
    }
    const m = /^\|\s*(?:<a id="od-\d+"><\/a>)?\s*(OD-\d+)\s*\|/.exec(line);
    if (!m) continue;
    if (section === null) throw new Error(`${m[1]} is a row outside the "Open" and "Decided" tables`);
    if (open.has(m[1]) || decided.has(m[1])) throw new Error(`${m[1]} is listed twice`);
    section.add(m[1]);
  }
  return { open, decided };
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

// The ids of the `<OpenDecision id="OD-n"` components of an MDX page, outside code fences.
function* openDecisionComponents(text) {
  let fence = null;
  for (const [i, line] of text.split('\n').entries()) {
    const f = /^\s*(`{3,}|~{3,})/.exec(line);
    if (f) {
      if (fence === null) fence = f[1][0];
      else if (f[1][0] === fence) fence = null;
      continue;
    }
    if (fence !== null) continue;
    for (const m of line.matchAll(/<OpenDecision\b[^>]*?\bid=(?:"([^"]*)"|'([^']*)'|\{["'`]([^"'`]*)["'`]\})/g)) {
      yield { line: i + 1, id: m[1] ?? m[2] ?? m[3] };
    }
  }
}

let ids;
try {
  ids = registeredIds();
} catch (e) {
  console.error(`check-status: cannot read ${relative(root, registry)}: ${e.message}`);
  process.exit(1);
}
if (ids.open.size === 0 || ids.decided.size === 0) {
  console.error(`check-status: ${relative(root, registry)} has ${ids.open.size} open and ${ids.decided.size} decided OD-<n> rows; an empty table proves nothing`);
  process.exit(1);
}
// One message for an id that an aside may not cite.
function notOpen(id) {
  if (ids.decided.has(id)) return `${id} is decided; state it as plain prose, not as an open-decision aside`;
  return `${id} is not a row of ${relative(root, registry)}`;
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
    else if (!ids.open.has(m[1])) problems.push(`line ${line}: ${notOpen(m[1])}`);
  }
  if (file.endsWith('.mdx')) {
    for (const { line, id } of openDecisionComponents(text)) {
      asides++;
      if (!/^OD-\d+$/.test(id)) problems.push(`line ${line}: <OpenDecision id="${id}"> must read OD-<n>`);
      else if (!ids.open.has(id)) problems.push(`line ${line}: ${notOpen(id)}`);
    }
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
console.log(`check-status: ${checked} pages with a valid status, ${asides} open-decision asides, ${ids.open.size} open and ${ids.decided.size} decided ids`);
