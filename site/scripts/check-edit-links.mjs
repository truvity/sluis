#!/usr/bin/env node
// Every "Edit page" link in the built site points at a file that exists in this repository.
// Starlight builds the link from `editLink.baseUrl` plus the entry's path under the site
// directory, which is not where the file is (the docs are the repository's docs/, linked at
// site/src/content/docs): the link was .../edit/main/docs/src/content/docs/principles.md for
// docs/principles.md, a 404 on every page. src/route-middleware.ts rewrites it; this is the
// guard. A local check against the checkout, no network.
//
// For each page in the built site (the v1 documentation under v1/ is another tool's output):
//   - an edit link, if the page has one, is https://github.com/<owner>/<repo>/edit/main/<path>
//     and <path> is a file in this checkout;
//   - a page whose link is under docs/ (and not a generated one, requirements/) is the page
//     for that file: docs/<page>.md or .mdx, or docs/<page>/index.md or .mdx.
// Fails closed: the home page must have a link, and so must at least ten pages.
//
// usage: site/scripts/check-edit-links.mjs [dist]   (dist relative to the repository root, default site/dist)
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const dist = resolve(root, process.argv[2] ?? 'site/dist');
const errors = [];
const E = (m) => errors.push(m);

if (!existsSync(dist)) {
  console.error(`check-edit-links: no ${dist}`);
  process.exit(1);
}

function* pages(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) {
      if (dir === dist && e.name === 'v1') continue;
      yield* pages(p);
    } else if (e.name === 'index.html') yield p;
  }
}

const isFile = (p) => existsSync(p) && statSync(p).isFile();
const EDIT = /href="(https:\/\/github\.com\/[^/"]+\/[^/"]+\/edit\/main\/([^"#?]*))"/g;
let withLink = 0;
let homeLinked = false;

for (const file of pages(dist)) {
  const page = relative(dist, dirname(file)); // '' for the home page
  const html = readFileSync(file, 'utf8');
  const links = [...html.matchAll(EDIT)];
  const label = page === '' ? '/' : `/${page}/`;
  if (links.length > 1) E(`${label}: ${links.length} edit links`);
  if (links.length === 0) continue;
  withLink++;
  if (page === '') homeLinked = true;
  const [, url, encoded] = links[0];
  const path = decodeURIComponent(encoded);
  if (!isFile(join(root, path))) {
    E(`${label}: ${url} is not a file in this repository (${path})`);
    continue;
  }
  if (path.startsWith('docs/') && !path.startsWith('docs/requirements/')) {
    const stem = path.slice('docs/'.length).replace(/\.(md|mdx)$/, '');
    const expected = [stem.replace(/(^|\/)index$/, ''), stem].map((s) => s.replace(/^\/|\/$/g, ''));
    if (!/\.(md|mdx)$/.test(path) || !expected.includes(page)) {
      E(`${label}: its edit link ${url} is not the source of this page`);
    }
  }
}

if (!homeLinked) E('the home page has no edit link');
if (withLink < 10) E(`only ${withLink} pages have an edit link; an empty sweep proves nothing`);

if (errors.length) {
  for (const m of errors) console.error(`check-edit-links: ${m}`);
  process.exit(1);
}
console.log(`check-edit-links: ${withLink} pages, every edit link is a file in the repository`);
