#!/usr/bin/env bash
# scripts/qg/commit-lint.sh — per-commit hygiene checks.
#
# Verifies each commit between $QG_BASE_REF and HEAD:
#   1. No `Co-Authored-By:` trailer (project policy: feedback_no_coauthor.md).
#   2. No nested parens in commit body (release-please breakage:
#      feedback_no_code_in_commit_body.md). Backtick'd snippets are stripped
#      before the check, so `fmt.Sprintf(...)` inside backticks is fine.
#   3. The PR's first commit subject follows Conventional Commits with one
#      of the allowed types from .github/quality-gate.yml.
#
# Exits 1 on any violation. Each violation is printed with the offending SHA.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement commit_lint)
forbid_co_author=$(qg_yq '.commit_lint.forbid_co_authored_by // true')
forbid_nested_parens=$(qg_yq '.commit_lint.forbid_nested_parens_in_body // true')
require_cc=$(qg_yq '.commit_lint.require_conventional_commits // true')
mapfile -t allowed_types < <(qg_yq '.commit_lint.allowed_types[]?' 2>/dev/null || true)

if [[ ${#allowed_types[@]} -eq 0 ]]; then
  allowed_types=(feat fix refactor perf docs test build ci chore revert style)
fi

types_re=$(IFS='|'; echo "${allowed_types[*]}")
cc_re="^(${types_re})(\([a-z0-9._/-]+\))?!?: .+"

mapfile -t shas < <(qg_pr_commits)

if [[ ${#shas[@]} -eq 0 ]]; then
  qg_log "no commits to lint (PR is empty?)"
  qg_set_output passed true
  qg_set_summary_line "✅ **Commit lint**: no commits"
  exit 0
fi

fails=()

for sha in "${shas[@]}"; do
  msg=$(git log -1 --format='%B' "$sha")
  subject=$(git log -1 --format='%s' "$sha")
  body=$(git log -1 --format='%b' "$sha")

  if [[ "$forbid_co_author" == "true" ]]; then
    if grep -qiE '^Co-Authored-By:' <<< "$msg"; then
      fails+=("${sha:0:8}: Co-Authored-By trailer is forbidden")
    fi
  fi

  if [[ "$forbid_nested_parens" == "true" ]]; then
    body_no_code=$(printf '%s' "$body" | sed -E 's/`[^`]*`//g')
    if grep -qE '\([^)]*\([^)]*\)' <<< "$body_no_code"; then
      fails+=("${sha:0:8}: nested parens in body break release-please — use backticks for code")
    fi
  fi

  if [[ "$require_cc" == "true" ]]; then
    if ! [[ "$subject" =~ $cc_re ]]; then
      fails+=("${sha:0:8}: subject does not match Conventional Commits — '${subject}'")
    fi
  fi
done

if [[ ${#fails[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_summary_line "✅ **Commit lint**: ${#shas[@]} commit(s) clean"
  exit 0
fi

qg_set_output passed false
{
  echo "## Commit lint failed"
  echo
  echo '```'
  printf '%s\n' "${fails[@]}"
  echo '```'
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"

if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "commit lint: ${#fails[@]} issue(s) — see step summary"
  exit 1
fi
qg_warn "commit lint: ${#fails[@]} issue(s)"
