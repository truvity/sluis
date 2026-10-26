#!/usr/bin/env bash
# Print the newest stable v1 release tag and the commit it points at, as
# "<tag> <commit>". Stable means exactly vMAJOR.MINOR.PATCH with MAJOR 1: module
# tags (audit/v1.2.3), pre-releases (v1.2.3-rc.1) and peeled `^{}` entries are
# not candidates. The commit is the peeled one, not the tag object's own SHA.
#
# usage: site/scripts/latest-v1-tag.sh [<remote>]   (default: origin; a URL works too)
#
# Used by the Pages workflow and by `just site-v1`, so both pick the same tag.
set -euo pipefail

remote=${1:-origin}
refs=$(git ls-remote --tags "$remote")

tag=$(printf '%s\n' "$refs" | awk '{print $2}' \
  | sed -n 's#^refs/tags/\(v1\.[0-9][0-9]*\.[0-9][0-9]*\)$#\1#p' | sort -V | tail -n 1)
[ -n "$tag" ] || { echo "latest-v1-tag: no tag v1.X.Y in $remote" >&2; exit 1; }

# An annotated or signed tag lists its commit on the `^{}` line; a lightweight
# tag has only the plain line, which already is the commit.
commit=$(printf '%s\n' "$refs" | awk -v r="refs/tags/$tag" '$2 == r "^{}" {print $1}')
[ -n "$commit" ] || commit=$(printf '%s\n' "$refs" | awk -v r="refs/tags/$tag" '$2 == r {print $1}')
[ -n "$commit" ] || { echo "latest-v1-tag: no commit for $tag in $remote" >&2; exit 1; }

echo "$tag $commit"
