---
name: maintainer-script
description: Use when issues, comments, labels, milestones or board items need more than a couple of writes, or when the user asks for a script to create, edit, comment on or close issues. Produces a script the maintainer reviews and runs.
---

Read "Writing to issues and the board" in `docs/contributing/workflow.md`, then follow the contract below. An agent opens at most a couple of issues itself; everything else is a script that the maintainer runs, because one bad loop against the GitHub API is hard to undo.

## The contract

- [ ] **Dry run by default.** Without `--apply` the script reads, decides and prints what it would do. It writes nothing.
- [ ] **Idempotent.** Create an issue only when no issue has the exact title; comment only when the same text is absent; replace a body only while it still equals the reviewed copy. Running it twice changes nothing the second time.
- [ ] **Duplicate checks across repositories.** Before it creates anything, it searches open and closed issues by title in the target repository and in the sibling repository, for a cross-repo item.
- [ ] **Reads over REST.** Use `gh api repos/...`, not GraphQL (`gh issue list`, `gh project`): everyone shares the GraphQL budget. Writes go over REST too.
- [ ] **Temp files, never big variables.** A page of issues is hundreds of KB. In a shell that exports its variables (allexport), a value over 128 KiB makes every later `gh` or `jq` exec fail with "Argument list too long". Write each page to a temp file and run `jq` on the file.
- [ ] **The real error.** When a read fails, print `gh`'s own stderr. Do not guess "rate limit".
- [ ] **A pause** after each write (a second or two), so a burst does not trip the secondary rate limit.
- [ ] **An apply log.** With `--apply`, append one line per write (time, action, issue number or title) to a log file next to the script.
- [ ] **Bodies live in files** next to the script, not inline in it. The dry-run output shows which file each write uses.
- [ ] **Scan before you hand it over.** Read the dry-run output and the body files, and scan them for private terms with the maintainer's list (kept outside the repository; do not write the terms into any file here) and for the shapes in `docs/contributing/public-content.md`. Everything in an issue is public.
- [ ] **Never print a token.** `gh` holds the credential; the script does not read it, echo it or put it on a command line.

## Skeleton

Copy it, fill in `plan`, and keep the helpers. Run `bash -n` and the dry run before you hand it over.

```bash
#!/usr/bin/env bash
# Maintainer script: dry run by default, --apply writes. Idempotent.
set -euo pipefail

REPO=truvity/sluis
SIBLINGS=(truvity/ritueel)           # repositories to check for duplicate titles
PAUSE=2                              # seconds after each write
HERE=$(cd "$(dirname "$0")" && pwd)  # bodies live next to the script
APPLY=false
[ "${1:-}" = "--apply" ] && APPLY=true
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
LOG="$HERE/apply.log"

die() { echo "error: $*" >&2; exit 1; }

# fetch <api path> <outfile>: all pages, merged into one JSON array, in a file.
fetch() {
  gh api --paginate "$1" >"$TMP/raw" 2>"$TMP/err" || { cat "$TMP/err" >&2; die "read failed: $1"; }
  jq -s 'add // []' "$TMP/raw" >"$2"
}

# Issues of a repository, open and closed, without pull requests.
load_issues() { # load_issues <owner/repo> <outfile>
  fetch "repos/$1/issues?state=all&per_page=100" "$TMP/all.json"
  jq '[.[] | select(has("pull_request") | not)]' "$TMP/all.json" >"$2"
}

# find_title <title>: print "repo#number" of an issue with exactly this title, if any.
find_title() {
  local r
  for r in "$REPO" "${SIBLINGS[@]}"; do
    jq -r --arg t "$1" --arg r "$r" '.[] | select(.title == $t) | "\($r)#\(.number)"' "$TMP/issues-${r//\//_}.json" | head -n 1
  done | head -n 1
}

record() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" >>"$LOG"; }

create_issue() { # create_issue <title> <bodyfile> [label ...]
  local title=$1 body=$2; shift 2
  local hit; hit=$(find_title "$title")
  if [ -n "$hit" ]; then echo "skip   exists as $hit: $title"; return; fi
  if ! $APPLY; then echo "create $REPO: $title  (body: $body)"; return; fi
  local args=(-f "title=$title" -F "body=@$body") l
  for l in "$@"; do args+=(-f "labels[]=$l"); done
  local out; out=$(gh api -X POST "repos/$REPO/issues" "${args[@]}" 2>"$TMP/err") || { cat "$TMP/err" >&2; die "create failed: $title"; }
  record "created #$(jq -r .number <<<"$out") $title"
  sleep "$PAUSE"
}

comment_once() { # comment_once <number> <bodyfile>: only when the same text is absent
  local n=$1 body=$2
  fetch "repos/$REPO/issues/$n/comments?per_page=100" "$TMP/comments.json"
  if jq -e --rawfile t "$body" 'any(.[]; .body == ($t | rtrimstr("\n")))' "$TMP/comments.json" >/dev/null; then
    echo "skip   comment already on #$n"; return
  fi
  if ! $APPLY; then echo "comment #$n  (body: $body)"; return; fi
  gh api -X POST "repos/$REPO/issues/$n/comments" -F "body=@$body" >/dev/null 2>"$TMP/err" || { cat "$TMP/err" >&2; die "comment failed: #$n"; }
  record "commented #$n"
  sleep "$PAUSE"
}

replace_body() { # replace_body <number> <reviewed-copy> <new-body>: only while the body is still the reviewed copy
  local n=$1 before=$2 after=$3
  gh api "repos/$REPO/issues/$n" --jq .body >"$TMP/current" 2>"$TMP/err" || { cat "$TMP/err" >&2; die "read failed: #$n"; }
  if cmp -s <(tr -d '\r' <"$TMP/current") "$before"; then :; else echo "skip   #$n changed since it was reviewed"; return; fi
  if ! $APPLY; then echo "edit   #$n body  (from: $after)"; return; fi
  gh api -X PATCH "repos/$REPO/issues/$n" -F "body=@$after" >/dev/null 2>"$TMP/err" || { cat "$TMP/err" >&2; die "edit failed: #$n"; }
  record "edited #$n"
  sleep "$PAUSE"
}

for r in "$REPO" "${SIBLINGS[@]}"; do load_issues "$r" "$TMP/issues-${r//\//_}.json"; done
$APPLY && echo "APPLYING; log: $LOG" || echo "DRY RUN; pass --apply to write"

# plan: the writes, in order. One line each.
# create_issue "Title, exactly as it should appear" "$HERE/bodies/title.md" "type:task"
# comment_once 123 "$HERE/bodies/comment-123.md"
# replace_body 123 "$HERE/bodies/123.before.md" "$HERE/bodies/123.after.md"

echo "done"
```

## Handing it over

Tell the maintainer, in the report: the path of the script, the dry-run output (or its path), how many writes it plans of each kind, what the scans found, and the command to run (`./script.sh --apply`). Do not run `--apply` yourself.
