#!/usr/bin/env bash
# The Workers Builds build command: everything site.yaml builds for GitHub Pages
# (the site, the v1 documentation under /v1/, Git LFS images and fonts, the build
# checks), as the Worker's assets in site/dist/client. `yarn wrangler deploy` then
# deploys that, from the redirect the build leaves in site/.wrangler/.
#
# Workers Builds settings (ADR 0063): root directory `site`, build command
# `scripts/cf-build.sh`, deploy command `yarn wrangler deploy`, build variable
# SLUIS_STATUS_KV_ID, no build watch paths (a change under docs/ must build too).
# It runs on a laptop the same way, without the variable and without deploying:
#
#   SLUIS_TARGET=workers site/scripts/cf-build.sh     (any directory)
#
# Needs git, git-lfs (when the repository has LFS files), python3 with venv and pip;
# Node and Yarn come from the build image or devbox.
# Environment:
#   SLUIS_STATUS_KV_ID     the KV namespace id; required in Workers Builds (WORKERS_CI)
#   SLUIS_REPO_URL         where the v1 tag is read from (default: the public repository)
#   SLUIS_V1_TAG           build this v1 tag instead of the newest stable one
#   GH_TOKEN               a token that can read the project board, for the status
#                          page; without it the page says its project data is not
#                          available (the build goes on and says so)
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
export SLUIS_TARGET=workers
repo_url=${SLUIS_REPO_URL:-https://github.com/truvity/sluis}

say() { echo "cf-build: $*"; }
die() { echo "cf-build: $*" >&2; exit 1; }

# The build variable is checked first, so that a missing id costs seconds, not the
# whole build. (The Astro integration checks it again, at the end.)
if [ -n "${WORKERS_CI:-}" ] && [ -z "${SLUIS_STATUS_KV_ID:-}" ]; then
  die "SLUIS_STATUS_KV_ID is not set. Add it as a build variable of the Worker (Settings, Build, Variables and secrets)."
fi

git rev-parse --git-dir >/dev/null 2>&1 || die "$root is not a git checkout; the build needs git history for the v1 tag and Git LFS"

# Git LFS objects (images, fonts): Workers Builds documents git-lfs in its image
# but not whether the clone fetches the objects, so fetch them and then check that no
# file is still a pointer. `git lfs ls-files` marks a missing object with "-".
fetch_lfs() { # <checkout>
  local dir=$1 n missing
  n=$(git -C "$dir" lfs ls-files 2>/dev/null | wc -l)
  [ "$n" -gt 0 ] || { say "$dir has no Git LFS files"; return 0; }
  git -C "$dir" lfs pull
  missing=$(git -C "$dir" lfs ls-files | awk '$2 == "-"' | wc -l)
  [ "$missing" -eq 0 ] || die "$missing of $n Git LFS files in $dir are still pointers after 'git lfs pull'"
  say "$dir: $n Git LFS files fetched"
}
if git lfs version >/dev/null 2>&1; then
  git lfs install --local >/dev/null
  fetch_lfs "$root"
else
  say "WARNING: git-lfs is not installed. check-built.sh still fails the build if an LFS pointer file reaches the output."
fi

# v1: the newest stable v1 release tag, checked out beside the site and built by
# site/scripts/build-v1.sh exactly as the Pages workflow does.
if [ -n "${SLUIS_V1_TAG:-}" ]; then tag=$SLUIS_V1_TAG; commit=
else read -r tag commit < <(site/scripts/latest-v1-tag.sh "$repo_url"); fi
say "v1 documentation: $tag"
rm -rf v1-src
git clone --quiet --depth 1 --branch "$tag" "$repo_url" v1-src
commit=$(git -C v1-src rev-parse HEAD)
git lfs version >/dev/null 2>&1 && fetch_lfs "$root/v1-src"
rm -rf v1-src/.git # the build reads files, not history

command -v python3 >/dev/null || die "python3 is not installed; MkDocs builds the v1 documentation with it"
python3 -c 'import venv, ensurepip' 2>/dev/null || die "python3 has no venv or ensurepip; MkDocs is installed in a virtual environment"

cd site
if [ ! -d node_modules ]; then yarn install --immutable; fi

# The site, strictly (no warning, no broken link) and for the Worker.
if [ -z "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ]; then
  say "WARNING: no GH_TOKEN, so the status page is built without the project board (it says so)."
fi
scripts/strict-build.sh
out=dist/client
[ -d "$out" ] || die "no $out after the build; is SLUIS_TARGET=workers in effect?"

cd "$root"
site/scripts/build-v1.sh v1-src "site/$out"

# Same stamp as site.yaml, so a deployed build can be told from its source.
commit_site=${WORKERS_CI_COMMIT_SHA:-$(git rev-parse HEAD)}
printf '{"commit":"%s","run":"%s","v1_tag":"%s","v1_commit":"%s"}\n' \
  "$commit_site" "${WORKERS_CI_BUILD_UUID:-local}" "$tag" "$commit" > "site/$out/build.json"
cat "site/$out/build.json"

site/scripts/check-built.sh "site/$out"

# Free plan: 20,000 files per Worker version (ADR 0063).
files=$(find "site/$out" -type f | wc -l)
[ "$files" -le 20000 ] || die "$files asset files; the Free plan allows 20,000 per version"
say "ok: $files asset files in site/$out"
