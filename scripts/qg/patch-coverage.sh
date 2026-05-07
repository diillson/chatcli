#!/usr/bin/env bash
# scripts/qg/patch-coverage.sh — patch coverage on the PR diff.
#
# Pipeline:
#   1. Convert coverage.out -> coverage.xml via gocover-cobertura.
#   2. Run diff-cover against the XML, comparing to origin/<base>.
#   3. Threshold from .github/quality-gate.yml; relaxed when the PR carries
#      the `refactor-only` label.
#   4. PRs that touch zero non-test Go files are auto-passed (docs/CI only).
#
# Exit codes: 0 pass, 1 fail. Sets outputs: percent, threshold, files_count.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

PROFILE="${1:-coverage.out}"
qg_require_yq

if [[ ! -f "$PROFILE" ]]; then
  qg_fail "coverage profile not found: $PROFILE"
  exit 1
fi

enforcement=$(qg_enforcement coverage_patch)
threshold=$(qg_yq '.coverage_patch.threshold_pct // 60')
refactor_threshold=$(qg_yq '.coverage_patch.refactor_threshold_pct // 30')

if qg_has_label refactor-only; then
  threshold="$refactor_threshold"
  qg_log "refactor-only label detected, relaxing threshold to ${threshold}%"
fi

# Docs-only short-circuit — count non-test, non-generated Go files
go_changed=$(git diff "$QG_BASE_REF"...HEAD --name-only --diff-filter=AM \
              -- '*.go' \
                 ':(exclude)*_test.go' \
                 ':(exclude)*.pb.go' \
                 ':(exclude)proto/**' \
                 ':(exclude)tools/docgen/**' \
              | wc -l | tr -d ' ')

qg_set_output files_count "$go_changed"
qg_set_output threshold "$threshold"

if [[ "$go_changed" == "0" ]]; then
  qg_log "no Go non-test files changed — patch coverage N/A"
  qg_set_output percent "100"
  qg_set_output passed true
  qg_set_summary_line "✅ **Patch coverage**: N/A (no Go non-test files changed)"
  exit 0
fi

# Tools — install if missing (Actions runners cache between steps via setup-go)
if ! command -v gocover-cobertura >/dev/null 2>&1; then
  go install github.com/boumenot/gocover-cobertura@v1.2.0
fi
if ! command -v diff-cover >/dev/null 2>&1; then
  pip install --quiet --no-input 'diff_cover>=9.0.0,<10'
fi

XML=coverage.xml
gocover-cobertura < "$PROFILE" > "$XML"

# diff-cover writes a Markdown report; capture it for the sticky comment.
report_md="diff-cover-report.md"
: > "$report_md"

set +e
diff-cover "$XML" \
  --compare-branch="$QG_BASE_REF" \
  --fail-under "$threshold" \
  --markdown-report "$report_md" \
  --quiet
rc=$?
set -e

# Extract overall % (diff-cover prints "Coverage: NN.N%" to the report)
percent=$(grep -oE 'Coverage:\s*[0-9]+(\.[0-9]+)?%' "$report_md" \
            | head -n1 \
            | grep -oE '[0-9]+(\.[0-9]+)?' \
            | head -n1 \
            || echo "0")

qg_set_output percent "$percent"

if [[ $rc -eq 0 ]]; then
  qg_set_output passed true
  qg_set_summary_line "✅ **Patch coverage**: ${percent}% (≥ ${threshold}% required)"
  exit 0
fi

msg="patch coverage ${percent}% < ${threshold}% required"
qg_set_output passed false
qg_set_summary_line "❌ **Patch coverage**: ${msg}"
qg_set_summary_line ""
qg_set_summary_line "<details><summary>diff-cover report</summary>"
qg_set_summary_line ""
qg_set_summary_line "$(cat "$report_md")"
qg_set_summary_line ""
qg_set_summary_line "</details>"

if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "$msg"
  exit 1
fi
qg_warn "$msg"
