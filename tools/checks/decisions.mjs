#!/usr/bin/env node
// Decision records in docs/decisions/ (the rules of docs/decisions/README.md).
//
// The format is read from the repository, not written down here:
//   - the header fields (`**Status:**`, `**Date:**`) and the sections (`## Context` ...) are the
//     ones 0000-template.md has, in its order;
//   - the labels each road not taken carries (`Pros:`, `Cons:`, `Why not:`) are the ones the
//     template's first option has;
//   - the allowed statuses are the backticked values of the README's "Status." bullet, and the
//     template's comment must list the same set; NNNN in a status is a record number that must exist;
//   - the parts the README's "Format" list names must be the template's fields and sections.
//
// Then, for every record:
//   - the file name is NNNN-kebab-case.md, and no two records share a number;
//   - the title is `# NNNN — ...` with the number of the file;
//   - each header field appears once, before the first section, in the template's order;
//     the status is an allowed value and the date a real YYYY-MM-DD day;
//   - each section appears exactly once, in the template's order, and is not empty;
//   - "Roads not taken" has at least one `###` option, and each option has a bullet for every label
//     (`- Pros: ...`, `- **Pros:** ...` or `- **Pros**: ...`) with text after the colon or on an
//     indented next line. Before the colon a label may carry a qualifier in parentheses
//     ("Why not (inferred):") or one from QUALIFIERS ("Why not first:");
//   - no line of the template's instructions comment (<!-- ... -->) is left;
//   - the README index has exactly one row for it, linking the file, with the same status.
// And the index has no row without a file.
//
// usage: tools/checks/decisions.mjs [repo-root]
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { reporter, repoRoot } from './lib.mjs';

const root = repoRoot();
const dir = join(root, 'docs', 'decisions');
const r = reporter('check-decisions');
const E = r.error;

const TEMPLATE = '0000-template.md';
const FILE = /^(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$/;
const read = (name) => readFileSync(join(dir, name), 'utf8');

// Lines outside fenced code blocks, with their 1-based numbers.
function lines(text) {
  const out = [];
  let fence = null;
  text.split('\n').forEach((line, i) => {
    const f = /^\s*(```|~~~)/.exec(line);
    if (f) fence = fence === f[1] ? null : fence ?? f[1];
    else if (!fence) out.push({ n: i + 1, line });
  });
  return out;
}

// Split into the part before the first `## ` and one entry per `## ` section.
function sections(text) {
  const head = [];
  const secs = [];
  for (const l of lines(text)) {
    const h = /^## (.+?)\s*$/.exec(l.line);
    if (h) secs.push({ title: h[1], n: l.n, body: [] });
    else (secs.length ? secs[secs.length - 1].body : head).push(l);
  }
  return { head, secs };
}

const FIELD = /^\*\*([A-Z][A-Za-z ]*):\*\*\s*(.*?)\s*$/;
const LABEL = /^\s*-\s+(?:\*\*)?([A-Z][A-Za-z ]*?)(?:\*\*)?:(?:\*\*)?(?:\s|$)/;
// Words a label may carry before its colon, besides a parenthesised note.
const QUALIFIERS = ['first'];

// ---- the format, from the template
if (!existsSync(join(dir, TEMPLATE))) {
  E(`docs/decisions/${TEMPLATE} is missing; the format is read from it`);
  r.finish(0, 'records');
}
const template = read(TEMPLATE);
const tpl = sections(template);
const FIELDS = tpl.head.map((l) => FIELD.exec(l.line)?.[1]).filter(Boolean);
const SECTIONS = tpl.secs.map((s) => s.title);
const ROADS = 'Roads not taken';
const tplRoads = tpl.secs.find((s) => s.title === ROADS);
const LABELS = [];
for (const l of tplRoads?.body ?? []) {
  if (/^### /.test(l.line)) { if (LABELS.length) break; continue; }
  const m = LABEL.exec(l.line);
  if (m) LABELS.push(m[1]);
}
// The lines of the template's instructions comment; a record keeps none of them.
const tplComment = (/<!--([\s\S]*?)-->/.exec(template)?.[1] ?? '').split('\n').map((l) => l.trim()).filter((l) => l.length >= 12);
if (!FIELDS.includes('Status') || !FIELDS.includes('Date')) E(`${TEMPLATE}: no **Status:** and **Date:** fields found`);
if (!SECTIONS.length) E(`${TEMPLATE}: no ## sections found`);
if (!SECTIONS.includes(ROADS)) E(`${TEMPLATE}: no "## ${ROADS}" section`);
if (!LABELS.length) E(`${TEMPLATE}: no option labels (- Pros:, ...) under "## ${ROADS}"`);

// ---- the allowed statuses, from the README, cross-checked with the template comment
const readme = existsSync(join(dir, 'README.md')) ? read('README.md') : '';
if (!readme) E('docs/decisions/README.md is missing');
const statusBullet = /^- \*\*Status\.\*\*(.*)$/m.exec(readme)?.[1] ?? '';
const STATUSES = [...statusBullet.matchAll(/`([^`]+)`/g)].map((m) => m[1]);
if (!STATUSES.length) E('README.md: no "- **Status.** One of `...`" bullet with backticked values');
const tplStatuses = (/^Status:\s*(.+?)\.?\s*$/m.exec(template)?.[1] ?? '')
  .split(/,\s*/).map((s) => s.replace(/^or\s+/, '').trim()).filter(Boolean);
if (JSON.stringify([...tplStatuses].sort()) !== JSON.stringify([...STATUSES].sort())) {
  E(`the statuses of README.md (${STATUSES.join(' | ')}) and of ${TEMPLATE}'s comment (${tplStatuses.join(' | ')}) differ`);
}
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const STATUS_RX = STATUSES.map((s) => new RegExp(`^${esc(s).replace(/NNNN/g, '(\\d{4})')}$`));

// The README's "Format" list must name the template's fields and sections, in order.
const format = /^## Format\s*$([\s\S]*?)^## /m.exec(readme)?.[1] ?? '';
const parts = [...format.matchAll(/^- \*\*([^*]+?)\.\*\*/gm)].map((m) => m[1]);
if (JSON.stringify(parts) !== JSON.stringify([...FIELDS, ...SECTIONS])) {
  E(`README.md "Format" lists ${JSON.stringify(parts)}, ${TEMPLATE} has ${JSON.stringify([...FIELDS, ...SECTIONS])}`);
}
if (r.errors.length) r.finish(0, 'records');

// ---- the files
const names = readdirSync(dir).sort();
const records = new Map();
for (const name of names) {
  if (name === 'README.md' || name === TEMPLATE) continue;
  const m = FILE.exec(name);
  if (!m) { E(`docs/decisions/${name}: not NNNN-kebab-case.md (or README.md, ${TEMPLATE})`); continue; }
  if (m[1] === '0000') { E(`docs/decisions/${name}: 0000 is the template's number`); continue; }
  if (records.has(m[1])) E(`docs/decisions/${name}: number ${m[1]} is also used by ${records.get(m[1]).name}`);
  else records.set(m[1], { name, num: m[1] });
}

const isDay = (s) => {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(s)) return false;
  const d = new Date(`${s}T00:00:00Z`);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s;
};

// ---- each record
for (const rec of records.values()) {
  const where = `docs/decisions/${rec.name}`;
  const text = read(rec.name);
  const { head, secs } = sections(text);

  const title = /^# (\d{4}) — \S.*$/.exec(text.split('\n')[0]);
  if (!title) E(`${where}:1: the title must be "# ${rec.num} — <decision>"`);
  else if (title[1] !== rec.num) E(`${where}:1: the title has number ${title[1]}, the file ${rec.num}`);

  const fields = head.map((l) => ({ ...l, m: FIELD.exec(l.line) })).filter((l) => l.m && FIELDS.includes(l.m[1]));
  let last = -1;
  for (const f of FIELDS) {
    const hits = fields.filter((l) => l.m[1] === f);
    if (hits.length !== 1) { E(`${where}: **${f}:** appears ${hits.length} times before the first section, expected once`); continue; }
    const at = fields.indexOf(hits[0]);
    if (at < last) E(`${where}:${hits[0].n}: **${f}:** is out of the template's order (${FIELDS.join(', ')})`);
    last = at;
    rec[f] = hits[0].m[2];
  }
  if (rec.Status !== undefined) {
    const ok = STATUS_RX.map((rx) => rx.exec(rec.Status)).find(Boolean);
    if (!ok) E(`${where}: status "${rec.Status}" is not one of ${STATUSES.join(', ')}`);
    for (const n of ok?.slice(1) ?? []) {
      if (!records.has(n)) E(`${where}: status "${rec.Status}" names record ${n}, which does not exist`);
      if (n === rec.num) E(`${where}: status "${rec.Status}" names the record itself`);
    }
  }
  if (rec.Date !== undefined && !isDay(rec.Date)) E(`${where}: date "${rec.Date}" is not a YYYY-MM-DD day`);

  let prev = -1;
  for (const s of SECTIONS) {
    const hits = secs.filter((x) => x.title === s);
    if (hits.length !== 1) { E(`${where}: "## ${s}" appears ${hits.length} times, expected once`); continue; }
    const at = secs.indexOf(hits[0]);
    if (at < prev) E(`${where}:${hits[0].n}: "## ${s}" is out of the template's order (${SECTIONS.join(', ')})`);
    prev = at;
    if (!hits[0].body.some((l) => l.line.trim() && !/^<!--.*-->$/.test(l.line.trim()))) E(`${where}:${hits[0].n}: "## ${s}" is empty`);
  }

  const roads = secs.find((x) => x.title === ROADS);
  if (roads) {
    const options = [];
    for (const l of roads.body) {
      const h = /^### (.+)$/.exec(l.line);
      if (h) options.push({ title: h[1], n: l.n, text: '' });
      else if (options.length) options[options.length - 1].text += `${l.line}\n`;
    }
    if (!options.length) E(`${where}:${roads.n}: "## ${ROADS}" has no "### <option>"; every other option weighed is listed`);
    const qual = `(?:\\s+\\([^()\\n]{1,40}\\)|${QUALIFIERS.map((q) => `\\s+${esc(q)}`).join('|')})?`;
    for (const o of options) {
      const body = o.text.split('\n');
      for (const label of LABELS) {
        const rx = new RegExp(`^\\s*-\\s+(?:\\*\\*)?${esc(label)}${qual}(?:\\*\\*)?:(?:\\*\\*)?(.*)$`);
        const at = body.findIndex((l) => rx.test(l));
        if (at < 0) { E(`${where}:${o.n}: option "${o.title}" has no "- ${label}:" bullet`); continue; }
        const value = rx.exec(body[at])[1].trim();
        if (!value && !/^\s+\S/.test(body[at + 1] ?? '')) E(`${where}:${o.n}: option "${o.title}" has an empty "${label}:"`);
      }
    }
  }

  if (tplComment.some((l) => text.includes(l))) E(`${where}: still has a line of the template's instructions comment`);
}

// ---- the index
const idx = /^## Index\s*$([\s\S]*?)(?=^## |(?![\s\S]))/m.exec(readme);
if (!idx) E('README.md: no "## Index" section');
const rows = new Map();
const indexStart = readme.slice(0, idx?.index ?? 0).split('\n').length;
(idx?.[1] ?? '').split('\n').forEach((line, i) => {
  if (!line.startsWith('|') || /^\|\s*-/.test(line) || /^\|\s*ADR\s*\|/.test(line)) return;
  const n = indexStart + i;
  const cells = line.replace(/^\|/, '').replace(/\|\s*$/, '').split('|').map((c) => c.trim());
  const link = /^\[(\d{4})\]\(([^)#\s]+)\)$/.exec(cells[0] ?? '');
  if (cells.length !== 3 || !link) { E(`README.md:${n}: an index row is "| [NNNN](NNNN-slug.md) | decision | status |"`); return; }
  const [, num, target] = link;
  if (rows.has(num)) E(`README.md:${n}: a second index row for ${num}`);
  rows.set(num, { n, target, status: cells[2] });
  const rec = records.get(num);
  if (!rec) { E(`README.md:${n}: index row ${num} has no record in docs/decisions/`); return; }
  if (target !== rec.name) E(`README.md:${n}: index row ${num} links ${target}, the file is ${rec.name}`);
  if (rec.Status !== undefined && cells[2] !== rec.Status) E(`README.md:${n}: index row ${num} says "${cells[2]}", the record says "${rec.Status}"`);
});
for (const rec of records.values()) {
  if (!rows.has(rec.num)) E(`docs/decisions/${rec.name}: no row in the README index`);
}

r.finish(records.size, `records (${SECTIONS.length} sections, ${STATUSES.length} statuses from the template and README)`);
