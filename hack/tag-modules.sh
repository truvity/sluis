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
# EVERY MODULE IS TAGGED AT THE RELEASE COMMIT. That commit is made by
# `just release-pin vX.Y.Z`, which sets every require of a module of this
# repository, in every go.mod, to the release (the replaces stay: they make the
# checkout build against itself). The release gate refuses a commit that is not
# pinned (hack/modules.py check --release).
#
# Before each ref exists the module is built as a consumer would build it, the
# replaces dropped and the other modules fetched at their tags, which is why the
# order is dependencies first (hack/build-as-consumer.sh).
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
  hack/build-as-consumer.sh "$dir" "$dir/go.mod" "$version"

  if ref=$(gh api "repos/$repo/git/ref/tags/$tag" --jq '.object.type + " " + .object.sha' 2>/dev/null); then
    read -r otype existing <<<"$ref"
    # A signed or annotated tag is an object of its own: the ref points at the
    # tag object and only its peeled target is the commit.
    while [ "$otype" = "tag" ]; do
      read -r otype existing < <(gh api "repos/$repo/git/tags/$existing" --jq '.object.type + " " + .object.sha')
    done
    if [ "$otype" = "commit" ] && [ "$existing" = "$sha" ]; then
      echo "$tag already points at $existing"
      continue
    fi
    echo "::error::$tag exists at $existing ($otype), not at the release commit $sha"
    exit 1
  fi
  gh api "repos/$repo/git/refs" -f "ref=refs/tags/$tag" -f "sha=$sha" --jq .ref
done
