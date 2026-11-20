#!/usr/bin/env bash
# The atoom dependency (github.com/truvity/atoom, plan: docs/plan.md there), kept at ONE released version in four places:
#
#   - go.mod: every github.com/truvity/atoom/<module> requirement at vX.Y.Z, the tag atoom pushed for each of its Go modules, and no replace
#     of an atoom module;
#   - Cargo.toml: [workspace.dependencies] every atoom-* crate, `git` and `tag = "vX.Y.Z"` (Cargo.lock records the commit the tag names);
#   - third_party/atoom/proto: atoom's proto files at that tag, which proto/ imports (generated Go comes from atoom's own modules), and
#     third_party/atoom/testdata: the slot map vectors that the Rust twin of key/slotplan reads;
#   - third_party/atoom/PIN: the version, the tag, the commit the tag names, and the sha256 of the vendored files.
#
#   tools/atoom/atoom.sh sync X.Y.Z    move all four to the release X.Y.Z (needs the network; idempotent), then run `just proto-gen`
#   tools/atoom/atoom.sh check         the four agree; the vendored files are what the PIN says; the runtime pins of this repository are
#                                      those of atoom's versions.yaml at that tag; and, from the network, the tag still names the pinned
#                                      commit and atoom's CI job `check` is green on it. It fails closed: when GitHub cannot be read it
#                                      refuses. A token in GH_TOKEN or GITHUB_TOKEN is sent when set (the unauthenticated rate limit is low).
#
# A release of atoom is the only thing sluis pins: a commit that atoom has not tagged is never a pin.
set -euo pipefail
cd "$(dirname "$0")/../.."

repo=truvity/atoom
dir=third_party/atoom
pin=$dir/PIN

tmp=""

die() { echo "atoom: $*" >&2; exit 1; }

# The sha256 of the vendored files: the list of their hashes, in byte order of the path, hashed again.
digest() {
    (cd "$dir" && find proto versions.yaml testdata -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)
}

pinned() { # key
    sed -n "s/^$1: *//p" "$pin"
}

# One call of the GitHub API, read-only: the body on stdout, a failure (network, status, rate limit) as a non-zero exit.
api() { # path
    local args=(-fsSL --retry 3 --retry-delay 2 -H "Accept: application/vnd.github+json" -H "X-GitHub-Api-Version: 2022-11-28")
    local token=${GH_TOKEN:-${GITHUB_TOKEN:-}}
    [ -z "$token" ] || args+=(-H "Authorization: Bearer $token")
    curl "${args[@]}" "https://api.github.com/repos/$repo/$1"
}

# The commit that the tag $1 of atoom names (an annotated tag is followed to its commit).
tag_commit() { # tag
    local ref type sha n=0
    ref=$(api "git/ref/tags/$1") || return 1
    while :; do
        type=$(jq -r '.object.type' <<<"$ref") || return 1
        sha=$(jq -r '.object.sha' <<<"$ref") || return 1
        [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || return 1
        case "$type" in
            commit) echo "$sha"; return 0 ;;
            tag) ref=$(api "git/tags/$sha") || return 1 ;;
            *) return 1 ;;
        esac
        n=$((n + 1))
        [ "$n" -lt 5 ] || return 1
    done
}

# The Go modules of atoom that go.mod requires, direct or indirect.
atoom_modules() {
    go mod edit -json | jq -r '.Require[]? | select(.Path | startswith("github.com/truvity/atoom/")) | .Path' | sort -u
}

sync() {
    local version=${1:-}
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: atoom.sh sync <the release, X.Y.Z>"
    local tag=v$version commit
    commit=$(tag_commit "$tag") || die "the tag $tag of $repo could not be resolved (is it released?)"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    curl -fsSL --retry 3 "https://codeload.github.com/$repo/tar.gz/$commit" -o "$tmp/atoom.tar.gz"
    mkdir "$tmp/src"
    tar -xzf "$tmp/atoom.tar.gz" -C "$tmp/src" --strip-components=1
    [ -f "$tmp/src/versions.yaml" ] || die "$tag has no versions.yaml"
    rm -rf "$dir/proto"
    mkdir -p "$dir/proto"
    local found=0 p
    for p in "$tmp"/src/*/proto/atoom; do
        [ -d "$p" ] || continue
        cp -R "$p" "$dir/proto/"
        found=1
    done
    [ "$found" -eq 1 ] || die "$tag has no <port>/proto/atoom directory"
    cp "$tmp/src/versions.yaml" "$dir/versions.yaml"
    # The vectors of the slot map check, which sluis's Rust twin (actors/store/src/config/slotplan.rs) reads too.
    rm -rf "$dir/testdata"
    mkdir -p "$dir/testdata"
    cp "$tmp/src/key/slotplan/testdata/vectors.json" "$dir/testdata/slotplan-vectors.json"
    printf 'source: %s\nversion: %s\ntag: %s\ncommit: %s\nsha256: %s\n' "$repo" "$version" "$tag" "$commit" "$(digest)" >"$pin"
    # Cargo: the lines of the workspace, one per atoom crate, at the tag.
    sed -i -E "s#^(atoom-[a-z-]+ = \{ git = \"https://github.com/$repo\", )(rev|tag|branch) = \"[^\"]*\"#\1tag = \"$tag\"#" Cargo.toml
    # Go: every atoom module that go.mod requires, at the tag; no replace of an atoom module is left (the placeholder version of the
    # commit pins is gone, because atoom's modules require each other at the tag now).
    local mod old
    while IFS= read -r old; do
        [ -n "$old" ] || continue
        go mod edit -dropreplace="$old"
    done < <(go mod edit -json | jq -r '.Replace[]? | select(.Old.Path | startswith("github.com/truvity/atoom/")) | .Old.Path + (if .Old.Version then "@" + .Old.Version else "" end)')
    while IFS= read -r mod; do
        [ -n "$mod" ] || continue
        go mod edit -require="$mod@$tag"
    done < <(atoom_modules)
    go mod tidy
    # The packages tool is a module of its own that requires this one, so the versions that atoom's modules raise (golang.org/x/...) must
    # be raised there too, or its build and its lint refuse ("updates to go.mod needed").
    (cd tools/release/packages && go mod tidy)
    local crate
    while IFS= read -r crate; do
        cargo update -p "$crate"
    done < <(sed -n -E 's/^(atoom-[a-z-]+) = \{ git = .*/\1/p' Cargo.toml)
    echo "atoom: moved to $tag (${commit:0:12}); now run: just proto-gen"
}

check() {
    local fail=0
    bad() { echo "atoom: $*" >&2; fail=1; }
    [ -f "$pin" ] || die "$pin is missing"
    local version tag commit
    version=$(pinned version)
    tag=$(pinned tag)
    commit=$(pinned commit)
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the PIN has no version, X.Y.Z"
    [ "$tag" = "v$version" ] || die "the PIN's tag is '$tag', not v$version"
    [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "the PIN has no commit of 40 hex digits"
    [ "$(pinned source)" = "$repo" ] || bad "the PIN source is not $repo"
    [ "$(pinned sha256)" = "$(digest)" ] || bad "the files under $dir are not what the PIN's sha256 says (run: tools/atoom/atoom.sh sync $version; never edit them)"
    local crates
    crates=$(sed -n -E 's/^(atoom-[a-z-]+) = \{ git = .*/\1/p' Cargo.toml)
    [ -n "$crates" ] || bad "Cargo.toml: no atoom crate"
    local crate
    while IFS= read -r crate; do
        [ -n "$crate" ] || continue
        grep -q "^$crate = { git = \"https://github.com/$repo\", tag = \"$tag\" }" Cargo.toml || bad "Cargo.toml: $crate is not at tag $tag"
    done <<<"$crates"
    local mods line
    mods=$(go mod edit -json | jq -r '.Require[]? | select(.Path | startswith("github.com/truvity/atoom/")) | .Path + " " + .Version')
    [ -n "$mods" ] || bad "go.mod requires no atoom module"
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        [ "${line#* }" = "$tag" ] || bad "go.mod: ${line% *} is ${line#* }, not $tag"
    done <<<"$mods"
    local rep
    rep=$(go mod edit -json | jq -r '.Replace[]? | select(.Old.Path | startswith("github.com/truvity/atoom/")) | .Old.Path')
    [ -z "$rep" ] || bad "go.mod: an atoom module is replaced ($(tr '\n' ' ' <<<"$rep")); a pin is a release, never a replace"

    # The runtime pins: atoom's versions.yaml at that tag says what atoom builds with; this repository uses the same.
    local key val
    declare -A v
    while IFS= read -r line; do
        [[ "$line" =~ ^[[:space:]]*# || -z "${line// /}" ]] && continue
        key=${line%%:*}
        val=${line#*:}
        val=${val%%#*}
        val=$(echo "$val" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e 's/^"\(.*\)"$/\1/')
        v[$key]=$val
    done <"$dir/versions.yaml"
    want() { [ "$3" = "$4" ] || bad "$1: $2 is '$3', atoom's versions.yaml says '$4'"; }
    want go.mod "connectrpc.com/connect" "$(awk '$1 == "connectrpc.com/connect" { print $2 }' go.mod)" "${v[go_connect]}"
    want go.mod "google.golang.org/protobuf" "$(awk '$1 == "google.golang.org/protobuf" { print $2 }' go.mod)" "${v[go_protobuf]}"
    local f got pair
    while IFS= read -r f; do
        for pair in "connectrpc:rust_connectrpc" "buffa:rust_buffa" "buffa-types:rust_buffa" "connectrpc-build:rust_connectrpc_build"; do
            crate=${pair%%:*}
            while IFS= read -r got; do
                [ -z "$got" ] || want "$f" "$crate" "$got" "${v[${pair#*:}]}"
            done < <(sed -n -E "s/^${crate} *= *\"([^\"]*)\".*/\1/p; s/^${crate} *= *\{.*version *= *\"([^\"]*)\".*/\1/p" "$f")
        done
    done < <(git ls-files -- 'Cargo.toml' '*/Cargo.toml')
    [ "$fail" -eq 0 ] || exit 1

    # The network part, which fails closed: whatever cannot be read is a refusal, not a pass.
    local named
    named=$(tag_commit "$tag") || die "refused: the tag $tag of $repo could not be read from GitHub, so the pin cannot be checked"
    [ "$named" = "$commit" ] || die "refused: the tag $tag of $repo names $named now, but the PIN says $commit (a tag that moved; never pin it)"
    local runs n ok
    runs=$(api "commits/$commit/check-runs?check_name=check&filter=latest&per_page=100") || die "refused: the CI of $repo at $commit could not be read from GitHub, so the pin cannot be checked"
    n=$(jq -r '[.check_runs[]? | select(.name == "check")] | length' <<<"$runs") || die "refused: the answer about the CI of $commit could not be read"
    [[ "$n" =~ ^[0-9]+$ ]] && [ "$n" -ge 1 ] || die "refused: atoom's CI job 'check' has no run on $commit"
    ok=$(jq -r '[.check_runs[] | select(.name == "check") | select(.status == "completed" and .conclusion == "success")] | length' <<<"$runs") || die "refused: the answer about the CI of $commit could not be read"
    [ "$ok" = "$n" ] || die "refused: atoom's CI job 'check' is not green on $commit ($ok of $n runs succeeded)"
    echo "atoom: go.mod, Cargo.toml and $dir are at $tag (${commit:0:12}); the tag names that commit, atoom's CI job 'check' is green on it, and the runtime pins agree with its versions.yaml"
}

case "${1:-}" in
    sync) shift; sync "$@" ;;
    check) check ;;
    *) die "usage: atoom.sh sync <X.Y.Z> | check" ;;
esac
