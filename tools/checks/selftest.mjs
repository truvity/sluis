#!/usr/bin/env node
// Self-test for the checks in tools/checks/: each check runs against scratch repositories that
// must pass or must fail. A check that passes everything is not a check.
//
// The fixtures are built here at run time, so no token-shaped string or deny-listed term is
// committed; the names are invented (examplecorp).
//
// usage: tools/checks/selftest.mjs
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..', '..');
const scratch = mkdtempSync(join(tmpdir(), 'sluis-checks-'));
let failures = 0;
let cases = 0;

// A git repository holding `files` (path -> content).
function fixture(files) {
  const dir = mkdtempSync(join(scratch, 'repo-'));
  for (const [path, text] of Object.entries(files)) {
    if (text === undefined) continue;
    mkdirSync(dirname(join(dir, path)), { recursive: true });
    writeFileSync(join(dir, path), text);
  }
  execFileSync('git', ['init', '-q', dir]);
  execFileSync('git', ['-C', dir, 'add', '-A']);
  return dir;
}

// Run `check` on `files`; `want` is 'pass' or 'fail'; `has`/`lacks` are strings the output must
// or must not contain (case-insensitive).
function expect(name, check, files, want, { env = {}, has = [], lacks = [] } = {}) {
  cases++;
  const dir = fixture(files);
  const res = spawnSync(process.execPath, [join(here, `${check}.mjs`), dir], {
    encoding: 'utf8',
    env: { ...process.env, PUBLIC_CONTENT_DENYLIST: '', ...env },
  });
  const out = `${res.stdout}${res.stderr}`;
  const got = res.status === 0 ? 'pass' : 'fail';
  const problems = [];
  if (got !== want) problems.push(`expected ${want}, got ${got}`);
  for (const s of has) if (!out.toLowerCase().includes(s.toLowerCase())) problems.push(`output lacks "${s}"`);
  for (const s of lacks) if (out.toLowerCase().includes(s.toLowerCase())) problems.push(`output contains "${s}"`);
  if (problems.length) {
    failures++;
    console.error(`FAIL ${check}: ${name}: ${problems.join('; ')}\n${out.replace(/^/gm, '  | ')}`);
  } else console.log(`ok   ${check}: ${name}`);
}

// ---- C1 decisions: the real template and README, with an index of one record
const template = readFileSync(join(repo, 'docs/decisions/0000-template.md'), 'utf8');
const readme = readFileSync(join(repo, 'docs/decisions/README.md'), 'utf8')
  .replace(/(## Index\n)[\s\S]*$/, '$1\n| ADR | Decision | Status |\n|-----|----------|--------|\n');
const row = (n, slug, status) => `| [${n}](${n}-${slug}.md) | A decision | ${status} |\n`;
const record = ({ num = '0001', status = 'Accepted', date = '2026-10-31', drop = '', why = 'Why not' } = {}) =>
  [
    `# ${num} — We use the example`, '', `**Status:** ${status}`, `**Date:** ${date}`, '',
    ...['Context', 'Decision', 'Consequences'].filter((s) => s !== drop).flatMap((s) => [`## ${s}`, '', 'Text.', '']),
    '## Roads not taken', '', '### Another way', '', '- Pros: one.', '- Cons: two.', `- ${why}: three.`, '',
  ].join('\n');
const adr = (extra = {}, idx = row('0001', 'we-use-the-example', 'Accepted')) => ({
  'docs/decisions/0000-template.md': template,
  'docs/decisions/README.md': readme + idx,
  'docs/decisions/0001-we-use-the-example.md': record(),
  ...extra,
});
const one = (opts) => ({ 'docs/decisions/0001-we-use-the-example.md': record(opts) });
expect('a good record', 'decisions', adr(), 'pass', { has: ['1 records'] });
expect('a qualified label', 'decisions', adr(one({ why: 'Why not (inferred)' })), 'pass');
expect('no records', 'decisions', { ...adr({}, ''), 'docs/decisions/0001-we-use-the-example.md': undefined }, 'fail', { has: ['empty sweep'] });
expect('a missing section', 'decisions', adr(one({ drop: 'Consequences' })), 'fail', { has: ['## Consequences'] });
expect('an unknown status', 'decisions', adr(one({ status: 'Maybe' })), 'fail', { has: ['not one of'] });
expect('superseded by a missing record', 'decisions', adr(one({ status: 'Superseded by 0099' }), row('0001', 'we-use-the-example', 'Superseded by 0099')), 'fail', { has: ['0099'] });
expect('a date that is no day', 'decisions', adr(one({ date: '2026-02-30' })), 'fail', { has: ['YYYY-MM-DD'] });
expect('an option without a reason', 'decisions', adr(one({ why: 'Because' })), 'fail', { has: ['Why not:'] });
expect('a title with another number', 'decisions', adr(one({ num: '0002' })), 'fail', { has: ['number 0002'] });
expect('an index status that differs', 'decisions', adr({}, row('0001', 'we-use-the-example', 'Proposed')), 'fail', { has: ['says "Proposed"'] });
expect('a record not in the index', 'decisions', adr({}, ''), 'fail', { has: ['no row in the README index'] });
expect('an index row without a file', 'decisions', adr({}, row('0001', 'we-use-the-example', 'Accepted') + row('0003', 'gone', 'Accepted')), 'fail', { has: ['0003 has no record'] });
expect('a file name that is not kebab-case', 'decisions', adr({ 'docs/decisions/0004_Bad.md': record({ num: '0004' }) }), 'fail', { has: ['0004_Bad.md'] });
expect('two records with one number', 'decisions', adr({ 'docs/decisions/0001-again.md': record() }), 'fail', { has: ['also used by'] });

// ---- C4 public content. The token-shaped strings are assembled here, not written out.
const pub = (text) => ({ 'README.md': `# Example\n\n${text}\n` });
const pem = `${'-'.repeat(5)}BEGIN EC PRIVATE KEY${'-'.repeat(5)}`;
expect('clean text', 'public-content', pub('Use sluis.example.com and account 123456789012.'), 'pass', { has: ['private deny-list not available, skipped'] });
expect('an account-id-shaped number', 'public-content', pub(`arn:aws:iam::${'48392017'}${'4655'}:role/x`), 'fail', { has: ['README.md:3', 'account-id-shaped'], lacks: ['4655'] });
expect('a private key block', 'public-content', pub(pem), 'fail', { has: ['private key block'] });
expect('a GitHub token', 'public-content', pub(`token ${'ghp'}_${'a1B2'.repeat(9)}`), 'fail', { has: ['GitHub token'], lacks: ['a1B2'] });
expect('an AWS access key id', 'public-content', pub(`${'AK'}IA${'Q'.repeat(16)}`), 'fail', { has: ['AWS access key id'] });
expect('an sk- key', 'public-content', pub(`${'sk'}-proj-${'x9'.repeat(12)}`), 'fail', { has: ['sk- secret key'] });
expect('FIDO SSH key types are not tokens', 'public-content', pub('sk-ssh-ed25519@openssh.com and sk-ecdsa-sha2-nistp256-cert-v01@openssh.com'), 'pass');
expect('a deny-listed term', 'public-content', pub('Deployed at ExampleCorp in 2026.'), 'fail',
  { env: { PUBLIC_CONTENT_DENYLIST: '# comment\n\nexamplecorp\nglobexample\n' }, has: ['README.md:3: matches a deny-listed term'], lacks: ['examplecorp', 'globexample', 'Deployed'] });
expect('a deny-listed term in a path', 'public-content', { 'docs/examplecorp/notes.md': 'Text.\n' }, 'fail',
  { env: { PUBLIC_CONTENT_DENYLIST: 'examplecorp' }, has: ['the path matches a deny-listed term'] });
expect('a term inside a longer word is no match', 'public-content', pub('The examplecorpus is fine.'), 'pass',
  { env: { PUBLIC_CONTENT_DENYLIST: 'examplecorp' }, has: ['private deny-list loaded'] });
expect('nothing to scan', 'public-content', {}, 'fail', { has: ['empty sweep'] });

// ---- C5 YAML extension
expect('.yaml files', 'yaml-ext', { 'a.yaml': 'a: 1\n' }, 'pass');
expect('the files tools require', 'yaml-ext', { '.github/ISSUE_TEMPLATE/config.yml': 'blank_issues_enabled: false\n', 'site/.yarnrc.yml': 'nodeLinker: node-modules\n' }, 'pass');
expect('a .yml file', 'yaml-ext', { 'a.yaml': 'a: 1\n', 'deploy/values.yml': 'a: 1\n' }, 'fail', { has: ['deploy/values.yml'] });
expect('no YAML at all', 'yaml-ext', { 'README.md': 'x\n' }, 'fail', { has: ['empty sweep'] });

// ---- C6 agent links
const docs = { 'docs/contributing/index.md': 'x\n', Justfile: 'default:\n' };
expect('no agent files yet', 'agent-links', docs, 'pass', { has: ['skipped'] });
expect('every pointer exists', 'agent-links', {
  ...docs,
  'AGENTS.md': 'Read [the guide](docs/contributing/index.md#start), run `just site-check` from the `Justfile`.\nPush `origin/main`, see `docs/requirements/<area>.yaml` and https://example.com/x.\n```\n[not a link](docs/missing.md)\n```\n',
  'CLAUDE.md': '@AGENTS.md\n',
  '.claude/skills/example/SKILL.md': 'See [the guide](../../../docs/contributing/index.md) and `docs/contributing/`.\n',
}, 'pass', { has: ['5 pointers in 3 agent files'] });
expect('a link to a missing file', 'agent-links', { ...docs, 'AGENTS.md': 'Read [this](docs/contributing/gone.md).\n' }, 'fail', { has: ['AGENTS.md:1: docs/contributing/gone.md does not exist'] });
expect('a code path to a missing file', 'agent-links', { ...docs, '.claude/skills/example/SKILL.md': 'Edit `docs/contributing/gone.md`.\n' }, 'fail', { has: ['gone.md does not exist'] });
expect('an agent file with no pointer', 'agent-links', { ...docs, 'AGENTS.md': 'Be careful.\n' }, 'fail', { has: ['empty sweep'] });

rmSync(scratch, { recursive: true, force: true });
if (failures) {
  console.error(`selftest: ${failures} of ${cases} cases failed`);
  process.exit(1);
}
console.log(`selftest: ${cases} cases, all as expected`);
