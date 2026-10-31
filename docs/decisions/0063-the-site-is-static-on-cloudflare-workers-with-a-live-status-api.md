# 0063 — The site is static on Cloudflare Workers, with a live status API

**Status:** Accepted
**Date:** 2026-10-31

Decided by the maintainer on 2026-10-31. Proposal: [issue #1010](https://github.com/truvity/sluis/issues/1010).
Rewritten in place on 2026-10-31 (no preview per pull request: preview hostnames would publish the operator's account subdomain; the deploy is GitHub Actions, not Cloudflare's build service).

## Context

The site at sluis.id is built from `docs/` and `site/` with Astro Starlight, and the v1 documentation is copied in under `/v1/`. It is served by GitHub Pages behind the Cloudflare proxy. Measured with `curl` on 2026-10-31:

- `cache-control: max-age=600`, the fixed TTL of GitHub Pages, and `cf-cache-status: DYNAMIC`, so every page view goes to GitHub.
- No `content-security-policy`, `x-frame-options`, `permissions-policy` or `referrer-policy`. For an identity and security project that is visible to anyone who checks the headers.

GitHub Pages cannot fix this, and it cannot do three other things the project now needs (all read 2026-10-31):

- **Headers.** Pages sets no custom headers. A `<meta>` CSP cannot carry `frame-ancestors` or `report-uri`, and Permissions-Policy has no meta form.
- **Redirects.** Pages has no server-side redirects, only meta-refresh pages. The site already redirects renamed pages that way.
- **Previews.** Pull request previews are not available: the `preview` input of `actions/deploy-pages` (v5.0.1, 2026-09-01) is "only in alpha currently and is not available to the public". Workers could give them, but at a cost to privacy; see "A preview per pull request" in the roads not taken.
- **Live data.** A project status page should show the state of issues, CI, coverage and conformance as it is now. On Pages that means a rebuild per event, or browser calls to `api.github.com`, which allows 60 unauthenticated requests per hour per visitor IP and has no place for a token.

Cloudflare's own documentation now says Workers "is Cloudflare's primary platform … Start new projects with Workers" (Pages docs, updated 2026-08-25). Astro's `@astrojs/cloudflare` adapter dropped Pages in v13 and targets Workers only. The facts below were read on 2026-10-31, against `astro` 7.4.4, `@astrojs/starlight` 0.42.8, `@astrojs/cloudflare` 14.4.3 (peer dependencies `astro ^7.4.0`, `wrangler ^4.125.0`) and `wrangler` 4.156.0:

| Fact | Value | Source (its own date) |
|---|---|---|
| Static asset requests on Workers | Free and unlimited, as long as no Worker runs first | Cloudflare static assets billing (2026-04-23) |
| Worker requests, Free plan | 100,000 a day, 10 ms CPU each; over the limit, Worker paths return 429 | Cloudflare limits (2026-10-29) |
| KV, Free plan | 100,000 reads and 1,000 writes a day | Cloudflare pricing (2026-10-02) |
| Cron Triggers, Free plan | 5 per account | Cloudflare limits (2026-10-29) |
| Asset files per version, Free plan | 20,000 | Cloudflare limits (2026-10-29) |
| Workers Cache | "every request to your Worker is charged … including requests that are normally free: static asset requests" | Cloudflare Workers Cache (2026-10-30) |
| Deploying from GitHub Actions | A long-lived API token; the docs name no OIDC federation | Cloudflare GitHub Actions (2026-10-18) |
| Starlight `prerender: false` | Disables Pagefind search | Starlight configuration reference |

The engine was weighed too. Since Astro 6 (2026-03-10) its content layer loads YAML, JSON or remote data into schema-checked collections at build time, and Astro has been owned by Cloudflare since January 2026. Fumadocs has the best built-in output for agents (`llms.txt`, a `.md` twin per page), but moving to it means rewriting every component in React. One measurement on 2026-10-31 matters for where data is rendered: sluis.id's `llms-full.txt` (1.25 MB) holds none of the 31 decision records and none of the 858 requirement records, because both are rendered by code in `site/src/pages`, outside the docs collection. A fact that only code renders is easy to lose from the text that agents read.

The project board's `Product milestone` field is a single-select with four options on 2026-10-31, in this order: First working version, v1 parity, CIAM upstream, Quota. Requirements already link the issues that deliver them through `issues:` ([Requirements](/contributing/requirements/)).

## Decision

### Engine and rendering

- **We stay on Astro Starlight.**
- **Pages stay prerendered,** so Pagefind search keeps working. We do not set Starlight's `prerender: false`.
- **The static output is hosted on Cloudflare Workers with static assets,** through the official `@astrojs/cloudflare` adapter.
- **Only two things run as Worker code:** the `/api/*` routes and one server island on the status page. Every other request is a static asset and does not invoke the Worker.
- **We do not use the adapter's experimental route cache, `cacheCloudflare()`,** because it turns on Workers Cache, which bills static asset requests.

### Deploy

- **GitHub Actions builds and deploys the site.** The `deploy-workers` job of `.github/workflows/site.yaml` runs on a push to `main`, and on a manual run from `main`, after the site checks have passed. It builds the site for the Worker and runs `wrangler deploy`.
- **The job runs in a GitHub environment, `cloudflare`, that allows `main` only.** The environment holds the three secrets of the deploy: the Cloudflare API token, the account identifier and the id of the KV namespace. A pull request never gets them, a fork's included, because the workflow has no pull request trigger and the environment refuses every other branch.
- **The token is limited to deploying this one Worker, and has an expiry.** The maintainer rotates it.
- **Deploys never overlap.** A concurrency group lets a running deploy finish, and a newer deploy replaces one that is still waiting.
- **Only the production branch deploys.** The Worker sets `workers_dev` and `preview_urls` to false. Pull requests are judged by the repository's GitHub CI, which runs the same build and a dry-run deploy with no Cloudflare credential, and by `wrangler dev` on a checkout. See "A preview per pull request" in the roads not taken.
- **The Cloudflare resources are declared in the operator's own infrastructure code, outside this repository:** the Worker, a KV namespace and the custom domain. The repository holds no account id and no KV namespace id.

### Headers and redirects

- **Security headers come from a `_headers` file:** Content-Security-Policy, `frame-ancestors` among its directives, and Permissions-Policy.
- **Redirects come from a `_redirects` file.**

### Live data

- **A read-only GitHub App owned by the organisation reads the live data.** Its permissions are issues, metadata, organisation projects, actions and checks, all read-only. Its private key is a Worker secret.
- **The build reads the project board with the same App,** so the build-time snapshot has the board's data. Each build mints a one-hour token for the repository with `issues: read` and `organization_projects: read` only, from the App's client id and private key (a repository variable and a repository secret).
- **GitHub webhooks refresh a KV document within seconds.** The Worker checks each delivery's signature before it acts on it.
- **A cron every 15 minutes is the safety net** for missed deliveries. It uses conditional requests, so an unchanged answer costs little.
- **CI posts coverage and conformance summaries to the API, authenticated by the GitHub Actions OIDC token.** No secret is stored for it. A pull request from a fork gets no OIDC token, so it cannot post.

### The status page

- **The page groups work by the `Product milestone` field of the GitHub project.** The field's option order is the page order. Adding or reordering an option changes the page with no code change.
- **Each planned decision record has a `type:decision` issue,** which places it on the page.
- **Requirements link their issues through `issues:`,** and appear under those issues' milestones.
- **Decision and requirement status are build-time facts from the repository.** The build publishes them as `/data/repo.json`.
- **Issue, CI, coverage and conformance state are live,** from the API.
- **Each block shows the time its data is as of,** and falls back to the build-time snapshot when the live data cannot be read.

### Cutover

1. Deploy the Worker with no route on the domain. It has no public hostname: `workers_dev` and `preview_urls` are off.
2. Verify the pages, search, the headers, that static requests do not invoke the Worker, and the API, on the same build with `wrangler dev`, and on a temporary hostname of the project's own zone that the operator attaches for the check and removes after.
3. Switch the domain to the Worker.
4. Keep GitHub Pages for one week as a DNS fallback.
5. Turn GitHub Pages off.

## Consequences

- The site ships real security headers and server-side redirects, still at no cost for static traffic. It has no preview per pull request: a change to the site is judged in GitHub CI and locally, and is seen live only after it merges.
- The Worker has no `workers.dev` hostname and no preview hostname, so nothing public names the operator's account. Turning either on is a new decision.
- Static traffic stays free only while no Worker runs first. Anything that sends every request through the Worker, such as a catch-all route or Workers Cache, puts the whole site under the Free plan's 100,000 requests a day, and over it Worker paths return 429 rather than fall back to the assets. The cutover checks this, and so must every later change to the routing.
- The Free plan's 10 ms of CPU per request is enough for a JSON answer from KV. Verifying an OIDC token costs more, but runs only on the rare CI posts. Measure both before relying on the Free plan.
- The Worker holds two secrets: the GitHub App's private key and the webhook secret. The App is read-only and its key is rotated, so a leaked key gives read access and nothing more.
- The OIDC check in the Worker pins the issuer `https://token.actions.githubusercontent.com`, the audience, the repository, its owner, the branch `main` and the workflow, so only trusted runs on `main` can post summaries.
- The server island needs a fixed `ASTRO_KEY`, so that cached HTML can still decrypt the island's props after a redeploy. Its fallback is the build-time snapshot, which is what readers without JavaScript and crawlers see.
- One CI shows the whole path. The deploy is a job of the workflow that builds the site, so its logs sit next to the build's, it runs only after the site checks have passed, and a broken deploy is a red check on the commit. Cloudflare posts no checks or comments on the repository.
- The deploy needs a long-lived Cloudflare API token, because Cloudflare accepts no OIDC federation for deploys. The token is limited to deploying this one Worker (it cannot create or delete Workers) and expires, but a leak still lets its holder replace the Worker that serves the site. It lives only in the `cloudflare` environment, which allows `main` only; anyone who can merge to `main`, or edit a workflow that runs there, can use it. The maintainer rotates it before it expires, and a deploy that fails with an authentication error is the reminder.
- The deploy job rebuilds the site for the Worker instead of passing on the build for GitHub Pages, because the KV namespace id is written into the generated Worker configuration at build time, from a secret that only this job can read. The build must produce the same output as the Pages build: the v1 documentation under `/v1/`, the Git LFS images and fonts, and the build checks, within the 20,000-file limit. `.github/workflows/site.yaml` keeps its GitHub Pages job as a manual fallback for one week after the cutover, and retires it then.
- The repository's Cloudflare configuration names bindings, not resources, so a fork can build the site but cannot deploy it to the operator's account.
- The status page changes when the maintainer edits the project board, with no pull request. Decision and requirement status still change only by a merged pull request.
- Work that is not yet a decision record or a requirement is invisible on the status page until it has an issue with a `Product milestone`.
- Some lock-in to Cloudflare: the static output, `_headers` and `_redirects` stay close to portable, but the API (KV, Cron, the adapter's bindings, about 200 lines) is Cloudflare-specific (estimated).
- The pages that describe how the site is built and deployed follow in this pull request: [Tools, checks and verification](/contributing/tools/#the-site), [Distribution](/distribution/), [Release signing](/distribution/release-signing/) and [Requirements](/contributing/requirements/).
- No product requirement follows: the decision is about the project's own site, not about what sluis does.

## Roads not taken

### GitHub Pages only, rebuilt on events

- Pros: no change; no cost; no new account or secret. Workflows can rebuild on `issues`, `milestone`, `workflow_run` and a `schedule`, and status badges work as plain images.
- Cons: no security headers beyond a meta CSP, no server-side redirects, no previews. Freshness is a rebuild of minutes, and a `schedule` can be delayed under load and is disabled after 60 days without activity in a public repository. Events caused by `GITHUB_TOKEN` do not start new workflow runs. Live data in the browser is limited to 60 requests per hour per visitor IP, with no place for a token.
- Why not: it cannot ship the headers a security project should have, and it cannot give the status page live data.

### GitHub Pages plus a Worker on `/api/*`

- Pros: the least migration; only the API is Cloudflare-specific; the static site stays where it is.
- Cons: two deploy systems; no previews for the static site; headers patched on with Cloudflare Transform Rules (10 on the Free plan) instead of a file in the repository.
- Why not: Workers with static assets does everything this does, with one deploy, at the same cost.

### Cloudflare Pages

- Pros: Cloudflare's older static platform, with `_headers`, `_redirects` and file-based Functions.
- Cons: Cloudflare tells new projects to start on Workers; the Astro adapter dropped Pages in v13; the archived `cloudflare/pages-action` has an RCE that may expose workflow secrets (CVE-2026-11325).
- Why not: it is the platform both Cloudflare and Astro are moving away from.

### Full on-demand rendering on Workers

- Pros: every page is fresh on every request; live content collections are stable since Astro 6.
- Cons: every request is a billed Worker request, under the Free plan's 100,000 a day; a full Starlight layout may not fit in 10 ms of CPU; `prerender: false` disables Pagefind search; live collections have no MDX and no image optimisation.
- Why not: the docs change only when a pull request merges, so rendering them per request buys nothing and costs search.

### The adapter's route cache, `cacheCloudflare()`

- Pros: per-route `maxAge`, stale-while-revalidate and tag invalidation from Astro, with no cache code of our own.
- Cons: it is experimental, and it turns on Workers Cache, which bills every request to the Worker, static assets included.
- Why not: it would make the free static traffic billable; one live route needs only plain `Cache-Control` and KV.

### Netlify, Vercel or Read the Docs

- Pros: mature static hosts with previews and headers; Netlify has unlimited deploy previews, and Read the Docs builds any tool that writes HTML.
- Cons: Netlify Free is credit-metered (20 credits per GB of bandwidth, of 300); Vercel Hobby is for "non-commercial, personal use only", and sluis is company-backed, so it would need Pro at $20 a seat; Read the Docs has no edge compute for live data. The sluis.id zone is already on Cloudflare.
- Why not: none of them does more than Workers here, and each adds a vendor.

### Fumadocs

- Pros: the best built-in output for agents (`llms.txt`, `llms-full.txt`, a `.md` twin per page, content negotiation); active (16.17.9 on 2026-10-28); runs on Workers through OpenNext.
- Cons: React and Next.js; every component, the explorers and the requirements pages would be rewritten.
- Why not: Starlight can add the missing agent outputs with a few endpoints; a rewrite costs weeks for that.

### Docusaurus

- Pros: the most widely used docs engine (66.6k stars); strong translation workflow; official Mermaid theme.
- Cons: 3.10 is the last 3.x and v4 is in canary (2026-10-23); its plugin API needs more code than Astro's loaders; a React rewrite.
- Why not: a rewrite onto a major version that is not out yet.

### VitePress

- Pros: fast; build-time data loaders; local search.
- Cons: no stable release since 1.6.4 (2025-08-05), and 2.0 is still in alpha (2026-10-25); a Vue rewrite.
- Why not: a rewrite onto a release line that has stalled.

### Nextra

- Pros: Next.js and MDX, with Pagefind search.
- Cons: last release 4.6.1 (2025-12-04), last commit 2026-06-23; no built-in agent output; a React rewrite.
- Why not: the project is slowing down.

### MkDocs Material or Zensical

- Pros: v1 of sluis uses MkDocs, so the v1 docs already build with it; Material is polished.
- Cons: Material is in maintenance mode with fixes until 2027-05-05; MkDocs core has not released since 2024-08-30, and MkDocs 2.0 drops plugins and is incompatible with Material. Zensical, its successor, is 0.0.74 (2026-10-30), before 1.0, with no data or macros support yet.
- Why not: one is a dead end and the other is not ready.

### Hugo with Docsy

- Pros: very fast; content adapters make pages from data; a large, healthy project (Hugo 0.169.0 on 2026-10-27).
- Cons: Go templates instead of components; the explorers and filters would be rewritten.
- Why not: a rewrite with no gain over Astro's content layer.

### Mintlify

- Pros: hosted; automatic `llms.txt`, `llms-full.txt` and `.md` per page; built-in search.
- Cons: commercial (Pro reported at $450 a month in August 2026); no arbitrary build-time data loaders; the explorers and requirements pages would not port; an open-source project's docs on a vendor.
- Why not: cost and lock-in, and it cannot render the requirements catalogue.

### Rendering content from data only in a Worker or a remote API

- Pros: always fresh; no rebuild for a change in the data.
- Cons: the content is not in the repository, so an agent or a person reading a checkout or GitHub sees nothing; it is not in `llms.txt` either. The measured gap above shows how easily code-rendered facts drop out of the agent outputs.
- Why not: facts are built from the repository; the Worker adds freshness only for state that lives outside it (issues, CI, coverage, conformance), over a build-time snapshot.

### Cloudflare's own build service, Workers Builds

- Pros: no Cloudflare credential in the repository or in GitHub at all; Cloudflare builds and deploys on every push to the production branch.
- Cons: a second CI, with its own logs that live in the Cloudflare dashboard, are kept for 90 days and cannot be read by an agent or by anyone without account access. It deploys without waiting for the repository's checks. Its checks and bot comments on commits and pull requests carry links with the operator's account identifier. The build needs its own settings, a build command, the GitHub App key as a second copy of a secret, and a build script that repeats what the workflow does.
- Why not: one CI with visible logs and a deploy gated on the checks is worth more than keeping a token out of GitHub. The token is long-lived, which is the trade-off the decision accepts, with a one-Worker scope, a main-only environment and an expiry to limit it.

### A preview per pull request

- Pros: a reviewer sees the real site, with its real headers, before a change merges. `wrangler versions upload` publishes each as a version with its own URL.
- Cons: a version URL or a preview URL is a `workers.dev` hostname of the form `<name>.<account-subdomain>.workers.dev`. The account's subdomain is one per account, and the pull requests of a public repository would carry it in a comment or a status. It names the operator's account to everyone. The only way to keep it out is to switch previews off.
- Why not: preview hostnames would publish the operator's account subdomain. GitHub CI builds the same output and dry-runs the deploy, and `wrangler dev` serves it locally with its `_headers` and `_redirects`, which covers what a review needs. This can be revisited if Cloudflare offers previews on a custom hostname of the project's own zone.

### Resources created by wrangler on first deploy

- Pros: nothing to declare elsewhere; one command creates the Worker and the KV namespace.
- Cons: the resources are not in code that is reviewed; their ids end up in the repository's configuration; the account's state drifts from what anyone declared.
- Why not: the operator's infrastructure code declares every resource, and the repository names none of them.

### GitHub repository milestones, or a milestone field in each file

- Pros: repository milestones are built in and need no project; a field in each record keeps the plan in the repository.
- Cons: repository milestones belong to one repository and are ordered only by due date, while the plan is an ordered list of product stages; a field in every decision record and requirement duplicates what the issue already carries, and changes only by pull request.
- Why not: the project's single-select field gives the order and the grouping in one place, and reordering it needs no code change.

### Project draft items instead of decision issues

- Pros: no issue to open for a decision that is only planned.
- Cons: a draft item has no issue number to link, no labels and no comment thread, so the proposal discussion has nowhere to happen.
- Why not: every planned decision already gets a `type:decision` issue as its proposal, so the issue is the item.
