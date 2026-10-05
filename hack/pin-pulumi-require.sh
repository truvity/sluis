#!/usr/bin/env bash
# Pins the Pulumi library's require of the root module to one release.
#
#   hack/pin-pulumi-require.sh <vX.Y.Z> [<go.mod>]
#
# deploy/pulumi is a module of its own that builds against the root module
# beside it (a `replace`), and a consumer, who does not get the replace, builds
# it against the root module at the version its go.mod REQUIRES. A library
# tagged with an older require compiles for nobody: it asks for a package that
# release does not have. The release workflow therefore tags the library at a
# commit whose go.mod requires the release being cut, and this is the edit.
#
# It rewrites the one require line and nothing else, and refuses a go.mod that
# does not have exactly one, so a reshaped file is a failure here and not a
# library nobody can build. It needs no Go toolchain: the workflow runs it on a
# bare runner. `-` as the file reads standard input and writes standard output.
set -euo pipefail

version="${1:?usage: pin-pulumi-require.sh <vX.Y.Z> [go.mod]}"
file="${2:-$(cd "$(dirname "$0")/.." && pwd)/deploy/pulumi/go.mod}"

if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "pin-pulumi-require: $version is not a release version (vX.Y.Z or a semver pre-release such as vX.Y.Z-rc.1)" >&2
  exit 2
fi

if [ "$file" = - ]; then
  src="$(cat)"
else
  src="$(cat "$file")"
fi

# Exactly one require of the root module: not the library's own path, which
# shares the prefix.
pattern='^[[:space:]]*github\.com/truvity/sluis[[:space:]]+v[0-9][^[:space:]]*[[:space:]]*$'
n="$(printf '%s\n' "$src" | grep -cE "$pattern" || true)"
if [ "$n" != 1 ]; then
  echo "pin-pulumi-require: want exactly one 'github.com/truvity/sluis vX.Y.Z' require line, found $n" >&2
  exit 1
fi

out="$(printf '%s\n' "$src" | sed -E "s|^([[:space:]]*github\.com/truvity/sluis[[:space:]]+)v[0-9][^[:space:]]*([[:space:]]*)\$|\1${version}\2|")"
if [ "$file" = - ]; then
  printf '%s\n' "$out"
else
  printf '%s\n' "$out" > "$file"
fi
