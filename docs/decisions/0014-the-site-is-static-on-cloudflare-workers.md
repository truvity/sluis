# 0014 — The site is static on Cloudflare Workers, deployed by GitHub Actions, with a live status API, and it serves the whole family

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #1010](https://github.com/truvity/sluis/issues/1010).

## Context

The site at sluis.id is built from `docs/` and `site/` with Astro Starlight, with the v1 documentation copied in under `/v1/`. It is also the site of a family of products: sluis, [atoom](https://github.com/truvity/atoom), [audit](https://github.com/truvity/audit) and [session](https://github.com/truvity/session) ([0044](/decisions/0044-sluis-is-a-core-and-add-ons/)).

GitHub Pages was not enough for an identity and security project. It sets no custom headers, so the site sent no `content-security-policy`, `x-frame-options`, `permissions-policy` or `referrer-policy`. It has no server-side redirects, only meta-refresh pages. It gives a status page no place to hold a token for live data, and unauthenticated browser calls to the GitHub API are limited per visitor address. Cloudflare tells new projects to start on Workers, and the Astro Cloudflare adapter targets Workers only.

The project board has a single-select field, `Product milestone`, whose options are the milestones in order. The status page reads it.

## Decision

### The site

- **Astro Starlight stays, and every docs page stays prerendered,** so Pagefind search keeps working. The static output is served by Cloudflare Workers with static assets, through the official `@astrojs/cloudflare` adapter.
- **Only the `/api/*` routes run as Worker code** (`run_worker_first` lists `/api/*` and nothing else). Every other request is a static asset and does not invoke the Worker. The adapter's route cache (`cacheCloudflare()`) is not used, because it turns on Workers Cache, which bills static asset requests.
- **Security headers come from a `_headers` file** (Content-Security-Policy with `frame-ancestors`, and Permissions-Policy). **Redirects come from a `_redirects` file** that the build writes from the site's redirect list. `www.sluis.id` answers 301 to `sluis.id` by a redirect rule before the Worker runs. A page address without its trailing slash gets the 404 page.
- **The names are Worker custom domains.** There is no `workers.dev` hostname and no preview URL: the Worker sets `workers_dev` and `preview_urls` to false, because either would publish the operator's account subdomain on a public repository. Pull requests are judged by GitHub CI, which runs the same build and a dry-run deploy with no Cloudflare credential, and by `wrangler dev` on a checkout.
- **The Cloudflare resources are declared in the operator's own infrastructure code,** outside this repository: the Worker, the KV namespace and the custom domains. The repository names bindings, never an account id or a namespace id.

### Deploy

- **GitHub Actions builds and deploys the site.** The deploy job runs on a push to `main`, or a manual run from `main`, after the site checks have passed, and runs `wrangler deploy`. Deploys never overlap.
- **The deploy runs in the GitHub environment `cloudflare`, which allows `main` only** and holds the deploy's secrets: the Cloudflare API token, the account identifier and the KV namespace id. A pull request, a fork's included, never gets them.
- **The token can deploy this one Worker only, and it expires.** The maintainer creates and rotates it.

### The live status API

- **The status page has a live block.** It is the Worker's HTML at `/api/status/live/`, which a bundled page script fetches and swaps in for the build-time snapshot. It is not an Astro server island, because the island's inline loader changes with every build and so cannot be listed in a hash-based Content-Security-Policy.
- **A read-only GitHub App owned by the organisation reads issues, projects, actions and checks.** Its private key is a Worker secret, and the build mints a one-hour token from the same App to read the board.
- **Webhooks refresh a KV document within seconds,** and a cron every 15 minutes with conditional requests catches missed deliveries. The Worker checks each webhook's signature.
- **CI posts coverage and conformance summaries,** authenticated by the GitHub Actions OIDC token, pinned to the issuer, audience, repository, branch `main` and workflow. No secret is stored for it.
- **The page groups work by the `Product milestone` field,** in the field's option order, so reordering the board changes the page with no code change. Each planned decision record has a `type:decision` issue, and requirements appear under their issues' milestones. Decision and requirement status are build-time facts from the repository, published as `/data/repo.json`. Issue, CI, coverage and conformance state are live. Each block shows the time its data is as of and falls back to the build-time snapshot.

The site, the deploy and the live status API for sluis's own board are built.

### The family

- **sluis.id is the site of the family.** It has one section each for sluis, atoom, audit and session, and the status API reads the boards of all four products. The sections and the status over four boards are planned.

## Consequences

- The site ships real security headers and server-side redirects at no cost for static traffic. A change is seen live only after it merges.
- Static traffic stays free only while no Worker runs first. A catch-all route or Workers Cache would put the whole site under the Free plan's daily Worker request limit, and over it Worker paths return 429 rather than fall back to the assets. Every routing change must keep that in mind.
- The deploy needs a long-lived Cloudflare API token, because Cloudflare accepts no OIDC federation for deploys. Its scope (one Worker, no create or delete), its expiry and the `main`-only environment limit a leak, but anyone who can merge to `main` or edit a workflow that runs there can use it. A deploy that fails authentication is the reminder to rotate it.
- The deploy job rebuilds the site for the Worker, because the KV namespace id is written into the generated Worker configuration at build time from a secret only that job can read. One CI shows build and deploy together, and a broken deploy is a red check on the commit.
- The Worker holds two secrets, the App's private key and the webhook secret. The App is read-only, so a leaked key gives read access and nothing more.
- The live block needs no `ASTRO_KEY` and no inline script, so the Content-Security-Policy stays hash-based. Readers without JavaScript and crawlers see the build-time snapshot, and so does everyone when the fetch fails; a note above the block says which one is shown.
- The status page changes when the maintainer edits a board, with no pull request. Decision and requirement status change only by a merged pull request. Work without an issue on a board is invisible there.
- A family site means one deploy, one Worker and one set of secrets for four products. A product's board that the App cannot read is missing from the page.
- The static output, `_headers` and `_redirects` stay close to portable. The API (KV, cron, the adapter's bindings) is Cloudflare-specific.
- No product requirement follows: this is the project's own site, not something sluis does.

## Roads not taken

- **GitHub Pages only, rebuilt on events.** No security headers beyond a meta CSP, no server-side redirects, and freshness of minutes at best.
- **GitHub Pages plus a Worker on `/api/*`.** Two deploy systems, and headers patched on with zone rules instead of a file in the repository.
- **Cloudflare Pages.** The platform both Cloudflare and the Astro adapter are moving away from.
- **An Astro server island for the live block.** Its inline loader holds a random id and an encrypted token that change with every build, which a hash-listed Content-Security-Policy cannot allow, and it needs a fixed `ASTRO_KEY`.
- **Full on-demand rendering on Workers.** Every request would be a billed Worker request, and `prerender: false` disables search; the docs change only on merge.
- **Another docs engine.** Each would mean rewriting the components, the explorers and the requirements pages, with no gain over Astro's content layer.
- **Cloudflare's own build service.** A second CI whose logs live in the dashboard, which deploys without waiting for the repository's checks and whose check links carry the account identifier.
- **A preview per pull request.** A preview hostname is a `workers.dev` name that carries the account subdomain; this can be revisited if previews on a hostname of the project's own zone become possible.
- **Resources created by wrangler on first deploy.** They would not be in reviewed code, and their ids would end up in the repository.
- **A site for each product.** Four deploys, four sets of secrets and four status pages, for products that are read together.

## Follow-ups

- The live status API on the Worker (webhooks, KV, cron, the CI summaries), built: [#1037](https://github.com/truvity/sluis/issues/1037). It is described in [Tools](/contributing/tools/). The maintainer sets the webhook subscription and its secret.
- Open: how the sections of atoom, audit and session reach the site build (each product keeping its pages in its own repository and the build gathering them, or the pages living here), and how the App gets read access to their boards.
