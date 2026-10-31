#!/usr/bin/env node
// The security headers of the built site (public/_headers, copied to the build
// output) against the pages they protect. A static `_headers` file cannot compute
// the hash of an inline script, so this check does: every inline script that a v2
// page runs must have its sha256 in the script-src of the `/*` rule, and every
// hash in the rule must still be used. A Starlight or Astro upgrade that changes
// an inline script fails here with the line to paste, instead of shipping a page
// whose theme or search script the browser silently refuses to run.
//
//   site/scripts/check-csp.mjs [dist]          check (default dist: site/dist)
//   site/scripts/check-csp.mjs [dist] --print  print the sha256 tokens in use, sorted
//
// Also checked, each failing closed (an empty sweep is a failure):
//   - the headers every response must carry are in the `/*` rule;
//   - no v2 page has an inline event handler or a javascript: URL (the CSP blocks them);
//   - the /v1/* rule detaches the strict policy and sets its own: the v1 documentation
//     is MkDocs output with a different inline script on almost every page;
//   - no line of the file is longer than 2000 characters (Cloudflare drops longer ones).
import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const args = process.argv.slice(2);
const print = args.includes('--print');
const dist = resolve(root, args.find((a) => !a.startsWith('--')) ?? 'site/dist');

const problems = [];
const bad = (m) => problems.push(m);

// The inline scripts a browser executes. Data blocks are not governed by script-src.
const DATA_TYPES = /^(application\/(ld\+)?json|importmap|speculationrules|text\/(template|html|plain))$/i;
const inlineScripts = (html) => {
  const out = [];
  for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)) {
    if (/\bsrc\s*=/i.test(m[1])) continue;
    const type = /\btype\s*=\s*"([^"]*)"/i.exec(m[1])?.[1];
    if (type && DATA_TYPES.test(type.trim())) continue;
    out.push(m[2]);
  }
  return out;
};
const sha256 = (s) => `'sha256-${createHash('sha256').update(s, 'utf8').digest('base64')}'`;

const pages = [];
const walk = (dir) => {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const f = join(dir, e.name);
    if (e.isDirectory()) walk(f);
    else if (e.name.endsWith('.html')) pages.push(f);
  }
};
walk(dist);
const v2 = pages.filter((f) => !relative(dist, f).split(sep).join('/').startsWith('v1/'));
if (v2.length < 50) bad(`only ${v2.length} v2 pages under ${dist}; is this a built site?`);

const used = new Map(); // token -> first page
for (const f of v2) {
  const html = readFileSync(f, 'utf8');
  const rel = relative(dist, f);
  for (const body of inlineScripts(html)) if (!used.has(sha256(body))) used.set(sha256(body), rel);
  if (/<[a-z][^>]*\son[a-z]+\s*=/i.test(html)) bad(`${rel}: an inline event handler; the CSP blocks it. Use addEventListener.`);
  if (/\b(href|src|action)\s*=\s*"\s*javascript:/i.test(html)) bad(`${rel}: a javascript: URL; the CSP blocks it`);
}
if (!used.size) bad('no inline script found in any v2 page; the sweep checked nothing');
if (print) {
  console.log([...used.keys()].sort().join(' '));
  process.exit(0);
}

const file = join(dist, '_headers');
if (!existsSync(file)) {
  console.error(`check-csp: no ${file}; site/public/_headers is missing`);
  process.exit(1);
}
const text = readFileSync(file, 'utf8');
text.split('\n').forEach((l, i) => { if (l.length > 2000) bad(`_headers line ${i + 1} is ${l.length} characters; Cloudflare ignores lines over 2000`); });

// Sections: a path line at column 0, then indented header lines.
const sections = new Map();
let cur = null;
for (const line of text.split('\n')) {
  if (!line.trim() || line.trimStart().startsWith('#')) continue;
  if (!/^\s/.test(line)) { cur = line.trim(); sections.set(cur, []); } else if (cur) sections.get(cur).push(line.trim());
}
const header = (sec, name) => sections.get(sec)?.find((l) => l.toLowerCase().startsWith(name.toLowerCase() + ':'))?.slice(name.length + 1).trim();

const all = sections.get('/*');
if (!all) bad('_headers has no /* rule');
else {
  for (const h of ['Content-Security-Policy', 'X-Content-Type-Options', 'X-Frame-Options', 'Referrer-Policy', 'Permissions-Policy']) {
    if (!header('/*', h)) bad(`the /* rule sets no ${h}`);
  }
  const csp = header('/*', 'Content-Security-Policy') ?? '';
  const dir = (n) => csp.split(';').map((d) => d.trim()).find((d) => d.split(/\s+/)[0] === n);
  for (const [n, want] of [['frame-ancestors', "'none'"], ['object-src', "'none'"], ['base-uri', "'self'"], ['default-src', "'self'"]]) {
    if (!dir(n)?.split(/\s+/).slice(1).includes(want)) bad(`the CSP lacks ${n} ${want}`);
  }
  const script = dir('script-src') ?? '';
  if (/'unsafe-inline'|'unsafe-eval'|\*/.test(script)) bad(`the v2 script-src allows unsafe-inline, unsafe-eval or a wildcard: ${script.slice(0, 80)}`);
  const listed = new Set(script.split(/\s+/).filter((t) => t.startsWith("'sha256-")));
  for (const [t, page] of used) if (!listed.has(t)) bad(`script-src lacks ${t} (inline script on ${page})`);
  for (const t of listed) if (!used.has(t)) bad(`script-src lists ${t}, which no v2 page uses any more; remove it`);
}
const v1 = sections.get('/v1/*');
if (!v1) bad('_headers has no /v1/* rule');
else {
  if (!v1.some((l) => /^!\s*Content-Security-Policy\s*$/i.test(l))) bad('the /v1/* rule does not detach the strict Content-Security-Policy ("! Content-Security-Policy")');
  const c = header('/v1/*', 'Content-Security-Policy');
  if (!c) bad('the /v1/* rule sets no Content-Security-Policy of its own');
  else if (!/frame-ancestors\s+'none'/.test(c) || !/object-src\s+'none'/.test(c)) bad("the /v1/* policy lacks frame-ancestors 'none' or object-src 'none'");
}

if (problems.length) {
  for (const p of problems) console.error(`check-csp: ${p}`);
  console.error(`check-csp: the sha256 tokens in use, to paste into script-src of the /* rule in site/public/_headers:\n${[...used.keys()].sort().join(' ')}`);
  process.exit(1);
}
console.log(`check-csp: ok (${v2.length} v2 pages, ${used.size} inline-script hashes, ${pages.length - v2.length} v1 pages under their own rule)`);
