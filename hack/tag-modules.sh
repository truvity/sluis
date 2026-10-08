#!/usr/bin/env bash
# Tags every Go module of the repository, other than the root, at the release.
#
#   GH_TOKEN=... REPO=owner/name GITHUB_SHA=<release commit> hack/tag-modules.sh <vX.Y.Z>
#
# A module in a subdirectory is released by a tag that carries the directory:
# `deploy/pulumi/vX.Y.Z` is how `go get github.com/truvity/sluis/deploy/pulumi@vX.Y.Z`
# finds it, and a plain `vX.Y.Z` does not. The modules are the go.mod files of the
# repository (hack/modules.py), tagged dependencies first.
#
# A MODULE'S TAG IS NOT NECESSARILY THE RELEASE COMMIT. A module that requires
# another module of this repository builds against it beside it (a `replace`),
# and a consumer, who does not get the replace, builds it against the version
# its go.mod REQUIRES. That require used to be bumped by hand before every tag,
# and a library tagged with a stale one compiled for nobody. So such a module's
# tag points at a commit made here: a child of the release commit whose go.mod
# requires THIS release and nothing else differs (hack/modules.py pin, which has
# a test), built as a consumer would build it BEFORE the ref exists, the replace
# dropped and the other modules fetched at their tags (hack/build-as-consumer.sh).
# A module that requires none is tagged at the release commit itself.
#
# Idempotent, so that a re-run of a release does not fail on its own tags: a tag
# that exists counts when it is that very commit. It refuses a tag that exists at
# anything else: a tag fetched once cannot be taken back, and the proxy remembers
# the first.
set -euo pipefail

version="${1:?usage: tag-modules.sh <vX.Y.Z>}"
sha="${GITHUB_SHA:?}"
repo="${REPO:?}"
cd "$(dirname "$0")/.."

for dir in $(hack/modules.py list); do
  tag="$dir/$version"
  pinned="$(mktemp)"
  hack/modules.py pin "$version" - < "$dir/go.mod" > "$pinned"
  hack/build-as-consumer.sh "$dir" "$pinned" "$version"
  pins=true
  if cmp -s "$pinned" "$dir/go.mod"; then pins=false; fi

  if ref=$(gh api "repos/$repo/git/ref/tags/$tag" --jq '.object.type + " " + .object.sha' 2>/dev/null); then
    read -r otype existing <<<"$ref"
    # A signed or annotated tag is an object of its own: the ref points at the
    # tag object and only its peeled target is the commit.
    while [ "$otype" = "tag" ]; do
      read -r otype existing < <(gh api "repos/$repo/git/tags/$existing" --jq '.object.type + " " + .object.sha')
    done
    ok=false
    if [ "$otype" = "commit" ]; then
      if [ "$pins" = false ]; then
        [ "$existing" = "$sha" ] && ok=true
      else
        parent="$(gh api "repos/$repo/git/commits/$existing" --jq '.parents | if length == 1 then .[0].sha else "" end')"
        if [ "$parent" = "$sha" ] \
           && gh api -H 'Accept: application/vnd.github.raw+json' "repos/$repo/contents/$dir/go.mod?ref=$existing" | cmp -s - "$pinned"; then
          ok=true
        fi
      fi
    fi
    if [ "$ok" = true ]; then
      echo "$tag already points at $existing"
      continue
    fi
    echo "::error::$tag exists at $existing ($otype), not where this release puts it (the release commit $sha, with $dir/go.mod pinned to $version where it requires the repository)"
    exit 1
  fi

  target="$sha"
  if [ "$pins" = true ]; then
    base_tree="$(gh api "repos/$repo/git/commits/$sha" --jq .tree.sha)"
    blob="$(gh api "repos/$repo/git/blobs" -f "content=$(base64 -w0 "$pinned")" -f encoding=base64 --jq .sha)"
    tree="$(jq -n --arg base "$base_tree" --arg path "$dir/go.mod" --arg blob "$blob" \
      '{base_tree: $base, tree: [{path: $path, mode: "100644", type: "blob", sha: $blob}]}' \
      | gh api "repos/$repo/git/trees" --input - --jq .sha)"
    target="$(jq -n --arg msg "$dir $version: require the repository's modules at $version" --arg tree "$tree" --arg parent "$sha" \
      '{message: $msg, tree: $tree, parents: [$parent]}' \
      | gh api "repos/$repo/git/commits" --input - --jq .sha)"
  fi
  gh api "repos/$repo/git/refs" -f "ref=refs/tags/$tag" -f "sha=$target" --jq .ref
done
