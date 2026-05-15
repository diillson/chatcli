#!/usr/bin/env bash
# scripts/qg/scope-budget.sh — PR size discipline.
#
# Counts files changed and lines added+removed in the PR diff, then enforces
# warn/block thresholds from .github/quality-gate.yml. Bypass labels:
#   * large-pr-approved  -> waives LOC threshold
#   * wide-pr-approved   -> waives files threshold
#
# The breakdown shows tooling-only paths separately so reviewers can tell
# "1500 LOC of YAML/scripts" from "1500 LOC of Go".

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement scope_budget)
loc_warn=$(qg_yq '.scope_budget.loc.warn // 800')
loc_block=$(qg_yq '.scope_budget.loc.block // 2000')
files_warn=$(qg_yq '.scope_budget.files.warn // 25')
files_block=$(qg_yq '.scope_budget.files.block // 50')
loc_label=$(qg_yq '.scope_budget.loc.bypass_label // "large-pr-approved"')
files_label=$(qg_yq '.scope_budget.files.bypass_label // "wide-pr-approved"')

mapfile -t tooling_paths < <(qg_yq '.scope_budget.tooling_paths[]?' 2>/dev/null || true)

stat_line=$(git diff "$QG_BASE_REF"...HEAD --shortstat || true)
files=$(grep -oE '[0-9]+ file' <<< "$stat_line" | grep -oE '[0-9]+' || echo 0)
ins=$(grep -oE '[0-9]+ insertion' <<< "$stat_line" | grep -oE '[0-9]+' || echo 0)
del=$(grep -oE '[0-9]+ deletion' <<< "$stat_line" | grep -oE '[0-9]+' || echo 0)
loc=$((ins + del))

# Tooling subset
tooling_loc=0
tooling_files=0
if [[ ${#tooling_paths[@]} -gt 0 ]]; then
  tool_stat=$(git diff "$QG_BASE_REF"...HEAD --shortstat -- "${tooling_paths[@]}" || true)
  tooling_files=$(grep -oE '[0-9]+ file' <<< "$tool_stat" | grep -oE '[0-9]+' || echo 0)
  ti=$(grep -oE '[0-9]+ insertion' <<< "$tool_stat" | grep -oE '[0-9]+' || echo 0)
  td=$(grep -oE '[0-9]+ deletion' <<< "$tool_stat" | grep -oE '[0-9]+' || echo 0)
  tooling_loc=$((ti + td))
fi
code_loc=$((loc - tooling_loc))
code_files=$((files - tooling_files))

qg_set_output files "$files"
qg_set_output loc "$loc"
qg_set_output code_loc "$code_loc"
qg_set_output tooling_loc "$tooling_loc"

# Blast radius — how many packages in the module import the packages the
# diff touches. Surfaced as informational metric; a 200 LOC change to a
# package imported by 30 others is a wider risk than a 1000 LOC leaf
# change. qg-fan-in is best-effort; failure is non-fatal because the
# tool needs `go list` and a clean go.mod state.
blast_radius=0
if command -v qg-fan-in >/dev/null 2>&1 || [[ -x "$(qg_tool qg-fan-in 2>/dev/null)" ]]; then
  blast_input=$(git diff "$QG_BASE_REF"...HEAD --name-only -- '*.go' ':(exclude)*_test.go')
  if [[ -n "$blast_input" ]]; then
    blast_radius=$(
      printf '%s\n' "$blast_input" \
        | ( cd "$QG_REPO_ROOT" && "$(qg_tool qg-fan-in)" -files - 2>/dev/null ) \
        | awk -F'\t' '$1=="TOTAL" { print $2; exit }'
    )
    [[ -z "$blast_radius" ]] && blast_radius=0
  fi
fi
qg_set_output blast_radius "$blast_radius"

bypass_loc=false
bypass_files=false
qg_has_label "$loc_label" && bypass_loc=true
qg_has_label "$files_label" && bypass_files=true

reasons=()
if (( loc > loc_block )) && ! $bypass_loc; then
  reasons+=("LOC ${loc} > ${loc_block} block (bypass with label '${loc_label}')")
fi
if (( files > files_block )) && ! $bypass_files; then
  reasons+=("files ${files} > ${files_block} block (bypass with label '${files_label}')")
fi

summary="**Scope**: ${files} files (${code_files} code + ${tooling_files} tooling), ${loc} LOC (${code_loc} code + ${tooling_loc} tooling), blast radius ${blast_radius}"

if [[ ${#reasons[@]} -gt 0 ]]; then
  qg_set_output passed false
  msg=$(printf 'PR exceeds scope budget: %s' "$(IFS='; '; echo "${reasons[*]}")")
  qg_set_summary_line "❌ ${summary}"
  for r in "${reasons[@]}"; do qg_set_summary_line "   - ${r}"; done
  if [[ "$enforcement" == "blocking" ]]; then
    qg_fail "$msg"
    exit 1
  fi
  qg_warn "$msg"
  exit 0
fi

# Warn-only path
warns=()
if (( loc > loc_warn )) && ! $bypass_loc; then
  warns+=("LOC ${loc} > ${loc_warn} warn (consider splitting)")
fi
if (( files > files_warn )) && ! $bypass_files; then
  warns+=("files ${files} > ${files_warn} warn")
fi

qg_set_output passed true
if [[ ${#warns[@]} -gt 0 ]]; then
  qg_set_summary_line "⚠️  ${summary}"
  for w in "${warns[@]}"; do
    qg_set_summary_line "   - ${w}"
    qg_warn "$w"
  done
else
  qg_set_summary_line "✅ ${summary}"
fi
