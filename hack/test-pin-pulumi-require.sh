#!/usr/bin/env bash
# Tests hack/pin-pulumi-require.sh against the real deploy/pulumi/go.mod and
# against files that must be refused. Run by `just pulumi-test`.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
pin="$root/hack/pin-pulumi-require.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "test-pin-pulumi-require: $*" >&2; exit 1; }

# The real go.mod: the require line moves, and nothing else does.
cp "$root/deploy/pulumi/go.mod" "$tmp/go.mod"
"$pin" v9.8.7 "$tmp/go.mod"
grep -qE '^[[:space:]]*github\.com/truvity/sluis v9\.8\.7$' "$tmp/go.mod" || fail "the require was not moved"
diff <(grep -vE '^[[:space:]]*github\.com/truvity/sluis v' "$root/deploy/pulumi/go.mod") \
     <(grep -vE '^[[:space:]]*github\.com/truvity/sluis v' "$tmp/go.mod") >/dev/null || fail "something besides the require changed"
grep -q '^replace github.com/truvity/sluis => ../..$' "$tmp/go.mod" || fail "the replace was lost"
grep -q '^module github.com/truvity/sluis/deploy/pulumi$' "$tmp/go.mod" || fail "the module line was touched"

# Idempotent, and it reads and writes standard streams.
"$pin" v9.8.7 "$tmp/go.mod"
"$pin" v9.8.7 - < "$tmp/go.mod" | cmp -s - "$tmp/go.mod" || fail "not idempotent"

# Refused: a version that is not a release, a file with no require, one with two.
"$pin" 1.2.3 "$tmp/go.mod" 2>/dev/null && fail "a version with no v was accepted"
# A pre-release is a release the trigger (`v*`) and the gate accept: so does the pin.
"$pin" v1.2.3-rc.1 "$tmp/go.mod" || fail "a pre-release was refused"
grep -qE '^[[:space:]]*github\.com/truvity/sluis v1\.2\.3-rc\.1$' "$tmp/go.mod" || fail "the pre-release was not pinned"
"$pin" v1.2.3-rc.1.x "$tmp/go.mod" || fail "a dotted pre-release was refused"
"$pin" v1.2.3-bad+meta "$tmp/go.mod" 2>/dev/null && fail "build metadata was accepted"
printf 'module x\n' > "$tmp/none.mod"
"$pin" v1.2.3 "$tmp/none.mod" 2>/dev/null && fail "a go.mod with no require was accepted"
printf 'require (\n\tgithub.com/truvity/sluis v1.0.0\n\tgithub.com/truvity/sluis v1.1.0\n)\n' > "$tmp/two.mod"
"$pin" v1.2.3 "$tmp/two.mod" 2>/dev/null && fail "a go.mod with two requires was accepted"
echo "pin-pulumi-require: ok"
