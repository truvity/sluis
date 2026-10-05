#!/usr/bin/env bash
# Refuse a release whose Pulumi library requires the root module at another
# version than the tag.
#
# deploy/pulumi is a module of its own, tagged deploy/pulumi/vX.Y.Z at the
# release commit. While it requires github.com/truvity/sluis, a consumer who
# `go get`s the library at vX.Y.Z builds it against whatever root version the
# require names, which must be the one released with it: a stale require ships
# a library that disagrees with the binaries it configures, and a tag cannot
# be taken back once a proxy has fetched it.
#
# No require is a pass: the library does not import the root any more.
#
# Usage: hack/check-release-require.sh vX.Y.Z [path/to/go.mod]
set -euo pipefail

tag="${1:?usage: check-release-require.sh vX.Y.Z [go.mod]}"
mod="${2:-deploy/pulumi/go.mod}"

if ! [[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "release-check: '$tag' is not a vX.Y.Z tag" >&2
    exit 2
fi
[ -f "$mod" ] || { echo "release-check: $mod not found" >&2; exit 2; }

# The version on a `require` line (single or in a block) of exactly the root
# module; github.com/truvity/sluis/deploy/pulumi and the like are other fields.
required=$(awk '
    /^require[[:space:]]*\(/ { inblock = 1; next }
    inblock && /^\)/         { inblock = 0; next }
    {
        line = $0
        sub(/\/\/.*/, "", line)
        n = split(line, f, /[[:space:]]+/)
        i = 1
        if (f[1] == "require") i = 2
        else if (!inblock) next
        if (f[i] == "" ) i++
        if (f[i] == "github.com/truvity/sluis") print f[i + 1]
    }' "$mod")

if [ -z "$required" ]; then
    echo "release-check: $mod does not require github.com/truvity/sluis: nothing to hold to $tag"
    exit 0
fi
if [ "$(wc -l <<<"$required")" -ne 1 ]; then
    echo "release-check: $mod requires github.com/truvity/sluis more than once: $required" >&2
    exit 1
fi
if [ "$required" != "$tag" ]; then
    echo "release-check: $mod requires github.com/truvity/sluis $required, and the release is $tag." >&2
    echo "Bump the require to $tag (and go mod tidy in deploy/pulumi) in a pull request before tagging." >&2
    exit 1
fi
echo "release-check: $mod requires github.com/truvity/sluis $required, the release"
