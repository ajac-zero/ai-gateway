#!/usr/bin/env bash
# Tests report-sync-issue.sh and close-repair-issues.sh against a fake, stateful `gh` that keeps issues in a
# directory. Run: .github/scripts/report-sync-issue_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
fail=0
TITLE="Upstream sync needs repair"

setup() { # label_create_rc
  tmp="$(mktemp -d)"
  mkdir -p "$tmp/bin" "$tmp/out" "$tmp/issues"
  echo '{"payload":{"issues":[{"id":"series-conflict:patch/a"}]}}' >"$tmp/out/report.json"
  echo "sync log line" >"$tmp/out/sync.log"
  echo "$1" >"$tmp/label_rc"
  cat >"$tmp/bin/gh" <<'GH'
#!/usr/bin/env bash
# Fake gh. State: $FAKE/issues/<n>.{title,body,labels,state}; $FAKE/label_rc is the exit of `label create`.
d="$FAKE/issues"
echo "$*" >>"$FAKE/calls"
arg() { local k="$1"; shift; while [[ $# -gt 0 ]]; do [[ "$1" == "$k" ]] && { echo "$2"; return; }; shift; done; }
case "$1 $2" in
  "label create") echo "HTTP 403: Resource not accessible by integration" >&2; exit "$(cat "$FAKE/label_rc")" ;;
  "label list") exit 0 ;;
  "issue list")
    label="$(arg --label "$@")"
    for f in "$d"/*.state; do
      [[ -e "$f" ]] || continue
      n="$(basename "$f" .state)"
      [[ "$(cat "$f")" == open ]] || continue
      [[ -z "$label" ]] || grep -qx "$label" "$d/$n.labels" || continue
      jq -n --argjson n "$n" --arg t "$(cat "$d/$n.title")" --rawfile b "$d/$n.body" '{number:$n,title:$t,body:$b}'
    done | jq -s . | { if [[ -n "$(arg --jq "$@")" ]]; then jq -r "$(arg --jq "$@")"; else cat; fi; } ;;
  "issue create")
    n=$(( $(ls "$d" | grep -c state) + 1 ))
    arg --title "$@" >"$d/$n.title"; arg --body "$@" >"$d/$n.body"; arg --label "$@" >"$d/$n.labels"; echo open >"$d/$n.state" ;;
  "issue comment") echo "$4" >>"$FAKE/comments" ;;
  "issue close") echo closed >"$d/$3.state" ;;
esac
exit 0
GH
  chmod +x "$tmp/bin/gh"
}

env_run() { # script, result
  FAKE="$tmp" PATH="$tmp/bin:$PATH" RESULT="$2" CODE=20 UPSTREAM_SHA=abc AFTER=def OUT="$tmp/out" RUN_URL=http://run \
    ISSUE_LABEL=upstream-sync-conflict ISSUE_TITLE="$TITLE" "$here/$1" 2>&1
}
open_count() { grep -lx open "$tmp"/issues/*.state 2>/dev/null | wc -l; }
check() { # name, condition result
  if [[ "$2" == 1 ]]; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi
}
# unrelated issue: same title, no marker; and a labelled-by-someone-else issue with a different title.
add_issue() { local n="$1"; echo "$2" >"$tmp/issues/$n.title"; echo "$3" >"$tmp/issues/$n.body"; echo "$4" >"$tmp/issues/$n.labels"; echo open >"$tmp/issues/$n.state"; }

# 1. Label can be created: issue is labelled, a repeat comments instead of duplicating, a clean sync closes it.
setup 0
env_run report-sync-issue.sh conflict >/dev/null
env_run report-sync-issue.sh conflict >/dev/null
check "labelled: one issue after two conflicts" "$([[ $(open_count) -eq 1 ]] && echo 1 || echo 0)"
check "labelled: issue has the label" "$(grep -qx upstream-sync-conflict "$tmp/issues/1.labels" && echo 1 || echo 0)"
check "labelled: second conflict commented" "$([[ -s "$tmp/comments" ]] && echo 1 || echo 0)"
env_run close-repair-issues.sh pushed >/dev/null
check "labelled: clean sync closes it" "$([[ $(open_count) -eq 0 ]] && echo 1 || echo 0)"
rm -rf "$tmp"

# 2. Label creation denied: warning, unlabelled issue, repeats do not duplicate, clean sync closes it.
setup 1
out="$(env_run report-sync-issue.sh conflict)"
check "denied: warns with the gh error" "$(grep -q '^::warning::.*Resource not accessible' <<<"$out" && echo 1 || echo 0)"
check "denied: issue opened without label" "$([[ $(open_count) -eq 1 && ! -s "$tmp/issues/1.labels" ]] && echo 1 || echo 0)"
env_run report-sync-issue.sh conflict >/dev/null
env_run report-sync-issue.sh conflict >/dev/null
check "denied: repeated conflicts do not duplicate" "$([[ $(open_count) -eq 1 ]] && echo 1 || echo 0)"
check "denied: repeats commented" "$([[ $(wc -l <"$tmp/comments") -eq 2 ]] && echo 1 || echo 0)"
env_run close-repair-issues.sh pushed >/dev/null
check "denied: clean sync closes the unlabelled issue" "$([[ $(open_count) -eq 0 ]] && echo 1 || echo 0)"
rm -rf "$tmp"

# 3. Unrelated issues are never commented on or closed: same title without the marker, other title with marker.
setup 1
add_issue 1 "$TITLE" "someone else's issue" ""
add_issue 2 "Something else" "<!-- fork-sync-repair -->" ""
env_run report-sync-issue.sh conflict >/dev/null
check "unrelated: conflict opens its own issue" "$([[ -d "$tmp/issues" && -e "$tmp/issues/3.state" && ! -s "$tmp/comments" ]] && echo 1 || echo 0)"
env_run close-repair-issues.sh pushed >/dev/null
check "unrelated: only ours is closed" "$([[ "$(cat "$tmp/issues/1.state")" == open && "$(cat "$tmp/issues/2.state")" == open && "$(cat "$tmp/issues/3.state")" == closed ]] && echo 1 || echo 0)"
rm -rf "$tmp"

# 4. Results that must not open issues.
setup 0
env_run report-sync-issue.sh error >/dev/null
env_run report-sync-issue.sh pushed >/dev/null
check "error/pushed without an open issue open nothing" "$([[ $(open_count) -eq 0 ]] && echo 1 || echo 0)"
add_issue 1 "x" "y" "upstream-sync-conflict"
env_run report-sync-issue.sh error >/dev/null
check "error comments on an open labelled issue" "$([[ -s "$tmp/comments" ]] && echo 1 || echo 0)"
rm -rf "$tmp"
exit $fail
