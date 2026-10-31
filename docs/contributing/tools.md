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
| `just check` | Everything a pull request must pass: `check-rules` and `site-check` |
| `just check-rules` | The repository rules, below, after a self-test of the checks |
| `just check-decisions` | The decision records: file names, the template's sections, the status, the index rows |
| `just check-requirements` | The requirements catalogue: schema, ids, statement and traceability rules |
| `just check-public` | Content that must not be public, in every tracked text file |
| `just check-yaml-ext` | YAML files use `.yaml` |
| `just check-agent-links` | Every path that `AGENTS.md`, `CLAUDE.md` and the skills point to exists |
| `just site-check` | The site: `site-lint`, `site-build`, `site-render` and `site-links` |
| `just site-lint` | Page descriptions and statuses, open-decision ids, the requirements catalogue |
| `just site-build` | A strict site build: no warning and no broken internal link |
| `just site-links` | Every link in the built site, internal and external |
| `just site-render` | Every diagram is drawn, in light and dark mode |

Read the recipe, and the workflow in `.github/workflows/` that calls it, to know what CI really runs. Build and test recipes for the code arrive with the code, and this table grows with them.

## The site

The site is built from `docs/` and `site/` with Astro Starlight, and every page is prerendered into static files.

- `just site-dev` serves it on `http://localhost:4321` with live reload; it runs `astro dev`.
- `just site-build` makes the strict build into `site/dist`, the same build CI checks.

How the site is hosted is decided in [ADR 0063](/decisions/0063-the-site-is-static-on-cloudflare-workers-with-a-live-status-api/), and not built yet. Today `site.yaml` publishes it to GitHub Pages. The target:

- The static output is served as assets on Cloudflare Workers, through the `@astrojs/cloudflare` adapter. With the adapter, `astro dev` runs the site in `workerd`, Cloudflare's runtime, so the `/api/*` routes and the status page's server island run locally as they do in production.
- Cloudflare's build service, Workers Builds, builds and deploys from the repository. Each pull request gets a preview of the site. The repository's CI holds no Cloudflare credential.
- Security headers are in a `_headers` file and redirects in a `_redirects` file, both in the repository.
- The Worker, its KV namespace and the custom domain are declared in the operator's own infrastructure code, outside this repository. The repository holds no account id and no namespace id.

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
