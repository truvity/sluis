// Adds a status badge to a sidebar link, only for pages that are partly built
// or shipped: a sidebar full of "Target" badges is noise while every v2 page is
// a target. The page-title badge shows every status. Groups and links that are
// not docs pages (Decisions, v1 documentation) are left alone.
import { defineRouteMiddleware } from '@astrojs/starlight/route-data';
import { getCollection } from 'astro:content';
import { STATUSES } from './status';

type SidebarEntry = {
  type: 'link' | 'group';
  href?: string;
  badge?: { text: string; variant: string };
  entries?: SidebarEntry[];
};

export const onRequest = defineRouteMiddleware(async (context) => {
  const pages = await getCollection('docs');
  const statusByHref = new Map(pages.map((page) => [`/${page.id.replace(/\/?index$/, '')}/`, page.data.status]));

  const walk = (entries: SidebarEntry[]) => {
    for (const entry of entries) {
      if (entry.type === 'group') {
        walk(entry.entries ?? []);
        continue;
      }
      const status = statusByHref.get(entry.href ?? '');
      if (status && status !== 'target' && !entry.badge) {
        entry.badge = { text: STATUSES[status].sidebar, variant: STATUSES[status].variant };
      }
    }
  };
  walk(context.locals.starlightRoute.sidebar as SidebarEntry[]);
});
