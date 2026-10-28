import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';
import { satteri } from '@astrojs/markdown-satteri';
import { statusPlugin } from './src/remark-status.mjs';

// https://sluis.id. The custom domain and its DNS are managed outside this
// repository; the site is served from the root, with the v1 documentation
// copied in under /v1/ (site/scripts/build-v1.sh).
export default defineConfig({
  site: 'https://sluis.id',
  trailingSlash: 'always',
  vite: {
    // Mermaid's diagram engines are one large lazily loaded chunk; the size
    // warning is expected for it and the build allows no other warning.
    build: { chunkSizeWarningLimit: 4000 },
  },
  // Astro's Markdown processor is Sätteri; statusPlugin puts each page's status
  // in its first paragraph (src/remark-status.mjs). Starlight adds its own
  // plugins (asides, heading links) to the same processor.
  markdown: { processor: satteri({ mdastPlugins: [statusPlugin] }) },
  integrations: [
    // Must come before Starlight so the ```mermaid fences are claimed first.
    mermaid({ theme: 'neutral', autoTheme: true, enableLog: false }),
    starlight({
      title: 'sluis',
      description:
        'sluis is a pluggable identity and access broker for people, robots and external systems. Open source, designed in the open.',
      favicon: '/favicon.svg',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/truvity/sluis' },
      ],
      editLink: { baseUrl: 'https://github.com/truvity/sluis/edit/main/docs/' },
      lastUpdated: false,
      // The 404 page is src/pages/404.astro.
      disable404Route: true,
      head: [
        { tag: 'meta', attrs: { property: 'og:site_name', content: 'sluis' } },
        { tag: 'meta', attrs: { property: 'og:type', content: 'website' } },
        {
          tag: 'script',
          attrs: { type: 'application/ld+json' },
          content: JSON.stringify({
            '@context': 'https://schema.org',
            '@type': 'SoftwareSourceCode',
            name: 'sluis',
            description: 'A pluggable identity and access broker for people, robots and external systems.',
            url: 'https://sluis.id/',
            codeRepository: 'https://github.com/truvity/sluis',
            license: 'https://opensource.org/license/mit',
          }),
        },
      ],
      // The status badge beside each page title, its colours and the rule that
      // hides the status paragraph on the page (it stays in llms.txt).
      components: { PageTitle: './src/components/PageTitle.astro' },
      customCss: ['./src/styles/status.css'],
      routeMiddleware: './src/route-middleware.ts',
      sidebar: [
        { label: 'Overview', slug: 'overview' },
        { label: 'Concepts', items: [{ autogenerate: { directory: 'concepts' } }] },
        { label: 'Architecture', items: [{ autogenerate: { directory: 'architecture' } }] },
        { label: 'Shapes', items: [{ autogenerate: { directory: 'shapes' } }] },
        { label: 'Tutorials', items: [{ autogenerate: { directory: 'tutorials' } }] },
        { label: 'Distribution', items: [{ autogenerate: { directory: 'distribution' } }] },
        { label: 'Open decisions', slug: 'open-decisions' },
        { label: 'Decisions', link: '/decisions/' },
        { label: 'Roadmap', slug: 'roadmap' },
        { label: 'Features: v1 and v2', slug: 'features' },
        { label: 'Contributing', slug: 'contributing' },
        { label: 'v1 documentation', link: '/v1/' },
      ],
      plugins: [
        // Fails the build on a broken internal link or anchor.
        starlightLinksValidator({
          errorOnRelativeLinks: false,
          // The ADR pages and the v1 documentation are not Starlight content.
          // The two explorer pages are MDX (they import components), and the
          // validator collects no headings from an MDX page, so a link to one
          // would always fail; the pages themselves are still built and checked.
          exclude: ['/v1/', '/v1/**', '/decisions/', '/decisions/**', '/architecture/explorer/', '/architecture/audit-explorer/'],
        }),
        starlightLlmsTxt({
          projectName: 'sluis',
          description:
            'sluis is a pluggable identity and access broker for people, robots and external systems. v2 is being designed in the open; v1 documentation is at /v1/. Each page starts with its status: target state, partly built or shipped.',
          // Keep the Proposed asides (notes) in llms-small.txt as well.
          minify: { note: false },
        }),
      ],
    }),
  ],
});
