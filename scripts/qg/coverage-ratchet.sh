#!/usr/bin/env bash
# scripts/qg/coverage-ratchet.sh — total coverage ratchet against main.
#
# Reads coverage.out (Go cover profile), computes total %, and compares
# against the baseline stored as a commit status `quality-gate/coverage`
# on the latest main commit. The baseline is published by
# .github/workflows/quality-gate-baseline.yml on every push to main.
#
# Behaviour:
#   * No baseline yet (first run): bootstrap mode, accept current value.
#   * Baseline present: require current >= baseline - tolerance.
#   * Hard floor from .github/quality-gate.yml is enforced regardless.
#
# Exit codes: 0 pass, 1 fail. Sets job outputs current/baseline/delta.

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

# Use the in-house qg-cover-total tool instead of `go tool cover -func` so
# this works against merged profiles spanning multiple Go modules
# (root + operator). `go tool cover` needs every package resolvable from
# the current go.mod and errors out otherwise without emitting `total:`.
current=$( "$(qg_tool qg-cover-total)" -profile "$PROFILE" )
if [[ -z "$current" ]]; then
  qg_fail "could not parse coverage from $PROFILE"
  exit 1
fi

hard_floor=$(qg_yq '.coverage_total.hard_floor_pct // 0')
tolerance=$(qg_yq '.coverage_total.tolerance_pct // 0')
enforcement=$(qg_enforcement coverage_total)

# Resolve baseline from main commit status
baseline=""
base_branch="${GITHUB_BASE_REF:-main}"
base_sha=$(git rev-parse "origin/${base_branch}" 2>/dev/null || true)

if [[ -n "$base_sha" && -n "${GITHUB_TOKEN:-}" && -n "${GITHUB_REPOSITORY:-}" ]]; then
  baseline=$(
    gh api "repos/${GITHUB_REPOSITORY}/commits/${base_sha}/statuses" \
      --jq '[.[] | select(.context == "quality-gate/coverage")] | .[0].description' 2>/dev/null \
      | grep -oE '[0-9]+\.[0-9]+' \
      | head -n1 \
      || true
  )
fi

qg_log_kv current "$current"
qg_log_kv baseline "${baseline:-<none>}"
qg_log_kv hard_floor "$hard_floor"
qg_log_kv tolerance "$tolerance"

qg_set_output current "$current"
qg_set_output baseline "${baseline:-}"

# Hard floor — non-negotiable
if awk -v c="$current" -v f="$hard_floor" 'BEGIN { exit !(c + 0 < f + 0) }'; then
  msg="coverage ${current}% below hard floor ${hard_floor}%"
  qg_set_output passed false
  qg_set_summary_line "❌ **Coverage total**: ${msg}"
  if [[ "$enforcement" == "blocking" ]]; then
    qg_fail "$msg"
    exit 1
  fi
  qg_warn "$msg"
  exit 0
fi

# First-run bootstrap
if [[ -z "$baseline" ]]; then
  qg_log "no baseline status on origin/${base_branch} — bootstrap mode, passing"
  qg_set_output passed true
  qg_set_output delta "0"
  qg_set_summary_line "✅ **Coverage total**: ${current}% (bootstrap — no baseline yet)"
  exit 0
fi

# Compare with tolerance
if awk -v c="$current" -v b="$baseline" -v t="$tolerance" \
     'BEGIN { exit !((c + t) + 0 >= b + 0) }'; then
  delta=$(awk -v c="$current" -v b="$baseline" 'BEGIN { printf "%+.2f", c - b }')
  qg_set_output passed true
  qg_set_output delta "$delta"
  qg_set_summary_line "✅ **Coverage total**: ${current}% (baseline ${baseline}%, Δ ${delta})"
  exit 0
fi

delta=$(awk -v c="$current" -v b="$baseline" 'BEGIN { printf "%+.2f", c - b }')
msg="coverage regressed: ${current}% < baseline ${baseline}% (Δ ${delta}, tolerance ${tolerance})"
qg_set_output passed false
qg_set_output delta "$delta"
qg_set_summary_line "❌ **Coverage total**: ${msg}"

if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "$msg"
  exit 1
fi
qg_warn "$msg"
