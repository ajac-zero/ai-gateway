#!/usr/bin/env bash
# Tests report-sync-issue.sh against a fake `gh`. Run: .github/scripts/report-sync-issue_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
fail=0

run_case() { # name, label_create_exit, label_exists(0/1), open_issue_number, result, expect_label(0/1), expect_issue(0/1), expect_comment(0/1), expect_warning(0/1)
  local name="$1" create_rc="$2" exists="$3" open="$4" result="$5" want_label="$6" want_issue="$7" want_comment="$8" want_warn="$9"
  local tmp calls out
  tmp="$(mktemp -d)"
  calls="$tmp/calls"
  : >"$calls"
  mkdir -p "$tmp/bin" "$tmp/out"
  echo '{"payload":{"issues":[{"id":"series-conflict:patch/a"}]}}' >"$tmp/out/report.json"
  echo "sync log line" >"$tmp/out/sync.log"
  cat >"$tmp/bin/gh" <<GH
#!/usr/bin/env bash
echo "\$*" >>"$calls"
case "\$1 \$2" in
  "label create") echo "HTTP 403: Resource not accessible by integration" >&2; exit $create_rc ;;
  "label list") [[ "$exists" == "1" ]] && echo "upstream-sync-conflict"; exit 0 ;;
  "issue list") echo "$open"; exit 0 ;;
  *) exit 0 ;;
esac
GH
  chmod +x "$tmp/bin/gh"
  out="$(PATH="$tmp/bin:$PATH" RESULT="$result" CODE=20 UPSTREAM_SHA=abc OUT="$tmp/out" RUN_URL=http://run \
    ISSUE_LABEL=upstream-sync-conflict ISSUE_TITLE="Upstream sync needs repair" "$here/report-sync-issue.sh" 2>&1)"
  local rc=$? ok=1
  [[ $rc -eq 0 ]] || ok=0
  has() { grep -q -- "$1" "$calls"; }
  if [[ "$want_issue" == 1 ]]; then has "^issue create --title Upstream sync needs repair" || ok=0; else ! has "^issue create" || ok=0; fi
  if [[ "$want_label" == 1 ]]; then has "^issue create .*--label upstream-sync-conflict" || ok=0; else ! has "^issue create .*--label" || ok=0; fi
  if [[ "$want_comment" == 1 ]]; then has "^issue comment 7" || ok=0; else ! has "^issue comment" || ok=0; fi
  if [[ "$want_warn" == 1 ]]; then grep -q '^::warning::.*Resource not accessible' <<<"$out" || ok=0; else ! grep -q '^::warning::' <<<"$out" || ok=0; fi
  if [[ $ok -eq 1 ]]; then echo "ok   $name"; else echo "FAIL $name (rc=$rc)"; cat "$calls"; echo "$out"; fail=1; fi
  rm -rf "$tmp"
}

#        name                              create exists open result    label issue comment warn
run_case "creates label and issue"          0 0 ""  conflict 1 1 0 0
run_case "label create denied: still opens" 1 0 ""  conflict 0 1 0 1
run_case "label already exists"             1 1 ""  conflict 1 1 0 0
run_case "open issue gets a comment"        0 0 7   conflict 0 0 1 0
run_case "error result without issue"       0 0 ""  error    0 0 0 0
run_case "pushed result is a no-op"         0 0 ""  pushed   0 0 0 0
exit $fail
