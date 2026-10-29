// Adds a status badge to a sidebar link, only for pages that are partly built
// or shipped: a sidebar full of "Target" badges is noise while every v2 page is
// a target. The page-title badge shows every status. Groups and links that are
// not docs pages (Decisions, v1 documentation) are left alone.
//
// It also drops the table of contents from a page with fewer than two headings:
// the "On this page" bar (src/components/PageSidebar.astro) would only list the
// page title. A page with `tableOfContents: false` has no table of contents to
// begin with.
import { defineRouteMiddleware } from '@astrojs/starlight/route-data';
import { getCollection } from 'astro:content';
import { STATUSES } from './status';

type SidebarEntry = {
  type: 'link' | 'group';
  href?: string;
  badge?: { text: string; variant: string };
  entries?: SidebarEntry[];
};

type TocItem = { children?: TocItem[] };

// Headings in the table of contents, not counting the first entry Starlight adds
// for the page title ("Overview").
const countHeadings = (items: TocItem[]): number =>
  items.reduce((n, item) => n + 1 + countHeadings(item.children ?? []), 0);

export const onRequest = defineRouteMiddleware(async (context) => {
  const route = context.locals.starlightRoute;
  if (route.toc && countHeadings(route.toc.items.slice(1)) < 2) {
    route.toc = undefined;
  }

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
