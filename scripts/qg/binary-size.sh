#!/usr/bin/env bash
# scripts/qg/binary-size.sh — Floor 14, binary size budget.
#
# Builds chatcli and the operator manager statically (same flags the
# release Dockerfiles use) and asserts the resulting binary stays under a
# configurable budget. Catches accidental dependency bloat — a single new
# heavy package can add tens of megabytes without tripping any other
# gate.
#
# Skipped when no Go file changes: the build wouldn't differ from the
# previous run.
#
# Exit codes: 0 pass, 1 fail. Sets outputs: passed, chatcli_bytes,
# operator_bytes, chatcli_budget, operator_budget.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

# human prints "12.3MB" for a byte count. Defined up top — bash does not
# hoist function definitions when execution reaches a call before the
# `function`/`name() { ... }` line is parsed.
human() {
  awk -v b="$1" 'BEGIN {
    mb = b / (1024 * 1024)
    printf "%.1fMB", mb
  }'
}

qg_require_yq

enforcement=$(qg_enforcement binary_size)
chatcli_budget_mb=$(qg_yq '.binary_size.chatcli_mb // 100')
operator_budget_mb=$(qg_yq '.binary_size.operator_mb // 100')

go_files=$(git diff "$QG_BASE_REF"...HEAD --name-only -- '*.go' | wc -l | tr -d ' ')
if [[ "$go_files" == "0" ]]; then
  qg_set_output passed true
  qg_set_output chatcli_bytes 0
  qg_set_output operator_bytes 0
  qg_set_summary_line "✅ **Binary size**: N/A (no Go changes)"
  exit 0
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$tmpdir/chatcli" "$QG_REPO_ROOT"
( cd "$QG_REPO_ROOT/operator" && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$tmpdir/operator" ./ )

chatcli_bytes=$(stat -c '%s' "$tmpdir/chatcli" 2>/dev/null || stat -f '%z' "$tmpdir/chatcli")
operator_bytes=$(stat -c '%s' "$tmpdir/operator" 2>/dev/null || stat -f '%z' "$tmpdir/operator")
chatcli_budget_bytes=$(( chatcli_budget_mb * 1024 * 1024 ))
operator_budget_bytes=$(( operator_budget_mb * 1024 * 1024 ))

qg_set_output chatcli_bytes "$chatcli_bytes"
qg_set_output operator_bytes "$operator_bytes"
qg_set_output chatcli_budget "$chatcli_budget_mb"
qg_set_output operator_budget "$operator_budget_mb"

violations=()
if (( chatcli_bytes > chatcli_budget_bytes )); then
  violations+=("chatcli $(human "$chatcli_bytes") > ${chatcli_budget_mb}MB budget")
fi
if (( operator_bytes > operator_budget_bytes )); then
  violations+=("operator $(human "$operator_bytes") > ${operator_budget_mb}MB budget")
fi

if [[ ${#violations[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_summary_line "✅ **Binary size**: chatcli $(human "$chatcli_bytes") / ${chatcli_budget_mb}MB · operator $(human "$operator_bytes") / ${operator_budget_mb}MB"
  exit 0
fi

qg_set_output passed false
{
  echo "## Binary size over budget"
  echo
  for v in "${violations[@]}"; do echo "- $v"; done
  echo
  echo "Adjust the budget in \`.github/quality-gate.yml\` (\`binary_size.*\`) if"
  echo "the growth is intentional, or audit recent dependency additions with"
  echo "\`go mod why <module>\` and \`go build -ldflags='-s -w' && du -sh\`."
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"

msg="binary size: ${violations[*]}"
if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "$msg"
  exit 1
fi
qg_warn "$msg"
