// Shared helpers for the repository rule checks in tools/checks/. No dependency beyond Node.
//
// Every check takes an optional repository root as its first argument (the self-test points
// it at a scratch repository) and defaults to the root of this checkout.
import { execFileSync } from 'node:child_process';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const repoRoot = (argv = process.argv.slice(2)) =>
  resolve(argv.find((a) => !a.startsWith('--')) ?? join(dirname(fileURLToPath(import.meta.url)), '..', '..'));

// The files git tracks, as paths relative to the root.
export function trackedFiles(root) {
  const out = execFileSync('git', ['-C', root, 'ls-files', '-z'], { encoding: 'utf8', maxBuffer: 64 << 20 });
  return out.split('\0').filter(Boolean);
}

// One reporter per check: errors fail the run, an empty sweep fails the run.
export function reporter(name) {
  const errors = [];
  return {
    error: (m) => errors.push(m),
    errors,
    // `checked` is the number of things the check looked at; zero is a failure, not a pass.
    finish(checked, what) {
      for (const m of errors) console.error(`error: ${m}`);
      if (errors.length) {
        console.error(`${name}: ${errors.length} errors`);
        process.exit(1);
      }
      if (!checked) {
        console.error(`${name}: no ${what} found; an empty sweep proves nothing`);
        process.exit(1);
      }
      console.log(`${name}: ${checked} ${what}, no errors`);
      process.exit(0);
    },
  };
}

// ---- public-content matchers, shared by public-content.mjs and public-messages.mjs

// AWS's own documentation placeholders, and all zeros.
const PLACEHOLDER_ACCOUNTS = new Set(['123456789012', '000000000000', '111122223333', '444455556666']);

// A run of base64 characters around position `at` that is long and mixed, as in an encoded key or
// a lockfile hash: digits inside it are not an account id.
function inBase64(line, at, len) {
  let a = at;
  let b = at + len;
  while (a > 0 && /[A-Za-z0-9+/=]/.test(line[a - 1])) a--;
  while (b < line.length && /[A-Za-z0-9+/=]/.test(line[b])) b++;
  const run = line.slice(a, b);
  return run.length >= 24 && /[A-Z]/.test(run) && /[a-z]/.test(run);
}
// The last group of a UUID (8-4-4-4-12) can be digits only.
const inUuid = (line, at) => /[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-$/.test(line.slice(Math.max(0, at - 24), at));

// [pattern, kind, keep(match, line)]. Written so that this file does not match itself.
export const GENERIC = [
  [/(?<![0-9A-Za-z_])[0-9]{12}(?![0-9A-Za-z_])/g, 'a 12-digit account-id-shaped number',
    (m, line) => !PLACEHOLDER_ACCOUNTS.has(m[0]) && !inUuid(line, m.index) && !inBase64(line, m.index, m[0].length)],
  [/(?<![0-9A-Za-z_-])[0-9]{4}-[0-9]{4}-[0-9]{4}(?![0-9A-Za-z_-])/g, 'a dashed account-id-shaped number',
    (m) => !PLACEHOLDER_ACCOUNTS.has(m[0].replaceAll('-', ''))],
  [/-{5}BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-{5}/g, 'a private key block'],
  [/\bAGE-SECRET-KEY-1[0-9A-Z]{50,}/g, 'an age secret key'],
  [/\bgh[pousr]_[A-Za-z0-9]{36,}/g, 'a GitHub token'],
  [/\bgithub_pat_[A-Za-z0-9_]{22,}/g, 'a GitHub fine-grained token'],
  [/\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/g, 'an AWS access key id'],
  [/\bxox[abposr]-[A-Za-z0-9-]{10,}/g, 'a Slack token'],
  [/\bglpat-[A-Za-z0-9_-]{20,}/g, 'a GitLab token'],
  [/\bAIza[0-9A-Za-z_-]{35}/g, 'a Google API key'],
  [/\b[sr]k_live_[A-Za-z0-9]{16,}/g, 'a Stripe live key'],
  [/\bnpm_[A-Za-z0-9]{36}\b/g, 'an npm token'],
  [/\bhvs\.[A-Za-z0-9_-]{24,}/g, 'a Vault token'],
  // An sk- key has a digit in it; the FIDO SSH key types sk-ssh-* and sk-ecdsa-* are not keys.
  [/\bsk-(?!ssh-|ecdsa-)(?=[A-Za-z0-9_-]*[0-9])[A-Za-z0-9_-]{20,}/g, 'an sk- secret key'],
];

// The kinds of the generic hits in one line, with their 1-based columns.
export function genericHits(line) {
  const out = [];
  for (const [rx, kind, keep = () => true] of GENERIC) {
    for (const m of line.matchAll(rx)) if (keep(m, line)) out.push({ col: m.index + 1, kind });
  }
  return out;
}

// The private deny-list: PUBLIC_CONTENT_DENYLIST holds one term per line; empty lines and lines
// starting with # are ignored. A term matches case-insensitively when no letter or digit touches
// it. Returns a RegExp, or null after printing that the part is skipped. When REQUIRE_DENYLIST is
// "true" (CI on this repository's own branches, once the maintainer sets the repository variable
// DENYLIST_REQUIRED), an empty list is an error instead.
export function denyList(name, error) {
  const terms = (process.env.PUBLIC_CONTENT_DENYLIST ?? '')
    .split(/\r?\n/).map((t) => t.trim()).filter((t) => t && !t.startsWith('#'));
  if (!terms.length) {
    if (process.env.REQUIRE_DENYLIST === 'true') error('the private deny-list is required here (REQUIRE_DENYLIST=true) but PUBLIC_CONTENT_DENYLIST is empty');
    else console.log('private deny-list not available, skipped');
    return null;
  }
  console.log(`${name}: private deny-list loaded`);
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return new RegExp(`(?<![\\p{L}\\p{N}])(?:${terms.map(esc).join('|')})(?![\\p{L}\\p{N}])`, 'iu');
}
