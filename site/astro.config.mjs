import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';
import starlightLinksValidator from 'starlight-links-validator';
import starlightLlmsTxt from 'starlight-llms-txt';

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
      sidebar: [
        { label: 'Overview', slug: 'overview' },
        { label: 'Architecture', items: [{ autogenerate: { directory: 'architecture' } }] },
        { label: 'Decisions', link: '/decisions/' },
        { label: 'Roadmap', slug: 'roadmap' },
        { label: 'Contributing', slug: 'contributing' },
        { label: 'v1 documentation', link: '/v1/' },
      ],
      plugins: [
        // Fails the build on a broken internal link or anchor.
        starlightLinksValidator({
          errorOnRelativeLinks: false,
          // The ADR pages and the v1 documentation are not Starlight content.
          exclude: ['/v1/', '/v1/**', '/decisions/', '/decisions/**'],
        }),
        starlightLlmsTxt({
          projectName: 'sluis',
          description:
            'sluis is a pluggable identity and access broker for people, robots and external systems. v2 is being designed in the open; v1 documentation is at /v1/.',
        }),
      ],
    }),
  ],
});
