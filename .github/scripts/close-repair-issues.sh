#!/usr/bin/env bash
# Fork-only: closes every open repair issue after a clean sync (see repair-issues.sh for which issues).
# Env: UPSTREAM_SHA AFTER RUN_URL ISSUE_LABEL ISSUE_TITLE (and GH_TOKEN for gh).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=repair-issues.sh
source "$here/repair-issues.sh"

for num in $(list_repair_issues); do
  gh issue close "$num" --comment "Resolved by a clean sync: $RUN_URL (upstream main \`$UPSTREAM_SHA\`, fork/main \`$AFTER\`)."
done
