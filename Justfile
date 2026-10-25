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

# Front-matter descriptions of 40 to 160 characters on every page in docs/.
site-lint:
    site/scripts/check-descriptions.mjs

# Every link in the built site, internal and external (lychee.toml). Needs `just site-build`.
site-links:
    lychee --config lychee.toml --root-dir "$PWD/site/dist" --no-progress 'site/dist/**/*.html'

# What a pull request must pass.
site-check: site-lint site-build site-links

# Refresh v1-src from `master` and build the v1 documentation into site/dist/v1.
# The Pages workflow does the same with actions/checkout. Needs python3, jq, curl.
site-v1 ref='origin/master':
    git fetch --quiet origin master
    rm -rf v1-src && mkdir v1-src
    git archive '{{ ref }}' | tar -x -C v1-src
    site/scripts/build-v1.sh v1-src site/dist
    site/scripts/check-built.sh site/dist

# Everything the Pages workflow publishes: the site and v1 under /v1/.
site-all: site-check site-v1
