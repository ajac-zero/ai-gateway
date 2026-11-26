#!/usr/bin/env bash
# Fork-only: reports a failed or conflicting Fork Sync run to the "Upstream sync needs repair" issue.
# Used by .github/workflows/fork_sync.yaml; the logic lives here so that it can be tested with a fake `gh`
# (see report-sync-issue_test.sh).
#
# Env: RESULT CODE UPSTREAM_SHA OUT RUN_URL ISSUE_LABEL ISSUE_TITLE (and GH_TOKEN for gh).
#
# - An open issue with the label gets a comment.
# - On a conflict with no open issue, a new issue is created. The label is created first. If that
#   fails, the gh error is surfaced as a workflow warning (it used to be swallowed) and the issue is
#   still opened without the label, so a conflict is never left unreported.
set -euo pipefail

if [[ "$RESULT" == "pushed" ]]; then
  exit 0 # handled by the "Close repair issues" step
fi

issues="$(jq -r '[.. | objects | select(has("id") and (.id|type=="string") and (.id|test("^(series|glue|fork)-"))) | "- `" + .id + "`"] | unique | .[]' "$OUT/report.json" 2>/dev/null || true)"
body="$(
  echo "Run: $RUN_URL"
  echo "Upstream main: \`$UPSTREAM_SHA\`"
  echo "Exit code: \`$CODE\` ($RESULT)"
  echo
  if [[ -n "$issues" ]]; then
    echo "Issues from the report:"
    echo "$issues"
    echo
  fi
  echo "Last lines of the sync log (the full report is in the run's \`fork-sync-report\` artifact):"
  echo '```'
  tail -n 40 "$OUT/sync.log" 2>/dev/null | cut -c1-300 || true
  echo '```'
)"

num="$(gh issue list --label "$ISSUE_LABEL" --state open --json number --jq '.[0].number // empty' 2>/dev/null || true)"
if [[ -n "$num" ]]; then
  gh issue comment "$num" --body "$body"
elif [[ "$RESULT" == "conflict" ]]; then
  label_args=(--label "$ISSUE_LABEL")
  if ! label_err="$(gh label create "$ISSUE_LABEL" --color B60205 --description "Upstream sync needs a person or agent" 2>&1)" &&
    ! gh label list --search "$ISSUE_LABEL" --json name --jq '.[].name' 2>/dev/null | grep -qx "$ISSUE_LABEL"; then
    echo "::warning::could not create label '$ISSUE_LABEL' (opening the issue without it): ${label_err//$'\n'/ }"
    label_args=()
  fi
  gh issue create --title "$ISSUE_TITLE" "${label_args[@]}" --body "$body"
fi
