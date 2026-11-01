#!/usr/bin/env bash
# Build the v1 documentation (the MkDocs site at a v1 release tag) so that it
# can be served under /v1/ of this site.
#
# usage: site/scripts/build-v1.sh <v1-checkout> <site-output>
#
#   <v1-checkout>  a checkout of a v1 release tag, the newest stable one (CI:
#                  actions/checkout with `ref: <tag>` and `path: v1-src`, the tag
#                  from site/scripts/latest-v1-tag.sh)
#   <site-output>  the Starlight build output (site/dist); the v1 site is copied
#                  to <site-output>/v1/
#
# v1's own build is not modified. A one-line config next to its mkdocs.yaml
# inherits it and only changes `site_url`, which is what MkDocs derives its
# canonical links and sitemap from. v1's pages use relative links, so the
# same output works at any base path. The build is v1's: `mkdocs build --strict`,
# its Mermaid bundle (fetched at build time and checked against a pinned
# SHA-256), and the JSON schemas published at the path their $id says.
#
# The schemas are also copied to /schemas/ at the root of the site: their $id
# names `https://truvity.github.io/sluis/schemas/...`. GitHub redirected that to the
# same path on the custom domain while GitHub Pages served the site (off since
# 2026-11-01), so the root path must still resolve on sluis.id.
set -euo pipefail

src=${1:?usage: build-v1.sh <v1-checkout> <site-output>}
dist=${2:?usage: build-v1.sh <v1-checkout> <site-output>}
src=$(cd "$src" && pwd)
dist=$(cd "$dist" && pwd)

for f in mkdocs.yaml hack/pages-requirements.txt hack/fetch-mermaid.sh hack/pages-site.sh; do
  [ -f "$src/$f" ] || { echo "build-v1: $src has no $f; is it a checkout of a v1 release tag?" >&2; exit 1; }
done

cd "$src"
cat > mkdocs.v1.yaml <<'YAML'
INHERIT: mkdocs.yaml
site_url: https://sluis.id/v1/
YAML

venv=dist/docs-venv
[ -x "$venv/bin/mkdocs" ] || {
  python3 -m venv "$venv"
  "$venv/bin/pip" install --quiet -r hack/pages-requirements.txt
}
hack/fetch-mermaid.sh

# Material prints a notice (its end of life, and that MkDocs 2.0 will break existing
# sites) that MkDocs 2.0 does not apply to v1: v1 pins mkdocs==1.6.1 in
# hack/pages-requirements.txt. NO_MKDOCS_2_WARNING is the variable Material reads
# to silence it; the other one only silences a second, ProperDocs notice.
export NO_MKDOCS_2_WARNING=true
export DISABLE_MKDOCS_2_WARNING=true

# The privacy plugin of v1 downloads the fonts at build time, with a 5 second read
# timeout that is not configurable, and `--strict` turns one slow answer from
# fonts.gstatic.com into a failed build ("Couldn't retrieve ..."). The plugin keeps
# what it has fetched in .cache/plugin/privacy, so a repeat fetches only what is
# missing. Repeat the build, up to 3 times, only when the log shows such a fetch
# failure; any other warning or error fails at once, and strict mode stays on.
log=$(mktemp)
out=$(mktemp -d)
trap 'rm -rf "$out" "$log"' EXIT
attempt=1
until "$venv/bin/mkdocs" build --strict -f mkdocs.v1.yaml -d "$out" 2>&1 | tee "$log"; do
  if [ "$attempt" -ge 3 ] || ! grep -q "Couldn't retrieve " "$log"; then
    exit 1
  fi
  attempt=$((attempt + 1))
  echo "build-v1: an external file could not be fetched; building again (attempt $attempt of 3)" >&2
  sleep 5
done
test -f "$out/assets/vendor/mermaid.min.js" || { echo "build-v1: the v1 site has no Mermaid bundle, so every diagram would show as source" >&2; exit 1; }
hack/pages-site.sh "$out"

rm -rf "$dist/v1" "$dist/schemas"
mkdir -p "$dist/v1"
cp -a "$out/." "$dist/v1/"
cp -a "$out/schemas" "$dist/schemas"
test -f "$dist/v1/index.html" || { echo "build-v1: no v1/index.html in the output" >&2; exit 1; }
echo "build-v1: v1 documentation in $dist/v1 ($(find "$dist/v1" -name '*.html' | wc -l) pages)"
