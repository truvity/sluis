---
title: Tools, checks and verification
description: Where the sluis tools come from, which recipe runs a check, how to run commands from an agent shell, and when a result counts as verified.
status: partial
sidebar:
  order: 2
---

## Tools come from devbox

Every tool the repository uses is pinned in `devbox.json`, at an exact version, and locked in `devbox.lock`. Run commands inside the devbox environment: `devbox shell`, or one command at a time with `devbox run -- <command>`.

- Pin exact versions, of tools and of the standards they implement. A range is not a pin.
- Prefer a pinned release binary over a container for a tool.
- Do not use testcontainers.

## Standards first

Follow specifications and standards. Where a standard exists for a protocol, a format or an algorithm, do not invent one; a deliberate deviation from a standard needs a [decision record](/contributing/decisions/). Pin the version of the standard you follow, as you pin the version of a tool.

## Run the recipe, not the tool

The `Justfile` holds the recipes, and CI runs the same recipes. Run the recipe, not the bare tool underneath it: the recipe carries the flags, the order and the extra checks that CI applies.

| Recipe | What it checks |
|---|---|
| `just check` | Everything a pull request must pass: `check-rules`, `site-check` and `site-workers` |
| `just check-rules` | The repository rules, below, after a self-test of the checks |
| `just check-decisions` | The decision records: file names, the template's sections, the status, the index rows |
| `just check-requirements` | The requirements catalogue: schema, ids, statement and traceability rules |
| `just check-public` | Content that must not be public, in every tracked text file |
| `just check-yaml-ext` | YAML files use `.yaml` |
| `just check-agent-links` | `AGENTS.md` exists, `CLAUDE.md` imports it, and every path that they and the skills point to exists |
| `just site-check` | The site: `site-lint`, `site-build`, `site-render` and `site-links` |
| `just site-lint` | Page descriptions and statuses, open-decision ids, and the requirements catalogue |
| `just site-build` | A strict site build: no warning and no broken internal link |
| `just site-links` | Every link in the built site, internal and external |
| `just site-render` | Every diagram is drawn, in light and dark mode |
| `just site-workers` | The Cloudflare Workers build with a fake KV id: the strict build for the Worker, `/v1/`, the headers and redirects checks, and `wrangler deploy --dry-run`. Part of `just check`; the `workers` job of `checks.yaml` runs it |

Read the recipe, and the workflow in `.github/workflows/` that calls it, to know what CI really runs. Build and test recipes for the code arrive with the code, and this table grows with them.

## The site

The site is built from `docs/` and `site/` with Astro Starlight, and every page is prerendered into static files.

- `just site-dev` serves it on `http://localhost:4321` with live reload; it runs `astro dev`.
- `just site-build` makes the strict build into `site/dist`, the same build CI checks.
- The [project status](/status/) page and `/data/repo.json` are built with the rest ([ADR 0063](/decisions/0063-the-site-is-static-on-cloudflare-workers-with-a-live-status-api/)). The page reads the v2 project board during the build, with the token in `GH_TOKEN` or `GITHUB_TOKEN`. Reading an organisation's project needs a token that may read it: locally, `GH_TOKEN=$(gh auth token) devbox run -- just check`. The `GITHUB_TOKEN` of GitHub Actions cannot read organisation projects, so `site.yaml` mints a short-lived read-only token from the `sluis-status` GitHub App instead (`actions/create-github-app-token`, with the repository variable `SLUIS_STATUS_APP_CLIENT_ID` and the secret `SLUIS_STATUS_APP_KEY`); a pull request build has neither, says "project data not available at build time" on the page and in its log, and does not fail. `site/scripts/check-repo-json.mjs` checks `/data/repo.json` against its schema, `site/public/data/repo.schema.json`, and against the records it is built from.

How the site is hosted is decided in [ADR 0063](/decisions/0063-the-site-is-static-on-cloudflare-workers-with-a-live-status-api/). `site.yaml` builds it for Cloudflare Workers and deploys it on every push to `main`, after its checks have passed. The Pages job of the same workflow can still publish to GitHub Pages, on a manual run from `main`, and stays as a fallback until about 2026-11-07. The build has two targets, chosen by `SLUIS_TARGET` (`pages`, the default, or `workers`). They build the same pages.

- **Pages** writes `site/dist` and turns the renamed-page redirects of `astro.config.mjs` into meta-refresh pages. `just site-build` and the `build` job of `site.yaml` use it, and it does not change until GitHub Pages is turned off.
- **Workers** goes through the `@astrojs/cloudflare` adapter and writes `site/dist/client`, the Worker's assets directory, with the Worker configuration, and turns the redirects into a `_redirects` file. Every docs page is prerendered, so Pagefind search keeps working. Only `/api/*` and one server island will run as Worker code, and nothing does yet. The experimental route cache `cacheCloudflare()` stays off, because it bills static asset requests.
- `site/wrangler.jsonc` names the Worker `sluis-site`, a KV binding `STATUS` and the static assets. It holds no account id and no namespace id. The id of the KV namespace is the environment variable `SLUIS_STATUS_KV_ID`: Wrangler does not read variables in its configuration, so `src/cloudflare-kv-id.mjs` writes the id into the configuration the build generates whenever the variable is set. A build without it (a laptop, a pull request) keeps a placeholder id and deploys nothing; `just site-workers` sets a fake one.
- `workers_dev` and `preview_urls` are off, for good. Wrangler turns both on by default, and each publishes a hostname that contains the operator's account subdomain. There is no preview per pull request for that reason ([ADR 0063](/decisions/0063-the-site-is-static-on-cloudflare-workers-with-a-live-status-api/)); a pull request is judged by `just site-workers` in CI, and you can serve the same build with `yarn wrangler dev` in `site/`.
- The adapter and Wrangler are pinned at releases at least two weeks old (`@astrojs/cloudflare` 14.4.0, `wrangler` 4.151.0). The adapter's Vite plugin is pinned to 1.64.0 in `resolutions` so that it brings that Wrangler and no newer one, and `.yarnrc.yml` adds the `prismjs` package that the adapter imports without declaring, and five packages that Starlight asks Vite to pre-bundle through the wrong owner (without them a cold build warns, and the strict build fails). Drop them when an upgrade makes them unnecessary.
- `site/public/_headers` holds the Content-Security-Policy and the other security headers. Inline scripts are listed by hash, and `site/scripts/check-csp.mjs` (run by `check-built.sh`) fails when a page runs one that is not listed, or a listed one is gone, and prints the hashes to paste. Two exceptions are written in the file: `style-src` keeps `'unsafe-inline'` for the style attributes of Starlight and Mermaid, and `/v1/*` has its own policy with inline scripts, because MkDocs Material emits a different one on almost every page.
- **The deploy** is the `deploy-workers` job of `.github/workflows/site.yaml`. It runs on a push to `main` and on a manual run from `main`, after the `build` job has passed, and never on a pull request. It builds the site for the Worker with the same steps as `build` (the strict build with `SLUIS_TARGET=workers`, the v1 documentation under `/v1/`, the build checks, a `build.json` stamp with the commit), then runs `just site-deploy`, which is `yarn wrangler deploy` in `site/`. It rebuilds instead of passing the output of `build` on, because the KV id is written into the generated Worker configuration at build time, from a secret that only this job can read. Deploys never overlap: a deploy waits for the one that is running, and a newer one replaces a deploy that is still waiting.
- The job runs in the GitHub environment `cloudflare`. The environment allows deployments from `main` only, and has no required reviewers. It holds three secrets:

  | Secret | Value |
  |---|---|
  | `CLOUDFLARE_API_TOKEN` | A Cloudflare API token with the permission Workers Scripts: Edit on the operator's Cloudflare account, with an expiry |
  | `CLOUDFLARE_ACCOUNT_ID` | The identifier of that account |
  | `SLUIS_STATUS_KV_ID` | The id of the KV namespace for the `STATUS` binding |

  The maintainer creates the token and rotates it before it expires; a deploy that fails with an authentication error is the sign that it has. The token is long-lived and scoped to the account, which is why only `main` can read it. The job gives each secret to the one step that needs it, and checks first that all three are set, without printing them. GitHub masks a secret's value in the log, and no step prints the KV id or the account id. The status page data comes from the `sluis-status` GitHub App, as in the `build` job.
- Security headers and redirects are files in the repository, and the Worker, its KV namespace and the routes of the domain are declared in the operator's own infrastructure code, outside it. The deploy uploads a new version of the Worker and changes none of them. `just site-workers` and every pull request check hold no Cloudflare credential.

## Commands from an agent shell

An AI agent's shell may reset its working directory between calls, so a `cd` in one call is gone in the next. Use absolute paths:

- `git -C /abs/path/to/worktree status`, `go -C /abs/path ...`, `cargo --manifest-path /abs/path/Cargo.toml ...`;
- or `cd /abs/path && devbox run -- just check` as one command.

Never run `go mod init`, `go get` or `cargo init` outside the repository: in the wrong directory they create or change a module that is not this one.

## Hardware tokens

Tests that use a hardware token, a smart card or a security key use dedicated test devices only, never a device someone uses for real work. Select the test device by its serial number, never by "the first one found", and reset it to its factory state when the test is done. A serial number is not written into the repository.

## When a result counts

- Measure before you fix. Find the cause in numbers or in a log line before you change anything.
- Read what a tool printed, not only its exit code. A tool can exit 0 and say it did nothing.
- A green check that tests nothing is not green. A check that finds nothing to check, an empty list of files or an empty sweep, fails rather than passes.
- Say what you ran, with the command and its result. Plausible output that nobody ran is not a contribution.
