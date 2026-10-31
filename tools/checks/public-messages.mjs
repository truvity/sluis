#!/usr/bin/env node
// Public text outside the files: the commit messages a pull request adds and its title and body
// become public too. Each is scanned with the same matchers as public-content.mjs (the generic
// patterns and the private deny-list), with the same output rules: where the hit is and what
// kind it is, never the value, the term or the line.
//
// The commits are BASE..HEAD, with BASE from the environment (default origin/main). The title and
// body come from the environment variables PR_TITLE and PR_BODY, which CI sets from the event
// with `env:`, so the text is never part of a shell command. Finding nothing at all to scan (no
// commit and no title) is a failure.
//
// usage: tools/checks/public-messages.mjs [repo-root]
import { execFileSync } from 'node:child_process';
import { denyList, genericHits, reporter, repoRoot } from './lib.mjs';

const root = repoRoot();
const r = reporter('check-messages');
const deny = denyList('check-messages', r.error);
const base = process.env.BASE || 'origin/main';

const scan = (where, text) => {
  text.split('\n').forEach((line, i) => {
    for (const h of genericHits(line)) r.error(`${where}, line ${i + 1}: ${h.kind}`);
    if (deny?.test(line)) r.error(`${where}, line ${i + 1}: matches a deny-listed term`);
  });
};

let scanned = 0;
const log = execFileSync('git', ['-C', root, 'log', '-z', '--format=%H%x00%B', `${base}..HEAD`], { encoding: 'utf8', maxBuffer: 64 << 20 });
const parts = log.split('\0');
for (let k = 0; k + 1 < parts.length; k += 2) {
  const sha = parts[k].trim();
  if (!sha) continue;
  scan(`commit ${sha.slice(0, 12)}`, parts[k + 1]);
  scanned++;
}
for (const [name, label] of [['PR_TITLE', 'pull request title'], ['PR_BODY', 'pull request body']]) {
  const text = process.env[name];
  if (text) { scan(label, text); scanned++; }
}

r.finish(scanned, `commit messages and pull request texts (${base}..HEAD)`);
