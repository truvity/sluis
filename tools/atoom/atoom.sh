#!/usr/bin/env bash
# The atoom dependency (github.com/truvity/atoom, plan: docs/plan.md there, step 1), kept at ONE commit in four places:
#
#   - go.mod: every github.com/truvity/atoom/<module> requirement, a pseudo-version that ends in the first 12 hex digits of the commit;
#   - Cargo.toml: [workspace.dependencies] atoom-common, `git` and `rev`;
#   - third_party/atoom/proto: atoom's proto files at that commit, which proto/ imports (generated Go comes from atoom's own modules);
#   - third_party/atoom/PIN: the commit and the sha256 of the vendored files.
#
#   tools/atoom/atoom.sh sync <commit>    move all four to <commit> (40 hex digits; needs the network), then run `just proto-gen`
#   tools/atoom/atoom.sh check            offline: the four agree, the vendored files are what the PIN says, and the runtime pins of
#                                         this repository are those of atoom's versions.yaml at that commit
#
# Until atoom has a release the pin is a commit; after its first tag the same script moves to the tag (the PIN then names both).
set -euo pipefail
cd "$(dirname "$0")/../.."

repo=truvity/atoom
dir=third_party/atoom
pin=$dir/PIN

tmp=""

die() { echo "atoom: $*" >&2; exit 1; }

# The sha256 of the vendored files: the list of their hashes, in byte order of the path, hashed again.
digest() {
    (cd "$dir" && find proto versions.yaml -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)
}

pinned() { # key
    sed -n "s/^$1: *//p" "$pin"
}

sync() {
    local commit=${1:-}
    [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "usage: atoom.sh sync <the full commit, 40 hex digits>"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    curl -fsSL "https://codeload.github.com/$repo/tar.gz/$commit" -o "$tmp/atoom.tar.gz"
    mkdir "$tmp/src"
    tar -xzf "$tmp/atoom.tar.gz" -C "$tmp/src" --strip-components=1
    [ -f "$tmp/src/versions.yaml" ] || die "$commit has no versions.yaml"
    rm -rf "$dir/proto"
    mkdir -p "$dir/proto"
    local found=0 p
    for p in "$tmp"/src/*/proto/atoom; do
        [ -d "$p" ] || continue
        cp -R "$p" "$dir/proto/"
        found=1
    done
    [ "$found" -eq 1 ] || die "$commit has no <port>/proto/atoom directory"
    cp "$tmp/src/versions.yaml" "$dir/versions.yaml"
    printf 'source: %s\ncommit: %s\nsha256: %s\n' "$repo" "$commit" "$(digest)" >"$pin"
    # Cargo: the one line of the workspace.
    sed -i -E "s#^(atoom-common = \{ git = \"https://github.com/$repo\", )(rev|tag) = \"[^\"]*\"#\1rev = \"$commit\"#" Cargo.toml
    # Go: every atoom module that go.mod requires.
    local mod
    while IFS= read -r mod; do
        go get "$mod@$commit"
    done < <(awk '$1 ~ "^github.com/truvity/atoom/" { print $1 }' go.mod | sort -u)
    cargo update -p atoom-common
    echo "atoom: moved to $commit; now run: just proto-gen"
}

check() {
    local fail=0
    bad() { echo "atoom: $*" >&2; fail=1; }
    [ -f "$pin" ] || die "$pin is missing"
    local commit
    commit=$(pinned commit)
    [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "the PIN has no commit of 40 hex digits"
    [ "$(pinned source)" = "$repo" ] || bad "the PIN source is not $repo"
    [ "$(pinned sha256)" = "$(digest)" ] || bad "the files under $dir are not what the PIN's sha256 says (run: tools/atoom/atoom.sh sync $commit; never edit them)"
    grep -q "^atoom-common = { git = \"https://github.com/$repo\", rev = \"$commit\" }" Cargo.toml || bad "Cargo.toml: atoom-common is not at rev $commit"
    local mods
    mods=$(awk '$1 ~ "^github.com/truvity/atoom/" { print $1 " " $2 }' go.mod)
    [ -n "$mods" ] || bad "go.mod requires no atoom module"
    local line
    while IFS= read -r line; do
        [ -n "$line" ] || continue
        [[ "${line#* }" == *-"${commit:0:12}" ]] || bad "go.mod: ${line% *} is ${line#* }, not at the commit ${commit:0:12}"
    done <<<"$mods"

    # The runtime pins: atoom's versions.yaml at that commit says what atoom builds with; this repository uses the same.
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
    local f crate pair got
    while IFS= read -r f; do
        for pair in "connectrpc:rust_connectrpc" "buffa:rust_buffa" "buffa-types:rust_buffa" "connectrpc-build:rust_connectrpc_build"; do
            crate=${pair%%:*}
            while IFS= read -r got; do
                [ -z "$got" ] || want "$f" "$crate" "$got" "${v[${pair#*:}]}"
            done < <(sed -n -E "s/^${crate} *= *\"([^\"]*)\".*/\1/p; s/^${crate} *= *\{.*version *= *\"([^\"]*)\".*/\1/p" "$f")
        done
    done < <(git ls-files -- 'Cargo.toml' '*/Cargo.toml')
    [ "$fail" -eq 0 ] || exit 1
    echo "atoom: go.mod, Cargo.toml and $dir are at ${commit:0:12}, and the runtime pins agree with its versions.yaml"
}

case "${1:-}" in
    sync) shift; sync "$@" ;;
    check) check ;;
    *) die "usage: atoom.sh sync <commit> | check" ;;
esac
