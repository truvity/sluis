---
title: Tools, checks and verification
description: Where the sluis tools come from, which recipe runs a check, how to run commands from an agent shell, and when a result counts as verified.
status: shipped
sidebar:
  order: 2
---

## Tools come from devbox

Every tool the repository uses is pinned in `devbox.json`, at an exact version, and locked in `devbox.lock`. Run commands inside the devbox environment: `devbox shell`, or one command at a time with `devbox run -- <command>`.

- Pin exact versions, of tools and of the standards they implement. A range is not a pin.
- Prefer a pinned release binary over a container for a tool.
- Do not use testcontainers.

## Run the recipe, not the tool

The `Justfile` holds the recipes, and CI runs the same recipes. Run the recipe, not the bare tool underneath it: the recipe carries the flags, the order and the extra checks that CI applies.

| Recipe | What it checks |
|---|---|
| `just site-lint` | Page descriptions and statuses, open-decision ids, the requirements catalogue |
| `just site-build` | A strict site build: no warning and no broken internal link |
| `just site-links` | Every link in the built site, internal and external |
| `just site-render` | Every diagram is drawn, in light and dark mode |
| `just site-check` | All of the above: what a pull request must pass |

Read the recipe, and the workflow in `.github/workflows/` that calls it, to know what CI really runs. Build and test recipes for the code arrive with the code, and this table grows with them.

## Commands from an agent shell

An AI agent's shell may reset its working directory between calls, so a `cd` in one call is gone in the next. Use absolute paths:

- `git -C /abs/path/to/worktree status`, `go -C /abs/path ...`, `cargo --manifest-path /abs/path/Cargo.toml ...`;
- or `cd /abs/path && devbox run -- just site-check` as one command.

Never run `go mod init`, `go get` or `cargo init` outside the repository: in the wrong directory they create or change a module that is not this one.

## When a result counts

- Measure before you fix. Find the cause in numbers or in a log line before you change anything.
- Read what a tool printed, not only its exit code. A tool can exit 0 and say it did nothing.
- A green check that tests nothing is not green. A check that finds nothing to check, an empty list of files or an empty sweep, fails rather than passes.
- Say what you ran, with the command and its result. Plausible output that nobody ran is not a contribution.
