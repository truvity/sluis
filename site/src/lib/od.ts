// The open-decision ids the explorers use must be rows of the "Open" table of
// docs/open-decisions.md, as `just site-lint` demands of the asides in Markdown
// pages. The explorer pages are MDX, where the `:::caution[...]` syntax is not
// processed, so the check is made here, at build time. An unknown id fails the
// build, and so does a decided one: a decided point is plain prose, not an
// open-decision note.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

let tables: { open: Set<string>; decided: Set<string> } | undefined;
function registry() {
  if (!tables) {
    const open = new Set<string>();
    const decided = new Set<string>();
    let section: Set<string> | null = null;
    const text = readFileSync(resolve(process.cwd(), '../docs/open-decisions.md'), 'utf8');
    for (const line of text.split('\n')) {
      const h = /^##\s+(.*?)\s*$/.exec(line);
      if (h) {
        section = /^open$/i.test(h[1]) ? open : /^decided$/i.test(h[1]) ? decided : null;
        continue;
      }
      const m = /^\|\s*(?:<a id="od-\d+"><\/a>)?\s*(OD-\d+)\s*\|/.exec(line);
      if (m && section) section.add(m[1]);
    }
    if (open.size === 0 || decided.size === 0) throw new Error('explorer: docs/open-decisions.md has an empty Open or Decided table');
    tables = { open, decided };
  }
  return tables;
}
export function registeredOpenDecisions(): Set<string> {
  return registry().open;
}
export function assertOpenDecision(id: string): string {
  const { open, decided } = registry();
  if (decided.has(id)) throw new Error(`explorer: ${id} is decided; state it as plain prose, not as an open decision`);
  if (!open.has(id)) throw new Error(`explorer: ${id} is not a row of docs/open-decisions.md`);
  return id;
}
