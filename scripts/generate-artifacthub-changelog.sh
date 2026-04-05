#!/usr/bin/env bash
# generate-artifacthub-changelog.sh
#
# Parses the latest version section from CHANGELOG.md and injects it as
# the `artifacthub.io/changes` annotation into one or more Chart.yaml files.
#
# Usage:
#   ./scripts/generate-artifacthub-changelog.sh deploy/helm/chatcli/Chart.yaml [deploy/helm/chatcli-operator/Chart.yaml ...]
#
# Requirements: bash 4+, sed, awk (standard on CI runners)
#
# The script reads CHANGELOG.md from the repository root, extracts the first
# version block (features, bug fixes, etc.), converts each entry into the
# ArtifactHub structured annotation format (kind + description + link), and
# writes the annotation into each Chart.yaml provided as argument.

set -euo pipefail

CHANGELOG="${CHANGELOG_FILE:-CHANGELOG.md}"

if [[ ! -f "$CHANGELOG" ]]; then
  echo "ERROR: $CHANGELOG not found" >&2
  exit 1
fi

if [[ $# -eq 0 ]]; then
  echo "Usage: $0 <Chart.yaml> [Chart.yaml ...]" >&2
  exit 1
fi

# Extract the latest version block (everything between the first ## [...] and the next ## [...])
latest_block=$(awk '
  /^## \[/ { if (found) exit; found=1; next }
  found { print }
' "$CHANGELOG")

if [[ -z "$latest_block" ]]; then
  echo "WARN: No changelog entries found in $CHANGELOG, skipping annotation" >&2
  exit 0
fi

# Parse entries into structured YAML
# Tracks current section (Features -> added, Bug Fixes -> fixed, etc.)
annotation=""
current_kind=""

while IFS= read -r line; do
  # Detect section headers
  if [[ "$line" =~ ^###[[:space:]]+(.*) ]]; then
    section="${BASH_REMATCH[1]}"
    case "$section" in
      Features|Feature)       current_kind="added" ;;
      "Bug Fixes"|"Bug Fix")  current_kind="fixed" ;;
      "Breaking Changes"|"BREAKING CHANGES"|"BREAKING CHANGE") current_kind="changed" ;;
      Deprecations|Deprecated) current_kind="deprecated" ;;
      Removed)                current_kind="removed" ;;
      Security)               current_kind="security" ;;
      *)                      current_kind="changed" ;;
    esac
    continue
  fi

  # Detect changelog entries: * **scope:** description ([#123](url)) ([hash](url))
  if [[ "$line" =~ ^\*[[:space:]]+(.*) ]]; then
    raw_entry="${BASH_REMATCH[1]}"
    [[ -z "$current_kind" ]] && current_kind="changed"

    # Extract description (strip trailing PR/commit links and commit hashes)
    description=$(echo "$raw_entry" | sed -E 's/\s*\(\[[a-f0-9]+\]\([^)]+\)\)//g' | sed -E 's/\s*\(\[#[0-9]+\]\([^)]+\)\)//g' | sed 's/[[:space:]]*$//')

    # Extract PR link if present
    pr_url=""
    pr_number=""
    pr_regex='\(\[#([0-9]+)\]\(([^)]+)\)\)'
    if [[ "$raw_entry" =~ $pr_regex ]]; then
      pr_number="${BASH_REMATCH[1]}"
      pr_url="${BASH_REMATCH[2]}"
    fi

    # Build YAML entry
    annotation+="    - kind: ${current_kind}"$'\n'
    annotation+="      description: \"${description}\""$'\n'
    if [[ -n "$pr_url" ]]; then
      annotation+="      links:"$'\n'
      annotation+="        - name: \"PR #${pr_number}\""$'\n'
      annotation+="          url: ${pr_url}"$'\n'
    fi
  fi
done <<< "$latest_block"

if [[ -z "$annotation" ]]; then
  echo "WARN: No parseable entries in latest changelog section, skipping" >&2
  exit 0
fi

# Build the full annotation value (pipe literal block scalar)
annotation_value="|\n${annotation}"

# Inject into each Chart.yaml
for chart_file in "$@"; do
  if [[ ! -f "$chart_file" ]]; then
    echo "WARN: $chart_file not found, skipping" >&2
    continue
  fi

  # Remove existing artifacthub.io/changes annotation if present
  # This handles multiline YAML block scalars (lines starting with spaces after the key)
  tmpfile=$(mktemp)
  awk '
    /^  artifacthub\.io\/changes:/ { skip=1; next }
    skip && /^    / { next }
    skip && !/^    / { skip=0 }
    { print }
  ' "$chart_file" > "$tmpfile"

  # Find the line number after the last existing artifacthub.io/ annotation,
  # including any multi-line block scalar content that follows it.
  insert_after=$(grep -n 'artifacthub\.io/' "$tmpfile" | tail -1 | cut -d: -f1)

  if [[ -z "$insert_after" ]]; then
    # No existing artifacthub annotations, find the annotations: key
    insert_after=$(grep -n '^annotations:' "$tmpfile" | head -1 | cut -d: -f1)
  fi

  if [[ -n "$insert_after" ]]; then
    # Skip past multi-line block scalar continuation lines (4+ leading spaces)
    # that belong to the annotation found above (e.g., maintainers, crds, links)
    total_lines=$(wc -l < "$tmpfile")
    while [[ $insert_after -lt $total_lines ]]; do
      next_line=$(sed -n "$((insert_after + 1))p" "$tmpfile")
      # Stop when the next line is NOT indented content (block scalar lines
      # use 4+ spaces) or is a new artifacthub.io annotation key
      if [[ ! "$next_line" =~ ^[[:space:]]{4} ]]; then
        break
      fi
      insert_after=$((insert_after + 1))
    done
  fi

  if [[ -z "$insert_after" ]]; then
    echo "WARN: No 'annotations:' block found in $chart_file, skipping" >&2
    rm -f "$tmpfile"
    continue
  fi

  # Insert the changes annotation
  {
    head -n "$insert_after" "$tmpfile"
    echo "  artifacthub.io/changes: |"
    echo -n "$annotation"
    tail -n +"$((insert_after + 1))" "$tmpfile"
  } > "$chart_file"

  rm -f "$tmpfile"
  echo "OK: Updated $chart_file with artifacthub.io/changes annotation"
done
