#!/usr/bin/env node
// Agent wiring: every path that the agent instruction files point to exists.
//
// The files: AGENTS.md and CLAUDE.md at the root and anywhere git tracks them,
// .claude/skills/<name>/SKILL.md and .claude/agents/<name>.md. A pointer is
//   - a Markdown link or reference definition with a relative target ([text](docs/x.md#part)),
//     resolved from the file's directory, or from the root when it starts with /;
//   - an inline code span that is a path whose first part is an entry of the repository root
//     (`docs/contributing/`, `Justfile`), resolved from the root or from the file's directory;
//   - an @-import (@AGENTS.md, @docs/x.md) of the same shape.
// URLs, anchors, globs and placeholders (<...>, NNNN, {...}, $...) are not pointers.
//
// AGENTS.md at the root must exist, and CLAUDE.md must import it with an @AGENTS.md line, so
// that Claude Code reads the same instructions as every other agent. A missing file is a
// failure, not a skipped check. An AGENTS.md that points to no page is a failure too: an empty
// sweep proves nothing.
//
// usage: tools/checks/agent-links.mjs [repo-root]
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { dirname, join, posix } from 'node:path';
import { reporter, repoRoot, trackedFiles } from './lib.mjs';

const root = repoRoot();
const r = reporter('check-agent-links');

const sources = new Set();
for (const f of ['AGENTS.md', 'CLAUDE.md']) if (existsSync(join(root, f))) sources.add(f);
for (const f of trackedFiles(root)) if (/(^|\/)(AGENTS|CLAUDE)\.md$/.test(f)) sources.add(f);
const skills = join(root, '.claude', 'skills');
if (existsSync(skills)) {
  for (const d of readdirSync(skills, { withFileTypes: true })) {
    if (d.isDirectory() && existsSync(join(skills, d.name, 'SKILL.md'))) sources.add(`.claude/skills/${d.name}/SKILL.md`);
  }
}
const agents = join(root, '.claude', 'agents');
if (existsSync(agents)) {
  for (const d of readdirSync(agents, { withFileTypes: true })) {
    if (d.isFile() && d.name.endsWith('.md')) sources.add(`.claude/agents/${d.name}`);
  }
}
if (!existsSync(join(root, 'AGENTS.md'))) r.error('AGENTS.md is missing at the repository root');
if (!existsSync(join(root, 'CLAUDE.md'))) r.error('CLAUDE.md is missing at the repository root; it must import AGENTS.md');
else if (!/^@AGENTS\.md\s*$/m.test(readFileSync(join(root, 'CLAUDE.md'), 'utf8'))) {
  r.error('CLAUDE.md does not import AGENTS.md: it needs a line "@AGENTS.md"');
}

const rootEntries = new Set(readdirSync(root).filter((n) => n !== '.git'));
const PLACEHOLDER = /[<>{}$*?]|NNNN/;
const isPathLike = (t) => /^[\w./@-]+$/.test(t) && !PLACEHOLDER.test(t) && rootEntries.has(t.replace(/^\.\//, '').split('/')[0]);

let pointers = 0;
let agentsPointers = 0;
for (const src of [...sources].sort()) {
  const base = dirname(src);
  const before = pointers;
  let fence = false;
  readFileSync(join(root, src), 'utf8').split('\n').forEach((line, i) => {
    if (/^\s*(```|~~~)/.test(line)) { fence = !fence; return; }
    if (fence) return;
    const where = `${src}:${i + 1}`;
    const check = (target, from) => {
      pointers++;
      const rel = target.startsWith('/') ? target.slice(1) : posix.join(from, target);
      const alt = posix.join(base, target);
      if (!existsSync(join(root, rel)) && !(from === '.' && existsSync(join(root, alt)))) {
        r.error(`${where}: ${target} does not exist`);
      }
    };
    const links = [
      ...line.matchAll(/\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)/g),
      ...line.matchAll(/^\s*\[[^\]]+\]:\s*<?(\S+?)>?(?:\s|$)/g),
    ].map((m) => m[1]);
    for (const link of links) {
      if (/^[a-z][a-z0-9+.-]*:/i.test(link) || link.startsWith('#')) continue;
      const target = decodeURIComponent(link.replace(/[#?].*$/, ''));
      if (target && !PLACEHOLDER.test(target)) check(target, base);
    }
    const prose = line.replace(/\[[^\]]*\]\([^)]*\)/g, '');
    for (const m of prose.matchAll(/`([^`\s]+)`/g)) {
      const t = m[1].replace(/[#:].*$/, '');
      if (isPathLike(t)) check(t, '.');
    }
    for (const m of prose.replace(/`[^`]*`/g, '').matchAll(/(?:^|\s)@([\w./-]+[\w/])/g)) {
      if (isPathLike(m[1])) check(m[1], base);
    }
  });
  if (src === 'AGENTS.md') agentsPointers = pointers - before;
}
if (existsSync(join(root, 'AGENTS.md')) && !agentsPointers) {
  r.error('AGENTS.md points to no page: an empty sweep proves nothing');
}

r.finish(pointers, `pointers in ${sources.size} agent files`);
