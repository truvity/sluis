#!/usr/bin/env bash
# Builds a module of this repository the way a consumer will: against the other
# modules of the repository at the version its (pinned) go.mod requires, with the
# `replace` lines that make this checkout build against itself DROPPED.
#
#   hack/build-as-consumer.sh <module dir> <pinned go.mod> <vX.Y.Z>
#
# Why this and not the replace: with the replace the library builds against the
# checkout, which proves nothing about the require. Here the other modules come
# from where a consumer gets them, GOPROXY=direct (the repository, at the tags
# the workflow has just pushed, dependencies first, so the very commits the
# module tag will be a child of) and not from the checkout, so a module that uses
# what the release does not have, or a require that names another version, fails
# here, before its `<dir>/vX` ref exists and a proxy can remember it.
#
# The pinned go.mod is the file the tag will carry, copied as a -modfile with the
# replace removed; the module's go.sum is copied beside it, and -mod=mod lets
# go add the sums the new require needs to the COPY. The tag's own go.sum is not
# touched: a consumer's build uses their go.sum, not the library's.
set -euo pipefail

dir="${1:?usage: build-as-consumer.sh <module dir> <pinned go.mod> <vX.Y.Z>}"
pinned="${2:?}"
version="${3:?}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
# Every module of the repository (hack/modules.py), not a list kept here.
paths="$("$(dirname "$0")/modules.py" paths)"
alt="$(printf '%s\n' "$paths" | sed 's/\./\\./g' | paste -sd'|')"
grep -v -E "^replace[[:space:]]+(${alt})[[:space:]]" "$pinned" > "$work/consumer.mod"
if grep -q -E '^replace[[:space:]]' "$work/consumer.mod"; then
  echo "build-as-consumer: a replace remains in the pinned go.mod" >&2
  exit 1
fi
# Every require of a module of this repository must be the release.
stale="$(grep -E "^[[:space:]]*(require[[:space:]]+)?(${alt})[[:space:]]+v[0-9]" "$work/consumer.mod" | grep -v -E "[[:space:]]${version//./\\.}([[:space:]]|\$)" || true)"
[ -z "$stale" ] || { echo "build-as-consumer: the pinned go.mod requires a module of this repository at another version than $version: $stale" >&2; exit 1; }
[ -f "$dir/go.sum" ] && cp "$dir/go.sum" "$work/consumer.sum" || : > "$work/consumer.sum"

cd "$dir"
export GOWORK=off GOPROXY=direct GONOSUMDB='github.com/truvity/*' GONOSUMCHECK=1 GOFLAGS=-mod=mod
# The tag was pushed by the release job; give the repository a moment to serve it.
for attempt in ${ATTEMPTS:-1 2 3 4 5}; do
  if go build -modfile="$work/consumer.mod" ./... ; then break; fi
  [ "$attempt" = 5 ] && { echo "build-as-consumer: the module does not build against the repository at $version" >&2; exit 1; }
  sleep 20
done
go vet -modfile="$work/consumer.mod" ./...
echo "build-as-consumer: $dir builds and vets against the repository at $version"
