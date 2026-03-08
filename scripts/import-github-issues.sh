#!/usr/bin/env bash
# Import open GitHub issues into the local beads (bd) database.
# Requires: gh (GitHub CLI), jq, bd
#
# Usage: ./scripts/import-github-issues.sh
#
# See also: https://github.com/steveyegge/beads/issues/1529

set -euo pipefail

gh issue list --state open --json number,title,body,labels --limit 100 | \
  jq -c '.[]' | while read -r issue; do
    number=$(echo "$issue" | jq -r '.number')
    title=$(echo "$issue" | jq -r '.title')
    body=$(echo "$issue" | jq -r '.body // ""')

    echo "Importing #${number}: ${title}"
    bd create "$title" --description="Imported from GitHub #${number}. ${body}" -t task
  done

echo "Done."
