#!/usr/bin/env bash
# Build the documentation site GitHub Pages serves: mkdocs (strict) into <out>,
# the Mermaid bundle its diagrams need, and every JSON schema at the path its
# $id says (hack/pages-site.sh). Used by the docs CI group (`just docs-site`)
# and the weekly link check, so the site is built one way. sluis.id is built
# from the `main` branch, which builds /v1/ from the latest v1 release tag.
#
# usage: hack/docs-site.sh [out]   (default _site)
set -euo pipefail
cd "$(dirname "$0")/.."
out="${1:-_site}"

venv=dist/docs-venv
[ -x "$venv/bin/mkdocs" ] || {
  python3 -m venv "$venv"
  "$venv/bin/pip" install --quiet -r hack/pages-requirements.txt
}
hack/fetch-mermaid.sh
"$venv/bin/mkdocs" build --strict -d "$out"
test -f "$out/assets/vendor/mermaid.min.js" || { echo "::error::the site has no Mermaid bundle, so every diagram would show as source"; exit 1; }
hack/pages-site.sh "$out"
touch "$out/.nojekyll"
