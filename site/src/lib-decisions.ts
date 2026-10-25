import { getCollection, type CollectionEntry } from 'astro:content';

type Decision = CollectionEntry<'decisions'>;

// The records, without the placeholder the loader keeps in an empty collection.
export async function listDecisions(): Promise<Decision[]> {
  const entries = await getCollection('decisions');
  return entries.filter((entry) => !entry.id.startsWith('_')).sort((a, b) => a.id.localeCompare(b.id));
}

// The title of an ADR: its front matter, else its first level-one heading,
// else its file name.
export function decisionTitle(entry: Decision): string {
  if (entry.data.title) return entry.data.title;
  const heading = /^#\s+(.+)$/m.exec(entry.body ?? '');
  return heading ? heading[1].trim() : entry.id;
}

// Starlight's page description must be a sentence; fall back to a fixed one.
export function decisionDescription(entry: Decision): string {
  return entry.data.description ?? `Architecture decision record ${entry.id} for sluis.`;
}

export const sourceUrl = (entry: Decision): string =>
  `https://github.com/truvity/sluis/blob/main/docs/decisions/${entry.id}.md`;
