#!/usr/bin/env bash
# Tests hack/modules.py against the real go.mod files and against files that
# must be refused. Run by `just release-chain`.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
mod="$root/hack/modules.py"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "test-modules: $*" >&2; exit 1; }

# Dependencies first: a module is tagged after the ones it requires.
list="$("$mod" list)"
pos() { printf '%s\n' "$list" | grep -nxF "$1" | cut -d: -f1; }
[ "$(pos deploy/pulumi)" -lt "$(pos deploy/pulumi/edge/cloudflare)" ] || fail "the core library is not before the edge"
[ "$(pos audit/sdk)" -lt "$(pos audit)" ] || fail "the audit SDK is not before audit"
for dir in storage audit/deploy/pulumi; do pos "$dir" > /dev/null || fail "$dir is not listed"; done
printf '%s\n' "$list" | grep -qxF . && fail "the root is listed"
"$mod" paths | grep -qxF github.com/truvity/sluis || fail "the root is not among the paths"

# The Pulumi library: the root require moves, and nothing else does.
cp "$root/deploy/pulumi/go.mod" "$tmp/go.mod"
"$mod" pin v9.8.7 "$tmp/go.mod"
grep -qE '^[[:space:]]*github\.com/truvity/sluis v9\.8\.7$' "$tmp/go.mod" || fail "the require was not moved"
diff <(grep -vE '^[[:space:]]*github\.com/truvity/sluis v' "$root/deploy/pulumi/go.mod") \
     <(grep -vE '^[[:space:]]*github\.com/truvity/sluis v' "$tmp/go.mod") >/dev/null || fail "something besides the require changed"
grep -q '^replace github.com/truvity/sluis => ../..$' "$tmp/go.mod" || fail "the replace was lost"
grep -q '^module github.com/truvity/sluis/deploy/pulumi$' "$tmp/go.mod" || fail "the module line was touched"

# An edge module requires the core library too (and the root as an indirect).
edge="$root/deploy/pulumi/edge/cloudflare/go.mod"
"$mod" pin v9.8.7 - < "$edge" > "$tmp/edge.mod"
grep -qE '^[[:space:]]*github\.com/truvity/sluis v9\.8\.7( // indirect)?$' "$tmp/edge.mod" || fail "the edge's root require was not moved"
grep -qE '^[[:space:]]*github\.com/truvity/sluis/deploy/pulumi v9\.8\.7$' "$tmp/edge.mod" || fail "the edge's core require was not moved"
diff <(grep -vE '^[[:space:]]*github\.com/truvity/sluis(/deploy/pulumi)? v' "$edge") \
     <(grep -vE '^[[:space:]]*github\.com/truvity/sluis(/deploy/pulumi)? v' "$tmp/edge.mod") >/dev/null || fail "something besides the requires changed in the edge"
grep -q '^replace github.com/truvity/sluis/deploy/pulumi => ../..$' "$tmp/edge.mod" || fail "the edge's replace was lost"

# audit requires its SDK, which sits beside it.
"$mod" pin v9.8.7 - < "$root/audit/go.mod" > "$tmp/audit.mod"
grep -qE '^[[:space:]]*github\.com/truvity/sluis/audit/sdk v9\.8\.7$' "$tmp/audit.mod" || fail "audit's SDK require was not moved"
grep -q '^replace github.com/truvity/sluis/audit/sdk => ./sdk$' "$tmp/audit.mod" || fail "audit's replace was lost"

# A module that requires none of the repository is unchanged.
"$mod" pin v9.8.7 - < "$root/storage/go.mod" | cmp -s - "$root/storage/go.mod" || fail "storage changed"

# Idempotent, and it reads and writes standard streams.
"$mod" pin v9.8.7 "$tmp/go.mod"
"$mod" pin v9.8.7 - < "$tmp/go.mod" | cmp -s - "$tmp/go.mod" || fail "not idempotent"

# Refused: a version that is not a release, a file with two requires of one module.
"$mod" pin 1.2.3 "$tmp/go.mod" 2>/dev/null && fail "a version with no v was accepted"
"$mod" pin v1.2.3-rc.1 "$tmp/go.mod" || fail "a pre-release was refused"
grep -qE '^[[:space:]]*github\.com/truvity/sluis v1\.2\.3-rc\.1$' "$tmp/go.mod" || fail "the pre-release was not pinned"
"$mod" pin v1.2.3-rc.1.x "$tmp/go.mod" || fail "a dotted pre-release was refused"
"$mod" pin v1.2.3-bad+meta "$tmp/go.mod" 2>/dev/null && fail "build metadata was accepted"
printf 'require (\n\tgithub.com/truvity/sluis v1.0.0\n\tgithub.com/truvity/sluis v1.1.0\n)\n' > "$tmp/two.mod"
"$mod" pin v1.2.3 "$tmp/two.mod" 2>/dev/null && fail "a go.mod with two requires was accepted"

# The release's gate reads the whole chain back.
"$mod" check v9.8.7 > "$tmp/check.out" || fail "check refused the repository"
grep -qxF 'audit/sdk/v9.8.7' "$tmp/check.out" || fail "check did not list the audit SDK's tag"
grep -qxF 'v9.8.7' "$tmp/check.out" || fail "check did not list the root's tag"
echo "modules: ok"
