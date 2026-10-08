#!/usr/bin/env bash
# `just release-pin` on a copy of the repository: every go.mod pinned to a
# release, the gate satisfied, and every module still loads (go.mod consistent
# with itself and its replaces). A pin that left one module on the old version
# fails here as "updates to go.mod needed", which is what CI saw on the pin commit.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
copy="$(mktemp -d)"
trap 'rm -rf "$copy"' EXIT
(cd "$root" && git ls-files -z | grep -zv '^node_modules/' | xargs -0 cp --parents -t "$copy")

version=v1.99.0-rc.1
"$copy/hack/modules.py" pin-all "$version"
"$copy/hack/modules.py" check "$version" --release > /dev/null

export GOWORK=off GOFLAGS=-mod=readonly
for dir in . $("$copy/hack/modules.py" list); do
  # `go list -e` reports a package that cannot build (the console is embedded
  # and not built here) without failing; a go.mod that needs updating, or a
  # module graph that does not load, still fails it.
  (cd "$copy/$dir" && go list -e -deps ./... > /dev/null) || { echo "test-release-pin: $dir does not load after the pin" >&2; exit 1; }
done
echo "release-pin: ok"
