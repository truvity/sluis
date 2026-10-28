// The open-decision ids the explorers use must be rows of docs/open-decisions.md,
// as `just site-lint` demands of the asides in Markdown pages. The explorer
// pages are MDX, where the `:::caution[...]` syntax is not processed, so the
// check is made here, at build time, and an unknown id fails the build.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

let ids: Set<string> | undefined;
export function registeredOpenDecisions(): Set<string> {
  if (!ids) {
    const text = readFileSync(resolve(process.cwd(), '../docs/open-decisions.md'), 'utf8');
    ids = new Set([...text.matchAll(/^\|\s*(OD-\d+)\s*\|/gm)].map((m) => m[1]));
  }
  return ids;
}
export function assertOpenDecision(id: string): string {
  if (!registeredOpenDecisions().has(id)) throw new Error(`explorer: ${id} is not a row of docs/open-decisions.md`);
  return id;
}
