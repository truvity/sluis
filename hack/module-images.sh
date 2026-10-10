#!/usr/bin/env bash
# The container image of each module: ghcr.io/truvity/sluis/sluis-<module>.
#
#   hack/module-images.sh publish <version>   build and push them (the release)
#   hack/module-images.sh check               push them to a throwaway registry
#                                             and assert the names that result
#
# The modules are the `sluis-*` images of hack/release-images.txt, the list the
# release's attest-oci signs from, so an image that is pushed is an image that
# is signed, and the other way round. See hack/ko-modules/.ko.yaml for why this
# is ko and not goreleaser.
set -euo pipefail

cd "$(dirname "$0")/.."
list=hack/release-images.txt
prefix=ghcr.io/truvity/sluis/sluis-

modules() {
  local ref
  while read -r kind ref; do
    if [ "$kind" = image ] && [[ $ref == "$prefix"* ]]; then echo "${ref#"$prefix"}"; fi
  done < <(grep -v '^[[:space:]]*\(#\|$\)' "$list")
}

# push <registry> <version> <latest|none>: <registry>/sluis-<module>:<version>
# for every module, and :latest when asked.
push() {
  local registry=$1 version=$2 latest=$3 module tags
  while read -r module; do
    tags=$version
    if [ "$latest" = latest ]; then tags=$version,latest; fi
    SLUIS_MODULE=$module SLUIS_VERSION=$version KO_DOCKER_REPO="$registry/sluis-$module" \
      KO_CONFIG_PATH=hack/ko-modules \
      ko build ./cmd/sluis --bare --sbom=none --tags "$tags" 2>"${TMPDIR:-/tmp}/ko-$module.log" >/dev/null ||
      { cat "${TMPDIR:-/tmp}/ko-$module.log" >&2; return 1; }
  done < <(modules)
}

case "${1:-}" in
publish)
  version=${2:?usage: module-images.sh publish <version>}
  push "ghcr.io/truvity/sluis" "$version" latest
  ;;
check)
  test -n "$(modules)" || { echo "no sluis-* image in $list" >&2; exit 1; }
  port=$((20000 + RANDOM % 20000))
  crane registry serve --address "127.0.0.1:$port" >/dev/null 2>&1 &
  server=$!
  trap 'kill "$server" 2>/dev/null || true' EXIT
  for _ in $(seq 50); do curl -fs "127.0.0.1:$port/v2/" >/dev/null && break; sleep 0.1; done
  registry="127.0.0.1:$port/truvity/sluis"
  version=0.0.0-check
  push "$registry" "$version" latest
  fail=0
  digests=
  while read -r module; do
    ref="$registry/sluis-$module"
    for tag in "$version" latest; do
      digest=$(crane digest "$ref:$tag" 2>/dev/null) || { echo "FAIL: $ref:$tag was not pushed" >&2; fail=1; continue; }
      if [ "$tag" = "$version" ]; then digests="$digests$digest\n"; fi
    done
  done < <(modules)
  # Exactly the module repositories: a stray repository is the three images
  # landing on one name.
  want=$(modules | sed 's|^|truvity/sluis/sluis-|' | sort)
  got=$(crane catalog "127.0.0.1:$port" | sort)
  if [ "$want" != "$got" ]; then
    printf 'FAIL: repositories pushed differ from hack/release-images.txt\nwant:\n%s\ngot:\n%s\n' "$want" "$got" >&2
    fail=1
  fi
  # Different modules are different binaries.
  if [ "$(printf '%b' "$digests" | sort | uniq -d | wc -l)" -ne 0 ]; then
    echo "FAIL: two module images have the same digest" >&2
    fail=1
  fi
  exit "$fail"
  ;;
*)
  echo "usage: module-images.sh publish <version> | check" >&2
  exit 2
  ;;
esac
