#!/usr/bin/env bash
# What the built site must contain, checked in site/dist (or the directory
# given, relative to the repository root). Each check fails closed: a missing
# file or an empty match is a failure.
#
# usage: site/scripts/check-built.sh [dist]
set -euo pipefail
cd "$(dirname "$0")/../.."
dist=${1:-site/dist}
fail=0
bad() { echo "check-built: $*" >&2; fail=1; }

for f in index.html 404.html llms.txt llms-full.txt sitemap-index.xml robots.txt \
         overview/index.html architecture/index.html architecture/context/index.html \
         decisions/index.html roadmap/index.html contributing/index.html; do
  [ -s "$dist/$f" ] || bad "missing or empty: $f"
done

# Search: Pagefind's index and its entry point.
[ -s "$dist/pagefind/pagefind.js" ] || bad "no Pagefind entry point in $dist/pagefind"
[ -n "$(ls "$dist"/pagefind/index 2>/dev/null)" ] || bad "no Pagefind index shards in $dist/pagefind/index"

# Mermaid: the diagram must be a <pre class="mermaid"> that the browser script
# draws, never a code listing that merely looks like one.
ctx="$dist/architecture/context/index.html"
if [ -s "$ctx" ]; then
  grep -q '<pre class="mermaid"' "$ctx" || bad "$ctx has no <pre class=\"mermaid\">: Mermaid is not rendering"
  if grep -q 'language-mermaid' "$ctx"; then bad "$ctx still has a language-mermaid code block"; fi
fi
if grep -rl 'language-mermaid' "$dist" --include=index.html >/dev/null 2>&1; then
  bad "a page ships a mermaid fence as a code listing: $(grep -rl 'language-mermaid' "$dist" --include=index.html | head -3 | tr '\n' ' ')"
fi

# Bundled scripts: no Astro or Vite build-time placeholder (__PREFETCH_PREFETCH_ALL__
# and the like) may survive into the output. One unreplaced placeholder in the
# page script throws a ReferenceError and stops every script after it, including
# the one that draws the Mermaid diagrams. The pattern is the prefixes Astro
# uses, not any __NAME__: Mermaid's own CSS has class names such as __APA__.
# site/scripts/check-render.mjs (just site-render) is the same guard in a real
# browser.
placeholder='__(PREFETCH|EXPERIMENTAL|ASTRO|VITE)_[A-Z0-9_]*__'
if [ -d "$dist/_astro" ]; then
  left=$(grep -rEl "$placeholder" "$dist/_astro" --include='*.js' || true)
  if [ -n "$left" ]; then
    bad "an unreplaced build-time placeholder is left in: $(echo "$left" | head -3 | tr '\n' ' ')($(grep -rEoh "$placeholder" "$dist/_astro" --include='*.js' | sort -u | head -5 | tr '\n' ' '))"
  fi
else
  bad "no $dist/_astro: the site has no bundled scripts"
fi

# The landing page says what v2 is and where v1 went.
grep -q 'v1 docs are at' "$dist/index.html" || bad "the landing page lacks the v2 status paragraph"
grep -q 'a pluggable identity and access broker' "$dist/index.html" || bad "the landing page lacks the tagline"

# The v1 documentation, when it was built in.
if [ -d "$dist/v1" ]; then
  [ -s "$dist/v1/index.html" ] || bad "v1/index.html missing"
  grep -q 'https://sluis.id/v1/' "$dist/v1/index.html" || bad "v1 was not built with site_url https://sluis.id/v1/"
  [ -s "$dist/v1/assets/vendor/mermaid.min.js" ] || bad "v1 has no Mermaid bundle"
  [ -d "$dist/schemas" ] || bad "no /schemas/ at the site root: the \$id URLs of v1's schemas would not resolve"
fi

[ "$fail" -eq 0 ] || exit 1
echo "check-built: ok ($(find "$dist" -name '*.html' | wc -l) HTML files)"
