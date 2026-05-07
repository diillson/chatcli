#!/usr/bin/env bash
# scripts/qg/cyclo-new.sh — cyclomatic complexity gate for files in the diff.
#
# Project-wide gocyclo is grandfathered at 70 in golangci.yml. We hold NEW
# code to a much lower bar (default 30). This runs gocyclo only against
# files changed in the PR (added or modified, .go non-test, non-generated)
# and ignores files explicitly listed under cyclo_new.exempt in
# .github/quality-gate.yml.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement cyclo_new)
threshold=$(qg_yq '.cyclo_new.max_complexity // 30')

mapfile -t exempt < <(qg_yq '.cyclo_new.exempt[]?' 2>/dev/null || true)

mapfile -t changed < <(qg_diff_changed_files \
                          '*.go' \
                          ':(exclude)*_test.go' \
                          ':(exclude)*.pb.go' \
                          ':(exclude)proto/**' \
                          ':(exclude)tools/docgen/**')

if [[ ${#changed[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_output checked 0
  qg_set_summary_line "✅ **Cyclo (new code)**: no Go files changed"
  exit 0
fi

# Filter exempt
filtered=()
for f in "${changed[@]}"; do
  skip=false
  for ex in "${exempt[@]}"; do
    # Exact path match OR glob match (intentional unquoted RHS).
    # shellcheck disable=SC2053
    if [[ "$f" == "$ex" || "$f" == $ex ]]; then
      skip=true
      break
    fi
  done
  $skip || filtered+=( "$f" )
done

if [[ ${#filtered[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_output checked 0
  qg_set_summary_line "✅ **Cyclo (new code)**: all changed files exempt"
  exit 0
fi

if ! command -v gocyclo >/dev/null 2>&1; then
  go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0
fi

# gocyclo prints "<complexity> <package> <func> <file:line>" for each fn.
# -over <N> => only those above N. We want functions with complexity > threshold.
violations=$(gocyclo -over "$threshold" "${filtered[@]}" || true)

qg_set_output checked "${#filtered[@]}"

if [[ -z "$violations" ]]; then
  qg_set_output passed true
  qg_set_summary_line "✅ **Cyclo (new code)**: ${#filtered[@]} files, none above ${threshold}"
  exit 0
fi

count=$(printf '%s\n' "$violations" | grep -c '^' || echo 0)
qg_set_output passed false
{
  echo "## Cyclomatic complexity > ${threshold}"
  echo
  echo '```'
  echo "$violations"
  echo '```'
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
qg_set_summary_line ""

msg="${count} function(s) exceed cyclomatic complexity ${threshold} in changed files"
if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "$msg"
  exit 1
fi
qg_warn "$msg"
