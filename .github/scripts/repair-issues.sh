#!/usr/bin/env bash
# Fork-only: finds the open "Upstream sync needs repair" issues. Sourced by report-sync-issue.sh and
# close-repair-issues.sh so that reporting and closing agree on which issues are ours.
#
# An issue is ours when it carries $ISSUE_LABEL, or, for issues opened while the label could not be
# created, when its title equals $ISSUE_TITLE exactly and its body contains $ISSUE_MARKER. Matching on the
# title alone would also catch unrelated issues.
ISSUE_MARKER="<!-- fork-sync-repair -->"

# Prints the numbers of the open repair issues, one per line, newest first, without duplicates.
list_repair_issues() {
  {
    gh issue list --label "$ISSUE_LABEL" --state open --limit 100 --json number --jq '.[].number' 2>/dev/null || true
    gh issue list --state open --limit 100 --search "\"$ISSUE_TITLE\" in:title" --json number,title,body 2>/dev/null |
      jq -r --arg title "$ISSUE_TITLE" --arg marker "$ISSUE_MARKER" \
        '.[] | select(.title == $title and (.body | contains($marker))) | .number' || true
  } | sort -rnu
}
