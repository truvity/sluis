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

// A git repository holding `files` (path -> content), and the `commits` (messages) made on top
// of a first commit on the branch `base`. Scratch commits are unsigned: they never leave this machine.
function fixture(files, commits = []) {
  const dir = mkdtempSync(join(scratch, 'repo-'));
  for (const [path, text] of Object.entries(files)) {
    if (text === undefined) continue;
    mkdirSync(dirname(join(dir, path)), { recursive: true });
    writeFileSync(join(dir, path), text);
  }
  execFileSync('git', ['init', '-q', dir]);
  execFileSync('git', ['-C', dir, 'add', '-A']);
  if (commits.length) {
    const git = (...a) => execFileSync('git', ['-C', dir, '-c', 'user.name=Example', '-c', 'user.email=dev@example.com', '-c', 'commit.gpgsign=false', ...a]);
    git('commit', '-q', '--allow-empty', '-m', 'base');
    git('branch', 'base');
    for (const m of commits) git('commit', '-q', '--allow-empty', '-m', m);
  }
  return dir;
}

// Run `check` on `files`; `want` is 'pass' or 'fail'; `has`/`lacks` are strings the output must
// or must not contain (case-insensitive).
function expect(name, check, files, want, { env = {}, has = [], lacks = [], commits = [], script = join(here, `${check}.mjs`), target = '' } = {}) {
  cases++;
  const dir = fixture(files, commits);
  const res = spawnSync(process.execPath, [script, join(dir, target)], {
    encoding: 'utf8',
    env: { ...process.env, PUBLIC_CONTENT_DENYLIST: '', REQUIRE_DENYLIST: '', PR_TITLE: '', PR_BODY: '', BASE: 'base', ...env },
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
const GOOD = ['- Pros: one.', '- Cons: two.', '- Why not: three.'];
const record = ({ num = '0001', status = 'Accepted', date = '2026-10-31', drop = '', roads = GOOD, extra = [] } = {}) =>
  [
    `# ${num} — We use the example`, '', `**Status:** ${status}`, `**Date:** ${date}`, '', ...extra,
    ...['Context', 'Decision', 'Consequences'].filter((s) => s !== drop).flatMap((s) => [`## ${s}`, '', 'Text.', '']),
    '## Roads not taken', '', '### Another way', '', ...roads, '',
  ].join('\n');
const why = (label) => ['- Pros: one.', '- Cons: two.', `- ${label}: three.`];
const adr = (extra = {}, idx = row('0001', 'we-use-the-example', 'Accepted')) => ({
  'docs/decisions/0000-template.md': template,
  'docs/decisions/README.md': readme + idx,
  'docs/decisions/0001-we-use-the-example.md': record(),
  ...extra,
});
const one = (opts) => ({ 'docs/decisions/0001-we-use-the-example.md': record(opts) });
expect('a good record', 'decisions', adr(), 'pass', { has: ['1 records'] });
expect('a label with a note in parentheses', 'decisions', adr(one({ roads: why('Why not (inferred)') })), 'pass');
expect('a label with an allowed qualifier', 'decisions', adr(one({ roads: why('Why not first') })), 'pass');
expect('bold labels, colon inside or outside', 'decisions', adr(one({ roads: ['- **Pros:** one.', '- **Cons**: two.', '- Why not:', '  three, on the next line.'] })), 'pass');
expect('a label with another qualifier', 'decisions', adr(one({ roads: why('Why not really') })), 'fail', { has: ['no "- Why not:" bullet'] });
expect('an empty label', 'decisions', adr(one({ roads: ['- Pros:', '- Cons: two.', '- Why not: three.'] })), 'fail', { has: ['empty "Pros:"'] });
expect('a label in prose, not a bullet', 'decisions', adr(one({ roads: ['- One way. Pros: one. Cons: two.', '- Why not: three.'] })), 'fail', { has: ['no "- Pros:" bullet'] });
expect('a line of the template comment left in', 'decisions', adr(one({ extra: [template.split('\n').find((l) => /^Keep the text public/.test(l)), ''] })), 'fail', { has: ["template's instructions comment"] });
expect('no records', 'decisions', { ...adr({}, ''), 'docs/decisions/0001-we-use-the-example.md': undefined }, 'fail', { has: ['empty sweep'] });
expect('a missing section', 'decisions', adr(one({ drop: 'Consequences' })), 'fail', { has: ['## Consequences'] });
expect('an unknown status', 'decisions', adr(one({ status: 'Maybe' })), 'fail', { has: ['not one of'] });
expect('superseded by a missing record', 'decisions', adr(one({ status: 'Superseded by 0099' }), row('0001', 'we-use-the-example', 'Superseded by 0099')), 'fail', { has: ['0099'] });
expect('a date that is no day', 'decisions', adr(one({ date: '2026-02-30' })), 'fail', { has: ['YYYY-MM-DD'] });
expect('an option without a reason', 'decisions', adr(one({ roads: why('Because') })), 'fail', { has: ['Why not:'] });
expect('a title with another number', 'decisions', adr(one({ num: '0002' })), 'fail', { has: ['number 0002'] });
expect('an index status that differs', 'decisions', adr({}, row('0001', 'we-use-the-example', 'Proposed')), 'fail', { has: ['says "Proposed"'] });
expect('a record not in the index', 'decisions', adr({}, ''), 'fail', { has: ['no row in the README index'] });
expect('an index row without a file', 'decisions', adr({}, row('0001', 'we-use-the-example', 'Accepted') + row('0003', 'gone', 'Accepted')), 'fail', { has: ['0003 has no record'] });
expect('a file name that is not kebab-case', 'decisions', adr({ 'docs/decisions/0004_Bad.md': record({ num: '0004' }) }), 'fail', { has: ['0004_Bad.md'] });
expect('two records with one number', 'decisions', adr({ 'docs/decisions/0001-again.md': record() }), 'fail', { has: ['also used by'] });

// ---- requirements catalogue: the evidence of a done record (L18). A scratch catalogue of one area,
// built with the real schema and vocabulary, one record, and the files its `tests` may name.
const reqIndex = readFileSync(join(repo, 'docs/requirements/index.yaml'), 'utf8');
const vocabulary = /^ {2}vocabulary:\n(?: {4}.*\n)+/m.exec(reqIndex)[0];
const catalogueFiles = (record, extra = {}) => ({
  'docs/requirements/requirements.schema.json': readFileSync(join(repo, 'docs/requirements/requirements.schema.json'), 'utf8'),
  'docs/requirements/standards.yaml': 'standards:\n  - {id: ST-01, name: "Example standard", group: "Example", register: false}\n',
  'docs/requirements/index.yaml': `schema: 1\ncatalogue:\n  title: "example"\n  checked: "2026-10-31"\n${vocabulary}  areas:\n    EX: {title: "Example area", file: ex.yaml}\n  gaps:\n    example-gap: "A gap that the scratch catalogue declares."\n  glossary:\n    term: "A glossary entry of the scratch catalogue."\n`,
  'docs/requirements/ex.yaml': `schema: 1\narea: EX\nrequirements:\n  - id: REQ-EX-001\n    area: EX\n    statement: "An example MUST do one thing."\n    rationale: "The scratch catalogue needs one record."\n    level: MUST\n    source: design-decision\n    runtimes: all\n    standards: []\n    verification:\n      - {kind: integration-test, ref: "the example test"}\n${record}`,
  'docs/open-decisions.md': '| OD-1 | A decision |\n',
  ...extra,
});
const reqCheck = join(repo, 'site/scripts/check-requirements.mjs');
const reqExpect = (name, files, want, opts = {}) => expect(name, 'check-requirements', files, want, { script: reqCheck, target: 'docs/requirements', ...opts });
const withTests = (...paths) => `    tests:\n${paths.map((t) => `      - ${t}\n`).join('')}`;
const testFile = (text = 'func Test(t *testing.T) { t.Run("REQ-EX-001/case", nil) }\n') => ({ 'tests/example_test.go': text });
reqExpect('a planned record needs no tests', catalogueFiles('    status: planned\n    features: [design]\n'), 'pass', { has: ['1 requirements'] });
reqExpect('a done record with an existing test file that names it', catalogueFiles(`    status: done\n${withTests('tests/example_test.go')}`, testFile()), 'pass');
reqExpect('a done record without tests', catalogueFiles('    status: done\n'), 'fail', { has: ['L18 REQ-EX-001', 'needs tests'] });
reqExpect('a done record whose test file is missing', catalogueFiles(`    status: done\n${withTests('tests/missing_test.go')}`), 'fail', { has: ['L18 REQ-EX-001', 'tests/missing_test.go is not a file'] });
reqExpect('a done record whose test path is a directory', catalogueFiles(`    status: done\n${withTests('tests')}`, testFile()), 'fail', { has: ['tests is not a file'] });
reqExpect('a done record whose test file does not name it', catalogueFiles(`    status: done\n${withTests('tests/example_test.go')}`, testFile('func Test(t *testing.T) {}\n')), 'fail', { has: ['does not name REQ-EX-001'] });
reqExpect('a done record whose test path leaves the repository', catalogueFiles(`    status: done\n${withTests('../outside_test.go')}`), 'fail', { has: ['L1', 'tests'] });
reqExpect('an empty tests list', catalogueFiles('    status: done\n    tests: []\n'), 'fail', { has: ['fewer than 1 items'] });
reqExpect('a planned record that names a missing test file', catalogueFiles(`    status: planned\n    features: [design]\n${withTests('tests/missing_test.go')}`), 'fail', { has: ['tests/missing_test.go is not a file'] });

// ---- C4 public content. The token-shaped strings are assembled here, not written out.
const pub = (text) => ({ 'README.md': `# Example\n\n${text}\n` });
const pem = `${'-'.repeat(5)}BEGIN EC PRIVATE KEY${'-'.repeat(5)}`;
expect('clean text', 'public-content', pub('Use sluis.example.com and account 123456789012.'), 'pass', { has: ['private deny-list not available, skipped'] });
expect('an account-id-shaped number', 'public-content', pub(`arn:aws:iam::${'48392017'}${'4655'}:role/x`), 'fail', { has: ['README.md:3', 'account-id-shaped'], lacks: ['4655'] });
expect('a private key block', 'public-content', pub(pem), 'fail', { has: ['private key block'] });
expect('a GitHub token', 'public-content', pub(`token ${'ghp'}_${'a1B2'.repeat(9)}`), 'fail', { has: ['GitHub token'], lacks: ['a1B2'] });
expect('an AWS access key id', 'public-content', pub(`${'AK'}IA${'Q'.repeat(16)}`), 'fail', { has: ['AWS access key id'] });
expect('an sk- key', 'public-content', pub(`${'sk'}-proj-${'x9'.repeat(12)}`), 'fail', { has: ['sk- secret key'] });
expect('an sk- word with no digit', 'public-content', pub(`${'sk'}-${'abcdefghij'.repeat(3)}`), 'pass');
expect('a dashed account id', 'public-content', pub(`account ${'4839'}-${'2017'}-${'4655'}`), 'fail', { has: ['dashed account-id-shaped'], lacks: ['4655'] });
expect('a dashed placeholder', 'public-content', pub('account 1234-5678-9012'), 'pass');
expect('a UUID whose last group is digits', 'public-content', pub(`id 9f1c2d3e-4b5a-4c6d-8e7f-${'48392017'}${'4655'}`), 'pass');
expect('digits inside a base64 run', 'public-content', pub(`sha512-Qx7Lm2${'48392017'}${'4655'}Zp9KdR3tWv0Ys==`), 'pass');
expect('a Stripe live key', 'public-content', pub(`${'sk'}_live_${'a1'.repeat(12)}`), 'fail', { has: ['Stripe live key'] });
expect('an npm token', 'public-content', pub(`${'npm'}_${'a1B2'.repeat(9)}`), 'fail', { has: ['npm token'] });
expect('an age secret key', 'public-content', pub(`${'AGE-SECRET'}-KEY-1${'QZ'.repeat(29)}`), 'fail', { has: ['age secret key'] });
expect('a Vault token', 'public-content', pub(`${'hvs'}.${'a1B2'.repeat(7)}`), 'fail', { has: ['Vault token'] });
expect('FIDO SSH key types are not tokens', 'public-content', pub('sk-ssh-ed25519@openssh.com and sk-ecdsa-sha2-nistp256-cert-v01@openssh.com'), 'pass');
expect('a deny-listed term', 'public-content', pub('Deployed at ExampleCorp in 2026.'), 'fail',
  { env: { PUBLIC_CONTENT_DENYLIST: '# comment\n\nexamplecorp\nglobexample\n' }, has: ['README.md:3: matches a deny-listed term'], lacks: ['examplecorp', 'globexample', 'Deployed'] });
expect('a deny-listed term in a path', 'public-content', { 'README.md': 'x\n', 'docs/examplecorp/notes.md': `${'4839'}-${'2017'}-${'4655'}\n` }, 'fail',
  { env: { PUBLIC_CONTENT_DENYLIST: 'examplecorp' }, has: ['a tracked path matches a deny-listed term (file #2)', 'file #2:1:1'], lacks: ['examplecorp', 'notes.md'] });
expect('a term inside a longer word is no match', 'public-content', pub('The examplecorpus is fine.'), 'pass',
  { env: { PUBLIC_CONTENT_DENYLIST: 'examplecorp' }, has: ['private deny-list loaded'], lacks: ['examplecorpus'] });
expect('a required deny-list that is empty', 'public-content', pub('Text.'), 'fail', { env: { REQUIRE_DENYLIST: 'true' }, has: ['required here'] });
expect('a required deny-list that is set', 'public-content', pub('Text.'), 'pass', { env: { REQUIRE_DENYLIST: 'true', PUBLIC_CONTENT_DENYLIST: 'examplecorp' }, lacks: ['examplecorp'] });
expect('a deny-list that is not required', 'public-content', pub('Text.'), 'pass', { env: { REQUIRE_DENYLIST: 'false' }, has: ['skipped'] });

// ---- C4 public text: commit messages and the pull request text
const msgs = { 'README.md': 'x\n' };
const term = { PUBLIC_CONTENT_DENYLIST: 'examplecorp' };
expect('clean messages', 'public-messages', msgs, 'pass', { commits: ['docs: a change', 'ci: another'], env: { ...term, PR_TITLE: 'docs: a change' }, has: ['3 commit messages'] });
expect('a term in a commit message', 'public-messages', msgs, 'fail', { commits: ['docs: a change', 'docs: for ExampleCorp\n\nBody.'], env: term, has: ['line 1: matches a deny-listed term'], lacks: ['examplecorp', 'Body.'] });
expect('a term in the pull request body', 'public-messages', msgs, 'fail', { commits: ['docs: a change'], env: { ...term, PR_TITLE: 'docs: a change', PR_BODY: 'Intro.\nSee examplecorp.' }, has: ['pull request body, line 2: matches a deny-listed term'], lacks: ['examplecorp'] });
expect('a token in a commit message', 'public-messages', msgs, 'fail', { commits: [`ci: ${'ghp'}_${'a1B2'.repeat(9)}`], has: ['GitHub token'], lacks: ['a1B2'] });
expect('no commits and no pull request text', 'public-messages', msgs, 'fail', { commits: ['docs: one'], env: { BASE: 'HEAD' }, has: ['empty sweep'] });
expect('nothing to scan', 'public-content', {}, 'fail', { has: ['empty sweep'] });

// ---- C5 YAML extension
expect('.yaml files', 'yaml-ext', { 'a.yaml': 'a: 1\n' }, 'pass');
expect('the files tools require', 'yaml-ext', { '.github/ISSUE_TEMPLATE/config.yml': 'blank_issues_enabled: false\n', 'site/.yarnrc.yml': 'nodeLinker: node-modules\n' }, 'pass');
expect('a .yml file', 'yaml-ext', { 'a.yaml': 'a: 1\n', 'deploy/values.yml': 'a: 1\n' }, 'fail', { has: ['deploy/values.yml'] });
expect('no YAML at all', 'yaml-ext', { 'README.md': 'x\n' }, 'fail', { has: ['empty sweep'] });

// ---- C6 agent links
const docs = { 'docs/contributing/index.md': 'x\n', Justfile: 'default:\n', 'CLAUDE.md': '@AGENTS.md\n' };
expect('no agent files at all', 'agent-links', { 'docs/contributing/index.md': 'x\n' }, 'fail', { has: ['AGENTS.md is missing', 'CLAUDE.md is missing'] });
expect('AGENTS.md is missing', 'agent-links', { ...docs, '.claude/skills/example/SKILL.md': 'See `docs/contributing/`.\n' }, 'fail', { has: ['AGENTS.md is missing'] });
expect('CLAUDE.md is missing', 'agent-links', { 'docs/contributing/index.md': 'x\n', 'AGENTS.md': 'Read `docs/contributing/`.\n' }, 'fail', { has: ['CLAUDE.md is missing'] });
expect('CLAUDE.md does not import AGENTS.md', 'agent-links', { ...docs, 'CLAUDE.md': 'Read AGENTS.md.\n', 'AGENTS.md': 'Read `docs/contributing/`.\n' }, 'fail', { has: ['CLAUDE.md does not import AGENTS.md'] });
expect('every pointer exists', 'agent-links', {
  ...docs,
  'AGENTS.md': 'Read [the guide](docs/contributing/index.md#start), run `just site-check` from the `Justfile`.\nPush `origin/main`, see `docs/requirements/<area>.yaml` and https://example.com/x.\n```\n[not a link](docs/missing.md)\n```\n',
  'CLAUDE.md': '@AGENTS.md\n',
  '.claude/skills/example/SKILL.md': 'See [the guide](../../../docs/contributing/index.md) and `docs/contributing/`.\n',
}, 'pass', { has: ['5 pointers in 3 agent files'] });
expect('a link to a missing file', 'agent-links', { ...docs, 'AGENTS.md': 'Read [this](docs/contributing/gone.md).\n' }, 'fail', { has: ['AGENTS.md:1: docs/contributing/gone.md does not exist'] });
expect('a code path to a missing file', 'agent-links', { ...docs, '.claude/skills/example/SKILL.md': 'Edit `docs/contributing/gone.md`.\n' }, 'fail', { has: ['gone.md does not exist'] });
expect('a subagent file with a missing path', 'agent-links', { ...docs, '.claude/agents/docs.md': 'Edit `docs/contributing/gone.md`.\n' }, 'fail', { has: ['.claude/agents/docs.md:1: docs/contributing/gone.md does not exist'] });
expect('an agent file with no pointer', 'agent-links', { ...docs, 'AGENTS.md': 'Be careful.\n' }, 'fail', { has: ['empty sweep'] });

rmSync(scratch, { recursive: true, force: true });
if (failures) {
  console.error(`selftest: ${failures} of ${cases} cases failed`);
  process.exit(1);
}
console.log(`selftest: ${cases} cases, all as expected`);
