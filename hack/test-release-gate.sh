#!/usr/bin/env bash
# hack/release-gate.sh against a local repository and a stand-in for `gh`: it
# passes a tag on master whose CI succeeded, and refuses one off master, one
# whose CI failed, was cancelled, never ran or never finished, one whose
# `check` did not pass, and one whose runs could not be read. A run still in
# progress is waited for.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT

# A repository with master, a tag on it, and a tag on a branch master never had.
repo="$dir/repo"
git init -q -b master "$repo"
g() { git -C "$repo" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
g commit -q --allow-empty -m one
g tag v1.0.0
g commit -q --allow-empty -m two
g update-ref refs/remotes/origin/master HEAD
g checkout -q -b side
g commit -q --allow-empty -m off-master
g tag v1.0.1
sha=$(g rev-parse 'v1.0.0^{commit}')

# The stand-in answers from $dir/answers: runs-<n>.json for the n-th runs query
# (the last one repeated), checks.json for the check runs, or fails when a file
# says "fail".
bin="$dir/bin"
mkdir -p "$bin"
cat > "$bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
a="$FAKE_GH_ANSWERS"
case "$2" in
  */actions/workflows/*/runs*)
    n=$(( $(cat "$a/count" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$a/count"
    f="$a/runs-$n.json"; [ -f "$f" ] || f=$(ls "$a"/runs-*.json | sort -V | tail -1)
    ;;
  */check-runs*) f="$a/checks.json" ;;
  *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
[ "$(cat "$f")" != fail ] || { echo "HTTP 403" >&2; exit 1; }
cat "$f"
GH
chmod +x "$bin/gh"

run() { printf '{"workflow_runs":[{"head_sha":"%s","run_number":%s,"run_attempt":1,"status":"%s","conclusion":%s,"html_url":"https://ci.example/%s"}]}' \
    "$sha" "$1" "$2" "$3" "$1"; }
checks() { printf '{"check_runs":[{"name":"check","conclusion":%s}]}' "$1"; }

fail=0
# gate NAME WANT(pass|refuse) TAG [PHRASE]: answers are already in place.
gate() {
    local out rc=0
    out=$(cd "$repo" && PATH="$bin:$PATH" FAKE_GH_ANSWERS="$dir/answers" GITHUB_REPOSITORY=example/repo \
        RELEASE_GATE_WAIT="${WAIT:-2}" RELEASE_GATE_POLL=0 "$root/hack/release-gate.sh" "$3" 2>&1) || rc=$?
    if [ "$2" = pass ] && [ "$rc" -ne 0 ]; then echo "FAIL: $1: refused: $out" >&2; fail=1; fi
    if [ "$2" = refuse ] && [ "$rc" -eq 0 ]; then echo "FAIL: $1: passed: $out" >&2; fail=1; fi
    if [ -n "${4:-}" ] && ! grep -q -- "$4" <<<"$out"; then echo "FAIL: $1: no '$4' in: $out" >&2; fail=1; fi
}
answers() { rm -rf "$dir/answers"; mkdir -p "$dir/answers"; local i=1; for r in "$@"; do echo "$r" > "$dir/answers/runs-$i.json"; i=$((i + 1)); done; }

answers "$(run 7 completed '"success"')"; checks '"success"' > "$dir/answers/checks.json"
gate "master, CI green" pass v1.0.0
gate "off master" refuse v1.0.1 "is not on origin/master"
gate "no such tag" refuse v9.9.9 "no such tag"

answers "$(run 7 completed '"failure"')"; checks '"failure"' > "$dir/answers/checks.json"
gate "CI failed" refuse v1.0.0 "concluded 'failure'"
answers "$(run 7 completed '"cancelled"')"; checks '"cancelled"' > "$dir/answers/checks.json"
gate "CI cancelled" refuse v1.0.0 "concluded 'cancelled'"
answers '{"workflow_runs":[]}'; checks '"success"' > "$dir/answers/checks.json"
gate "no CI run" refuse v1.0.0 "has not completed (no run)"
answers "$(run 7 in_progress null)"; checks null > "$dir/answers/checks.json"
gate "CI never finishes" refuse v1.0.0 "has not completed (in_progress)"
answers "$(run 7 queued null)" "$(run 7 in_progress null)" "$(run 7 completed '"success"')"; checks '"success"' > "$dir/answers/checks.json"
WAIT=30 gate "CI finishes while waited for" pass v1.0.0 "waiting for master's CI"
answers "$(run 7 completed '"success"')"; checks '"failure"' > "$dir/answers/checks.json"
gate "check failed" refuse v1.0.0 "'check' check on"
answers "$(run 7 completed '"success"')"; echo '{"check_runs":[]}' > "$dir/answers/checks.json"
gate "no check ran" refuse v1.0.0 "no 'check' check ran"
# Two runs of the commit (a re-run): the newer one decides.
both() { jq -c -s '{workflow_runs: (map(.workflow_runs) | add)}' <(run "$1" completed "$2") <(run "$3" completed "$4"); }
answers "$(both 8 '"success"' 7 '"failure"')"; checks '"success"' > "$dir/answers/checks.json"
gate "the newer run passed" pass v1.0.0
answers "$(both 7 '"success"' 8 '"failure"')"
gate "the newer run failed" refuse v1.0.0 "concluded 'failure'"
answers fail; checks '"success"' > "$dir/answers/checks.json"
gate "runs unreadable" refuse v1.0.0 "could not be read"
answers "$(run 7 completed '"success"')"; echo fail > "$dir/answers/checks.json"
gate "checks unreadable" refuse v1.0.0 "could not be read"

[ "$fail" -eq 0 ] && echo "release-gate: every case as expected"
exit "$fail"
