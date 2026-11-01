#!/usr/bin/env bash
# `go mod tidy` in EVERY Go module of this repository, dependencies first.
#
# `just tidy` tidies the root only. A dependency update that moves the root
# go.mod also moves the go.sum of the modules that replace it with a path
# (audit, deploy/pulumi and the rest), and a tool that tidies only the module
# whose go.mod it edited leaves those behind: the shared CI then fails them as
# "modified the working tree", or the audit e2e jobs as "missing go.sum entry".
#
# Renovate runs this as a postUpgradeTask (renovate.json), where `just` is not
# installed; it needs only bash, python3 and go. Which modules exist is read
# from the go.mod files by hack/modules.py (third_party/ excluded), never listed
# here, so a new module is tidied the day it appears.
#
#     hack/tidy-modules.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

root_path="$(awk '$1 == "module" { print $2; exit }' go.mod)"
mapfile -t dirs < <(python3 -I hack/modules.py list)

tidy() {
  echo "tidy-modules: $1" >&2
  (cd "$1" && go mod tidy)
}

# hack/modules.py lists every module but the root, each after the ones it
# requires. The root goes before the first module that requires it, and after
# the ones it requires itself, so each module is tidied against dependencies
# that are already tidy.
root_done=false
for d in "${dirs[@]}"; do
  if ! $root_done && grep -qE "^[[:space:]]*(require[[:space:]]+)?${root_path//./\\.}[[:space:]]+v" "$d/go.mod"; then
    tidy .
    root_done=true
  fi
  tidy "$d"
done
$root_done || tidy .
