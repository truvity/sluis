#!/usr/bin/env bash
# Maintainer script: the sluis v2 tracker re-baseline of 2026-11-18, GitHub half.
# Dry run by default, --apply writes. Idempotent: a second run changes nothing.
#
#   1. milestones: rename v2.0-alpha to Parity, create the rest (milestones.tsv)
#   2. existing issues: set the milestone, comment where the plan has a comment file, re-open the
#      issues that the 2026-11-18 decisions bring back (FAPI), close the five Done ones with an
#      evidence comment, close the two CIAM leftovers as not planned with a reason comment
#      (plan-existing.tsv)
#   3. new issues: create each one that no issue of that exact title has (plan-new.tsv, bodies/)
#   4. links: replace {{ref:n}} in the new bodies by the GitHub number of the issue n
#
# The contract is .claude/skills/maintainer-script/SKILL.md. Reads and writes go over REST.
set -euo pipefail

REPO=truvity/sluis
SIBLINGS=(truvity/ritueel)           # repositories to check for duplicate titles
PAUSE=2                              # seconds after each write
HERE=$(cd "$(dirname "$0")" && pwd)  # plan files and bodies live next to the script
APPLY=false
case "${1:-}" in
  "") ;;
  --apply) APPLY=true ;;
  *) echo "usage: $0 [--apply]   (no argument: dry run)" >&2; exit 2 ;;
esac
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
LOG="$HERE/apply.log"

declare -A N=()
bump() { N[$1]=$(( ${N[$1]:-0} + 1 )); }
die() { echo "error: $*" >&2; exit 1; }
# gh never reads the plan files that the loops below feed to the script.
gh() { command gh "$@" </dev/null; }
record() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" >>"$LOG"; }
plan_lines() { grep -v -e '^#' -e '^[[:space:]]*$' "$1"; }

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

load_milestones() { fetch "repos/$REPO/milestones?state=all&per_page=100" "$TMP/ms.json"; }

# ms_number <title>: the milestone number, or empty. "Parity" falls back to v2.0-alpha, which
# the script renames, so a dry run counts the issues in it as already in Parity.
ms_number() {
  local n
  n=$(jq -r --arg t "$1" '[.[] | select(.title == $t)][0].number // empty' "$TMP/ms.json")
  if [ -z "$n" ] && [ "$1" = "Parity" ]; then
    n=$(jq -r '[.[] | select(.title == "v2.0-alpha")][0].number // empty' "$TMP/ms.json")
  fi
  printf '%s' "$n"
}

comment_once() { # comment_once <number> <bodyfile>: only when the same text is absent
  local n=$1 body=$2
  fetch "repos/$REPO/issues/$n/comments?per_page=100" "$TMP/comments.json"
  if jq -e --rawfile t "$body" 'any(.[]; .body == ($t | rtrimstr("\n")))' "$TMP/comments.json" >/dev/null; then
    echo "skip   comment already on #$n"; bump comment_present; return
  fi
  bump comment
  if ! $APPLY; then echo "comment #$n  (body: ${body#"$HERE"/})"; return; fi
  gh api -X POST "repos/$REPO/issues/$n/comments" -F "body=@$body" >/dev/null 2>"$TMP/err" || { cat "$TMP/err" >&2; die "comment failed: #$n"; }
  record "commented #$n"
  sleep "$PAUSE"
}

patch_issue() { # patch_issue <number> <what, for the log> <gh api field args ...>
  local n=$1 what=$2; shift 2
  gh api -X PATCH "repos/$REPO/issues/$n" "$@" >/dev/null 2>"$TMP/err" || { cat "$TMP/err" >&2; die "update failed: #$n ($what)"; }
  record "updated #$n $what"
  sleep "$PAUSE"
}

# render <in file> <out file>: replace {{ref:n}} by #<GitHub number> where it is known.
render() {
  cp "$1" "$2"
  local n g
  while IFS=$'\t' read -r n g; do
    sed -i "s/{{ref:$n}}/#$g/g" "$2"
  done <"$TMP/newnums.tsv"
}

# ---------------------------------------------------------------- reads
for r in "$REPO" "${SIBLINGS[@]}"; do load_issues "$r" "$TMP/issues-${r//\//_}.json"; done
load_milestones
fetch "repos/$REPO/labels?per_page=100" "$TMP/labels.json"
jq -r '.[] | [.number, .state, (.milestone.number // 0), (.title | gsub("[\t\n]"; " "))] | @tsv' "$TMP/issues-${REPO//\//_}.json" >"$TMP/idx.tsv"
$APPLY && echo "APPLYING; log: $LOG" || echo "DRY RUN; pass --apply to write"
echo "read: $(jq length "$TMP/issues-${REPO//\//_}.json") issues of $REPO, $(jq length "$TMP/ms.json") milestones"

# ---------------------------------------------------------------- 1. milestones
echo
echo "== 1. milestones"
while IFS=$'\t' read -r title desc; do
  if jq -e --arg t "$title" 'any(.[]; .title == $t)' "$TMP/ms.json" >/dev/null; then
    echo "skip   milestone exists: $title"; bump ms_present; continue
  fi
  if [ "$title" = "Parity" ] && jq -e 'any(.[]; .title == "v2.0-alpha")' "$TMP/ms.json" >/dev/null; then
    num=$(jq -r '[.[] | select(.title == "v2.0-alpha")][0].number' "$TMP/ms.json")
    bump ms_rename
    if ! $APPLY; then echo "rename milestone #$num v2.0-alpha -> Parity"; continue; fi
    gh api -X PATCH "repos/$REPO/milestones/$num" -f "title=$title" -f "description=$desc" >/dev/null 2>"$TMP/err" || { cat "$TMP/err" >&2; die "rename failed: v2.0-alpha"; }
    record "renamed milestone #$num v2.0-alpha to Parity"
    sleep "$PAUSE"
    continue
  fi
  bump ms_create
  if ! $APPLY; then echo "create milestone: $title"; continue; fi
  gh api -X POST "repos/$REPO/milestones" -f "title=$title" -f "description=$desc" -f state=open >/dev/null 2>"$TMP/err" || { cat "$TMP/err" >&2; die "create failed: milestone $title"; }
  record "created milestone $title"
  sleep "$PAUSE"
done < <(plan_lines "$HERE/milestones.tsv")
$APPLY && load_milestones

# ---------------------------------------------------------------- 2. existing issues
echo
echo "== 2. existing issues"
while IFS=$'\t' read -r num ms action exptitle body; do
  row=$(awk -F'\t' -v n="$num" '$1 == n' "$TMP/idx.tsv")
  if [ -z "$row" ]; then echo "WARN   #$num is not in $REPO (not touched)"; bump missing; continue; fi
  IFS=$'\t' read -r _ state curms title <<<"$row"
  if [ "$exptitle" != "-" ] && [ "$exptitle" != "$title" ]; then
    echo "WARN   #$num title differs from the plan: now \"$title\", plan \"$exptitle\""; bump title_differs
  fi
  # An issue that is already closed (the Linear sync closes the mirror of a Done or Canceled issue) gets
  # no second close; a Done or not-planned one still gets its comment, and a Done one its milestone.
  if [ "$state" = "closed" ]; then
    case "$action" in
      keep) echo "skip   #$num is already closed"; bump already_closed; continue ;;
      reopen) ;;
      *) echo "note   #$num is already closed; the comment and the milestone are still checked"; bump already_closed ;;
    esac
  fi
  target=""
  if [ "$ms" != "-" ]; then
    target=$(ms_number "$ms")
    if [ -z "$target" ] && $APPLY; then die "milestone '$ms' does not exist after step 1"; fi
  fi
  case "$action" in
    keep)
      if [ "$body" != "-" ]; then comment_once "$num" "$HERE/$body"; fi
      if [ -n "$target" ] && [ "$curms" = "$target" ]; then bump ms_already; continue; fi
      bump ms_move
      if ! $APPLY; then echo "move   #$num -> $ms"; continue; fi
      patch_issue "$num" "milestone $ms" -F "milestone=$target"
      ;;
    reopen)
      # An issue that was closed with the CIAM work comes back: open it, set its milestone, then comment.
      if [ "$state" = "closed" ]; then
        bump reopen
        if ! $APPLY; then echo "reopen #$num, milestone $ms"
        else patch_issue "$num" "reopened, milestone $ms" -f state=open -f state_reason=reopened -F "milestone=$target"; fi
      elif [ -n "$target" ] && [ "$curms" = "$target" ]; then bump ms_already
      else
        bump ms_move
        if ! $APPLY; then echo "move   #$num -> $ms"; else patch_issue "$num" "milestone $ms" -F "milestone=$target"; fi
      fi
      if [ "$body" != "-" ]; then comment_once "$num" "$HERE/$body"; fi
      ;;
    done)
      comment_once "$num" "$HERE/$body"
      if [ "$state" = "closed" ]; then
        if [ -n "$target" ] && [ "$curms" = "$target" ]; then continue; fi
        bump ms_move
        if ! $APPLY; then echo "move   #$num (closed) -> $ms"; continue; fi
        patch_issue "$num" "milestone $ms" -F "milestone=$target"
        continue
      fi
      bump close_done
      if ! $APPLY; then echo "close  #$num as completed, milestone $ms"; continue; fi
      patch_issue "$num" "closed as completed, milestone $ms" -f state=closed -f state_reason=completed -F "milestone=$target"
      ;;
    notplanned)
      comment_once "$num" "$HERE/$body"
      if [ "$state" = "closed" ]; then continue; fi
      bump close_notplanned
      if ! $APPLY; then echo "close  #$num as not planned"; continue; fi
      patch_issue "$num" "closed as not planned" -f state=closed -f state_reason=not_planned
      ;;
    *) die "unknown action '$action' for #$num" ;;
  esac
done < <(plan_lines "$HERE/plan-existing.tsv")

# ---------------------------------------------------------------- 3. new issues
echo
echo "== 3. new issues"
: >"$TMP/newnums.tsv"
# Numbers of issues of these titles that already exist, so a second run links them too.
while IFS=$'\t' read -r n ms labels title body; do
  hit=$(jq -r --arg t "$title" '.[] | select(.title == $t) | .number' "$TMP/issues-${REPO//\//_}.json" | head -n 1)
  if [ -n "$hit" ]; then printf '%s\t%s\n' "$n" "$hit" >>"$TMP/newnums.tsv"; fi
done < <(plan_lines "$HERE/plan-new.tsv")
known=" $(cut -f1 "$TMP/newnums.tsv" | tr '\n' ' ')"
while IFS=$'\t' read -r n ms labels title body; do
  hit=$(find_title "$title")
  if [ -n "$hit" ]; then echo "skip   exists as $hit: $title"; bump new_present; known+=" $n "; continue; fi
  target=$(ms_number "$ms")
  if [ -z "$target" ] && $APPLY; then die "milestone '$ms' does not exist"; fi
  IFS=',' read -r -a lab <<<"$labels"
  for l in "${lab[@]}"; do
    jq -e --arg l "$l" 'any(.[]; .name == $l)' "$TMP/labels.json" >/dev/null || die "label '$l' does not exist in $REPO (new issue $n)"
  done
  bump new_create
  if ! $APPLY; then
    echo "create $REPO: $title  [$ms; $labels]  (body: bodies/${body#bodies/})"
    for t in $(grep -o '{{ref:[0-9]*}}' "$HERE/$body" | tr -dc '0-9\n' | sort -un); do
      case "$known" in *" $t "*) ;; *) bump new_forward_ref ;; esac
    done
    known+=" $n "
    continue
  fi
  render "$HERE/$body" "$TMP/body.md"
  args=(-f "title=$title" -F "body=@$TMP/body.md" -F "milestone=$target")
  for l in "${lab[@]}"; do args+=(-f "labels[]=$l"); done
  out=$(gh api -X POST "repos/$REPO/issues" "${args[@]}" 2>"$TMP/err") || { cat "$TMP/err" >&2; die "create failed: $title"; }
  g=$(jq -r .number <<<"$out")
  printf '%s\t%s\n' "$n" "$g" >>"$TMP/newnums.tsv"
  echo "created #$g: $title"
  record "created #$g $title"
  sleep "$PAUSE"
done < <(plan_lines "$HERE/plan-new.tsv")

# ---------------------------------------------------------------- 4. links between the new issues
echo
echo "== 4. links between the new issues"
if $APPLY; then
  while IFS=$'\t' read -r n g; do
    gh api "repos/$REPO/issues/$g" --jq .body >"$TMP/current" 2>"$TMP/err" || { cat "$TMP/err" >&2; die "read failed: #$g"; }
    grep -q '{{ref:' "$TMP/current" || continue
    render "$TMP/current" "$TMP/linked"
    if grep -q '{{ref:' "$TMP/linked"; then echo "WARN   #$g still has an unresolved {{ref:n}}"; bump unresolved; fi
    cmp -s "$TMP/current" "$TMP/linked" && continue
    bump links
    patch_issue "$g" "body links" -F "body=@$TMP/linked"
  done <"$TMP/newnums.tsv"
else
  echo "(the issues of step 3 whose body points at a later one are linked in this step: ${N[new_forward_ref]:-0} references)"
  while IFS=$'\t' read -r n g; do
    jq -r --argjson g "$g" '.[] | select(.number == $g) | .body // ""' "$TMP/issues-${REPO//\//_}.json" >"$TMP/current"
    if grep -q '{{ref:' "$TMP/current"; then echo "link   #$g (existing issue has a {{ref:n}})"; bump links; fi
  done <"$TMP/newnums.tsv"
fi

# ---------------------------------------------------------------- summary
echo
if $APPLY; then echo "SUMMARY (done)"; v="did"; else echo "SUMMARY (dry run: nothing was written)"; v="would do"; fi
cat <<EOF
  milestones: create ${N[ms_create]:-0}, rename ${N[ms_rename]:-0}, already there ${N[ms_present]:-0}
  existing issues: milestone set on ${N[ms_move]:-0}, already right ${N[ms_already]:-0}
  re-opened: ${N[reopen]:-0}
  closed as completed: ${N[close_done]:-0}; closed as not planned: ${N[close_notplanned]:-0}; found closed already (not closed again): ${N[already_closed]:-0}
  comments: ${v} ${N[comment]:-0}, already present ${N[comment_present]:-0}
  new issues: create ${N[new_create]:-0}, already exist ${N[new_present]:-0}
  links between new issues: ${N[links]:-0} bodies, ${N[new_forward_ref]:-0} forward references
  warnings: not found ${N[missing]:-0}, title differs ${N[title_differs]:-0}, unresolved ${N[unresolved]:-0}
EOF
echo "done"
