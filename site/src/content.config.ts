import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';
import type { Loader } from 'astro/loaders';
import { docsSchema, i18nSchema } from '@astrojs/starlight/schema';
import { i18nLoader } from '@astrojs/starlight/loaders';
import { z } from 'astro/zod';

// The documentation lives in ../docs (the repository's docs/ directory), so
// it reads the same on GitHub as on sluis.id. The ADRs in docs/decisions are
// their own collection: their front matter is not the site's, and the site
// indexes them instead of treating them as ordinary pages.
const docsRoot = fileURLToPath(new URL('../../docs', import.meta.url));
const decisionsRoot = `${docsRoot}/decisions`;

// Astro warns about an empty collection, and the build must have no warnings.
// While docs/decisions is missing or holds no records (it arrives with the
// governance change), the collection holds one placeholder entry whose id
// starts with an underscore; the decisions pages skip every such id.
const decisionsLoader = (): Loader => ({
  name: 'sluis-decisions-loader',
  load: async (context) => {
    if (existsSync(decisionsRoot)) {
      await glob({
        base: decisionsRoot,
        pattern: ['*.md', '!README.md', '!_*'],
      }).load(context);
    }
    if (context.store.keys().length === 0) {
      context.store.set({ id: '_empty', data: {} });
    }
  },
});

export const collections = {
  docs: defineCollection({
    loader: glob({
      base: 'src/content/docs',
      pattern: ['**/[^_]*.{md,mdx}', '!decisions/**'],
    }),
    schema: docsSchema(),
  }),
  i18n: defineCollection({ loader: i18nLoader(), schema: i18nSchema() }),
  decisions: defineCollection({
    loader: decisionsLoader(),
    schema: z.object({
      title: z.string().optional(),
      status: z.string().optional(),
      date: z.coerce.date().optional(),
      description: z.string().optional(),
    }).passthrough(),
  }),
};
