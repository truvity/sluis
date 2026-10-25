#!/usr/bin/env node
// Front-matter description lint: every page under docs/ must carry a
// `description:` of 40 to 160 characters (search results, link previews and
// llms.txt are built from it). The ADRs in docs/decisions/ are indexed by the
// site rather than rendered as pages of their own, so they are not linted here.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const MIN = 40;
const MAX = 160;
const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const docs = join(root, 'docs');

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

// The value of a top-level `description:` key on one line, unquoted.
function description(fm) {
  const m = /^description:[ \t]*(.*)$/m.exec(fm);
  if (!m) return { missing: true };
  let v = m[1].trim();
  if (v === '' || v === '|' || v === '>' || /^[|>][+-]?$/.test(v)) {
    return { error: 'write the description on one line' };
  }
  const q = v[0];
  if ((q === '"' || q === "'") && v.endsWith(q) && v.length >= 2) {
    v = v.slice(1, -1);
    v = q === '"' ? v.replace(/\\(["\\])/g, '$1') : v.replace(/''/g, "'");
  }
  return { value: v };
}

let failures = 0;
let checked = 0;
for (const file of walk(docs)) {
  const rel = relative(root, file);
  const fm = frontMatter(readFileSync(file, 'utf8'));
  let problem = null;
  if (fm === null) problem = 'no front matter';
  else {
    const d = description(fm);
    if (d.missing) problem = 'no description';
    else if (d.error) problem = d.error;
    else if (d.value.length < MIN || d.value.length > MAX) {
      problem = `description is ${d.value.length} characters, want ${MIN} to ${MAX}`;
    }
  }
  checked++;
  if (problem) {
    failures++;
    console.error(`${rel}: ${problem}`);
  }
}

if (checked === 0) {
  console.error(`check-descriptions: no pages found under ${relative(root, docs)}; an empty sweep proves nothing`);
  process.exit(1);
}
if (failures > 0) {
  console.error(`check-descriptions: ${failures} of ${checked} pages failed`);
  process.exit(1);
}
console.log(`check-descriptions: ${checked} pages, all descriptions ${MIN} to ${MAX} characters`);
