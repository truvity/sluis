#!/usr/bin/env bash
# Hold the Lambda zips of a release to what they must be.
#
#   hack/check-zips.sh DIR
#
# DIR holds the release's assets (a goreleaser dist/ or a downloaded release).
# For each line of hack/release-zips.txt (RELEASE_ZIPS_FILE names another):
#
#   - exactly one sluis-<name>_<version>_linux_arm64.zip exists;
#   - it holds `bootstrap` and nothing else;
#   - a `module` zip's bootstrap carries the pin `sluis-module=<name>` and no
#     other module's, so it refuses to start as another module; a `legacy` zip
#     carries none;
#   - checksums.txt lists the zip with its SHA-256, when DIR has a checksums.txt
#     (a zip missing from it is an error: a pin that is not checksummed is not a pin).
#
# Needs: unzip, sha256sum, grep.
set -euo pipefail

dir="${1:?usage: check-zips.sh DIR}"
list="${RELEASE_ZIPS_FILE:-$(dirname "$0")/release-zips.txt}"
fail() { echo "check-zips: FAIL: $*" >&2; exit 1; }

shopt -s nullglob
modules=()
while read -r kind name; do
  case "$kind" in ''|'#'*) continue ;; module) modules+=("$name") ;; esac
done < "$list"
[ "${#modules[@]}" -gt 0 ] || fail "$list names no module"

n=0
while read -r kind name; do
  case "$kind" in ''|'#'*) continue ;; esac
  zips=("$dir"/sluis-"$name"_*_linux_arm64.zip)
  [ "${#zips[@]}" -eq 1 ] || fail "want exactly one sluis-${name}_<version>_linux_arm64.zip in $dir, found ${#zips[@]}"
  zip="${zips[0]}"
  base="$(basename "$zip")"
  entries="$(unzip -Z1 "$zip")"
  [ "$entries" = bootstrap ] || fail "$base holds [$(echo "$entries" | tr '\n' ' ')], want bootstrap alone"
  bin="$(mktemp)"
  unzip -p "$zip" bootstrap > "$bin"
  case "$kind" in
    module)
      grep -qa "sluis-module=$name" "$bin" || { rm -f "$bin"; fail "$base is not pinned to its module: no sluis-module=$name in bootstrap"; }
      for other in "${modules[@]}"; do
        [ "$other" = "$name" ] && continue
        ! grep -qa "sluis-module=$other" "$bin" || { rm -f "$bin"; fail "$base carries the pin of module $other"; }
      done ;;
    legacy)
      for any in "${modules[@]}"; do
        ! grep -qa "sluis-module=$any" "$bin" || { rm -f "$bin"; fail "$base is the unpinned zip and carries the pin of module $any"; }
      done ;;
    *) rm -f "$bin"; fail "$list: unknown kind $kind" ;;
  esac
  rm -f "$bin"
  if [ -f "$dir/checksums.txt" ]; then
    want="$(sha256sum "$zip" | cut -d' ' -f1)"
    grep -q "^$want  \*\?$base\$" "$dir/checksums.txt" || fail "$base is not in checksums.txt with its SHA-256 $want"
  fi
  n=$((n + 1))
done < "$list"
echo "zips: $n Lambda zips pinned and checksummed"
