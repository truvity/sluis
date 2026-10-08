#!/usr/bin/env bash
# Builds the Pulumi library the way a consumer will: against the root module at
# the version its (pinned) go.mod requires, with the `replace` that makes this
# checkout build against itself DROPPED.
#
#   hack/build-as-consumer.sh <module dir> <pinned go.mod> <vX.Y.Z>
#
# Why this and not the replace: with the replace the library builds against the
# checkout, which proves nothing about the require. Here the root module comes
# from where a consumer gets it, GOPROXY=direct (the repository, at the release
# tag the workflow has just pushed, so the very commit the library tag will be
# a child of) and not from the checkout, so a library that uses what the release
# does not have, or a require that names another version, fails here, before
# the `deploy/pulumi/vX` ref exists and a proxy can remember it.
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
grep -v -E '^replace[[:space:]]+github\.com/truvity/sluis(/deploy/pulumi)?[[:space:]]' "$pinned" > "$work/consumer.mod"
if grep -q -E '^replace[[:space:]]' "$work/consumer.mod"; then
  echo "build-as-consumer: a replace remains in the pinned go.mod" >&2
  exit 1
fi
grep -q -E "^[[:space:]]*github\.com/truvity/sluis ${version//./\\.}([[:space:]]*//.*)?\$" "$work/consumer.mod" \
  || { echo "build-as-consumer: the pinned go.mod does not require the root at $version" >&2; exit 1; }
cp "$dir/go.sum" "$work/consumer.sum"

cd "$dir"
export GOWORK=off GOPROXY=direct GONOSUMDB='github.com/truvity/*' GONOSUMCHECK=1 GOFLAGS=-mod=mod
# The tag was pushed by the release job; give the repository a moment to serve it.
for attempt in ${ATTEMPTS:-1 2 3 4 5}; do
  if go build -modfile="$work/consumer.mod" ./... ; then break; fi
  [ "$attempt" = 5 ] && { echo "build-as-consumer: the library does not build against the root at $version" >&2; exit 1; }
  sleep 20
done
go vet -modfile="$work/consumer.mod" ./...
echo "build-as-consumer: $dir builds and vets against github.com/truvity/sluis $version"
