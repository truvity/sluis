#!/usr/bin/env node
// Public content: this is a public repository, so no tracked file may carry a secret, a real
// account id or a private name.
//
// Generic part (the patterns are public, they name nothing):
//   - a 12-digit number shaped like an AWS account id, other than the documented placeholders
//     below (use 123456789012 in examples);
//   - a PEM private key block;
//   - a token with a well-known prefix: GitHub (ghp_, gho_, ghu_, ghs_, ghr_, github_pat_),
//     AWS access key ids (AKIA, ASIA), Slack (xox?-), GitLab (glpat-), Google API keys (AIza),
//     and sk- secret keys (the FIDO SSH key types sk-ssh-* and sk-ecdsa-* are not tokens).
//   A hit prints the file, the line and the kind, never the value.
//
// Private part: the environment variable PUBLIC_CONTENT_DENYLIST holds a newline-separated list
// of terms (a repository secret; empty lines and lines starting with # are ignored). A term
// matches case-insensitively when no letter or digit touches it on either side, in a file's text
// or in its path. A hit prints the file and line and "matches a deny-listed term", never the term
// or the line. Without the variable this part prints one line and is skipped.
//
// Every tracked text file is scanned; symbolic links and binary files (a NUL byte) are not.
//
// usage: tools/checks/public-content.mjs [repo-root]
import { lstatSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { reporter, repoRoot, trackedFiles } from './lib.mjs';

const root = repoRoot();
const r = reporter('check-public');

// AWS's own documentation placeholders, and all zeros.
const PLACEHOLDER_ACCOUNTS = new Set(['123456789012', '000000000000', '111122223333', '444455556666']);

// Written so that this file does not match itself.
const GENERIC = [
  [/(?<![0-9A-Za-z_])[0-9]{12}(?![0-9A-Za-z_])/g, 'a 12-digit account-id-shaped number', (m) => !PLACEHOLDER_ACCOUNTS.has(m)],
  [/-{5}BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-{5}/g, 'a private key block'],
  [/\bgh[pousr]_[A-Za-z0-9]{36,}/g, 'a GitHub token'],
  [/\bgithub_pat_[A-Za-z0-9_]{22,}/g, 'a GitHub fine-grained token'],
  [/\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/g, 'an AWS access key id'],
  [/\bxox[abposr]-[A-Za-z0-9-]{10,}/g, 'a Slack token'],
  [/\bglpat-[A-Za-z0-9_-]{20,}/g, 'a GitLab token'],
  [/\bAIza[0-9A-Za-z_-]{35}/g, 'a Google API key'],
  [/\bsk-(?!ssh-|ecdsa-)[A-Za-z0-9_-]{20,}/g, 'an sk- secret key'],
];

const terms = (process.env.PUBLIC_CONTENT_DENYLIST ?? '')
  .split(/\r?\n/).map((t) => t.trim()).filter((t) => t && !t.startsWith('#'));
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const deny = terms.length
  ? new RegExp(`(?<![\\p{L}\\p{N}])(?:${terms.map(esc).join('|')})(?![\\p{L}\\p{N}])`, 'iu')
  : null;
console.log(deny ? 'check-public: private deny-list loaded' : 'private deny-list not available, skipped');

let scanned = 0;
for (const path of trackedFiles(root)) {
  const abs = join(root, path);
  let st;
  try { st = lstatSync(abs); } catch { continue; } // deleted in the working tree
  if (!st.isFile()) continue;
  if (deny?.test(path)) r.error(`${path}: the path matches a deny-listed term`);
  const buf = readFileSync(abs);
  if (buf.subarray(0, 8000).includes(0)) continue;
  scanned++;
  buf.toString('utf8').split('\n').forEach((line, i) => {
    for (const [rx, what, keep = () => true] of GENERIC) {
      for (const m of line.matchAll(rx)) {
        if (keep(m[0])) r.error(`${path}:${i + 1}:${m.index + 1}: ${what}`);
      }
    }
    if (deny?.test(line)) r.error(`${path}:${i + 1}: matches a deny-listed term`);
  });
}

r.finish(scanned, 'tracked text files');
