#!/usr/bin/env bash
# scripts/qg/patch-coverage.sh — patch coverage on the PR diff.
#
# Delegates to tools/qg/cmd/qg-diffcover (Go-native). Replaces the previous
# Python pipeline (gocover-cobertura -> diff-cover) whose silent failure
# modes were masking patch-coverage regressions — the Floor 3 result on a
# real PR showed `0% (≥ 60% required)` and still passed, because the parser
# defaulted to 0 when its input file was empty.
#
# Behaviour now:
#   * Auto-skip when the PR touches zero non-test Go files (docs/CI only).
#   * `refactor-only` label relaxes the threshold (move-don't-add).
#   * Hard failure when an in-diff Go file has zero coverage profile entries
#     — that indicates the test invocation forgot `-coverpkg=./...` and we
#     cannot certify what the gate didn't measure.
#
# Exit codes: 0 pass, 1 fail. Sets outputs: percent, covered, total,
# threshold, files_count, uninstrumented, passed.

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

mapfile -t exclude_patterns < <(qg_yq '.coverage_patch.exclude[]?' 2>/dev/null || true)

# Auto-skip when no Go non-test files changed. This keeps docs/CI-only PRs
# from re-running coverage computation for nothing.
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
  qg_set_output covered "0"
  qg_set_output total "0"
  qg_set_output passed true
  qg_set_summary_line "✅ **Patch coverage**: N/A (no Go non-test files changed)"
  exit 0
fi

# Resolve qg-diffcover from PATH (pre-built by setup-qg-tools) or
# build-on-demand into .qg-cache/.
binary=$(qg_tool qg-diffcover)

# Compose exclude flags. Defaults are baked into the script so the Go tool
# can be invoked standalone too; quality-gate.yml extras add to that list.
default_excludes=( "*_test.go" "*.pb.go" "proto/**" "tools/docgen/**" )

args=(
  -coverage "$PROFILE"
  -base "$QG_BASE_REF"
  -threshold "$threshold"
  -markdown "/tmp/qg-patch-coverage.md"
  # Strip only the root module prefix. Operator entries already include
  # "operator/" after the strip, so they match `git diff` paths verbatim.
  # Stripping the operator prefix as well would leave bare "controllers/"
  # which would not match.
  -strip-prefix "github.com/diillson/chatcli"
  -include "*.go"
)
for p in "${default_excludes[@]}"; do args+=( -exclude "$p" ); done
for p in "${exclude_patterns[@]}"; do args+=( -exclude "$p" ); done

set +e
output=$( "$binary" "${args[@]}" 2>&1 )
rc=$?
set -e

# Forward key=value lines from the tool to GitHub Outputs verbatim.
while IFS='=' read -r k v; do
  case "$k" in
    percent|covered|total|threshold|files_changed|uninstrumented)
      qg_set_output "$k" "$v"
      ;;
  esac
done <<< "$output"

percent=$(awk -F= '/^percent=/ { print $2; exit }' <<< "$output")
percent="${percent:-0}"

if [[ $rc -eq 0 ]]; then
  qg_set_output passed true
  qg_set_summary_line "✅ **Patch coverage**: ${percent}% (≥ ${threshold}% required)"
  exit 0
fi

# rc != 0 — fail with the tool's error message. The Go tool itself decides
# what failure means (below threshold OR uninstrumented files in diff).
qg_set_output passed false
qg_set_summary_line "❌ **Patch coverage**: ${percent}% < ${threshold}% required"
qg_set_summary_line ""
qg_set_summary_line "<details><summary>Coverage report</summary>"
qg_set_summary_line ""
if [[ -s /tmp/qg-patch-coverage.md ]]; then
  qg_set_summary_line "$(cat /tmp/qg-patch-coverage.md)"
fi
qg_set_summary_line ""
qg_set_summary_line "</details>"

# Surface the tool's error text in the workflow log too.
printf '%s\n' "$output" >&2

if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "patch coverage gate failed (see step summary)"
  exit 1
fi
qg_warn "patch coverage gate failed (warn-only mode)"
