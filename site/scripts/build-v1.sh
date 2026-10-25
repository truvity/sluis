#!/usr/bin/env bash
# Build the v1 documentation (the MkDocs site on the `master` branch) so that it
# can be served under /v1/ of this site.
#
# usage: site/scripts/build-v1.sh <v1-checkout> <site-output>
#
#   <v1-checkout>  a checkout of the `master` branch (CI: actions/checkout with
#                  `ref: master` and `path: v1-src`)
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
# names `https://truvity.github.io/sluis/schemas/...`, which redirects to the
# same path on the custom domain once it is set, so the root path must resolve.
set -euo pipefail

src=${1:?usage: build-v1.sh <v1-checkout> <site-output>}
dist=${2:?usage: build-v1.sh <v1-checkout> <site-output>}
src=$(cd "$src" && pwd)
dist=$(cd "$dist" && pwd)

for f in mkdocs.yaml hack/pages-requirements.txt hack/fetch-mermaid.sh hack/pages-site.sh; do
  [ -f "$src/$f" ] || { echo "build-v1: $src has no $f; is it a checkout of master?" >&2; exit 1; }
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

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
"$venv/bin/mkdocs" build --strict -f mkdocs.v1.yaml -d "$out"
test -f "$out/assets/vendor/mermaid.min.js" || { echo "build-v1: the v1 site has no Mermaid bundle, so every diagram would show as source" >&2; exit 1; }
hack/pages-site.sh "$out"

rm -rf "$dist/v1" "$dist/schemas"
mkdir -p "$dist/v1"
cp -a "$out/." "$dist/v1/"
cp -a "$out/schemas" "$dist/schemas"
test -f "$dist/v1/index.html" || { echo "build-v1: no v1/index.html in the output" >&2; exit 1; }
echo "build-v1: v1 documentation in $dist/v1 ($(find "$dist/v1" -name '*.html' | wc -l) pages)"
