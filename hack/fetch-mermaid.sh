#!/usr/bin/env bash
# Puts Mermaid into docs/assets/vendor/ for the documentation site, which draws
# diagrams with it (docs/assets/diagrams.js). It is fetched at build time from
# the npm registry and checked against a pinned SHA-256, so the 3.5 MB bundle
# stays out of git and no reader's browser calls a CDN. Run before `mkdocs build`.
set -euo pipefail

version=11.17.2
sha256=581ed7d74bd9048d0e3a91363927d72ef22942d7722546b27f7cc29e35390eb8

root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/docs/assets/vendor"
mkdir -p "$out"

if [ -f "$out/mermaid.min.js" ] && echo "$sha256  $out/mermaid.min.js" | sha256sum -c --status; then
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "https://registry.npmjs.org/mermaid/-/mermaid-$version.tgz" -o "$tmp/mermaid.tgz"
tar -xzf "$tmp/mermaid.tgz" -C "$tmp" package/dist/mermaid.min.js package/LICENSE
echo "$sha256  $tmp/package/dist/mermaid.min.js" | sha256sum -c --quiet
cp "$tmp/package/dist/mermaid.min.js" "$out/mermaid.min.js"
cp "$tmp/package/LICENSE" "$out/mermaid.LICENSE"
echo "mermaid $version in docs/assets/vendor/"
