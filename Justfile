# Recipes for the sluis.id website (Astro Starlight, source in site/, content in
# docs/). Run them inside the devbox shell (`devbox shell`, or `devbox run -- just <recipe>`).

set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Install the site's dependencies exactly as the lockfile says.
site-install:
    cd site && yarn install --immutable

# The site on http://localhost:4321 with live reload.
site-dev: site-install
    cd site && yarn dev

# A strict build into site/dist: no warnings, no broken internal link, and the
# built output has what it must (search index, Mermaid, llms.txt, the landing text).
site-build: site-install
    site/scripts/strict-build.sh
    site/scripts/check-built.sh site/dist

# Front-matter descriptions of 40 to 160 characters, a valid status, and
# registered open-decision ids on every page in docs/.
site-lint:
    site/scripts/check-descriptions.mjs
    site/scripts/check-status.mjs

# Every link in the built site, internal and external (lychee.toml). Needs `just site-build`.
site-links:
    lychee --config lychee.toml --root-dir "$PWD/site/dist" --no-progress 'site/dist/**/*.html'

# Open the built site in headless Chrome and assert that every Mermaid diagram is
# drawn (an <svg> in each pre.mermaid), the explorers' script ran and the console
# is clean, in light and dark mode. Needs Chrome or Chromium (CHROME, or
# google-chrome / chromium on PATH) and `just site-build`. Screenshots: pass a
# directory, `just site-render site/dist /tmp/shots`.
site-render dist='site/dist' shots='':
    site/scripts/check-render.mjs {{ dist }} {{ shots }}

# What a pull request must pass.
site-check: site-lint site-build site-render site-links

# Refresh v1-src from the newest stable v1 release tag (or the tag given) and build
# the v1 documentation into site/dist/v1. The Pages workflow picks the tag by the
# same rule (site/scripts/latest-v1-tag.sh). Needs python3, jq, curl.
site-v1 tag='':
    #!/usr/bin/env bash
    set -euo pipefail
    tag='{{ tag }}'
    [ -n "$tag" ] || tag=$(site/scripts/latest-v1-tag.sh origin | cut -d' ' -f1)
    echo "v1 documentation: $tag"
    git fetch --quiet --no-tags origin "+refs/tags/$tag:refs/tags/$tag"
    rm -rf v1-src && mkdir v1-src
    git archive "$tag" | tar -x -C v1-src
    site/scripts/build-v1.sh v1-src site/dist
    site/scripts/check-built.sh site/dist

# Everything the Pages workflow publishes: the site and v1 under /v1/.
site-all: site-check site-v1
