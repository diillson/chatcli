#!/usr/bin/env bash
# scripts/qg/ai-smells.sh — anti-pattern scanner for the PR diff.
#
# Each check runs against added lines (the "+" side of the diff) on Go
# non-test files unless noted. Failures collect into a single report so the
# author sees every issue at once instead of fixing-pushing-fixing.
#
# Checks: TODO/FIXME/XXX, panic() in non-test, interface{}/any in new
# exported APIs, hardcoded user-facing strings under cli/, obvious secrets,
# new go.mod imports without `deps:approved`, removed exports without
# `breaking-change`.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement ai_smells)

# Exclude pathspecs from config (default safety net + repo-specific tooling).
mapfile -t exclude_specs < <(qg_yq '.ai_smells.exclude[]?' 2>/dev/null || true)
exclude_args=()
for p in "${exclude_specs[@]}"; do
  exclude_args+=( ":(exclude)$p" )
done

# Collect Go non-test diff once into a tempfile for repeated grep passes.
diff_tmp=$(mktemp)
trap 'rm -f "$diff_tmp"' EXIT

git diff "$QG_BASE_REF"...HEAD --no-color --unified=0 \
  -- '*.go' "${exclude_args[@]}" > "$diff_tmp" || true

# Stream of "filename:line:added-content" for grep targets that need location.
located_added() {
  awk '
    /^diff --git a\/.* b\/(.*)$/ {
      match($0, /b\/[^ ]+/)
      file = substr($0, RSTART+2, RLENGTH-2)
      next
    }
    /^@@ / {
      match($0, /\+[0-9]+(,[0-9]+)?/)
      hunk = substr($0, RSTART+1, RLENGTH-1)
      split(hunk, a, ",")
      lineno = a[1] + 0
      next
    }
    /^\+\+\+/ { next }
    /^\+/ {
      content = substr($0, 2)
      printf "%s:%d:%s\n", file, lineno, content
      lineno++
      next
    }
    /^-/ { next }
    /^[^+-]/ { lineno++ }
  ' "$diff_tmp"
}

fails=()

# --- Check: TODO / FIXME / XXX -----------------------------------------------
if [[ "$(qg_yq '.ai_smells.checks.todo_fixme_xxx // true')" == "true" ]]; then
  hits=$(located_added | grep -E ':(.*)\b(TODO|FIXME|XXX)\b' || true)
  if [[ -n "$hits" ]]; then
    fails+=("TODO/FIXME/XXX in new code:"$'\n'"$hits")
  fi
fi

# --- Check: panic() in non-test ---------------------------------------------
if [[ "$(qg_yq '.ai_smells.checks.panic_in_new_code // true')" == "true" ]]; then
  hits=$(located_added | grep -E ':[^:]*\bpanic\(' || true)
  if [[ -n "$hits" ]]; then
    fails+=("panic() in new non-test code (return error instead):"$'\n'"$hits")
  fi
fi

# --- Check: interface{} / any in NEW public APIs ----------------------------
if [[ "$(qg_yq '.ai_smells.checks.interface_any_in_public_api // true')" == "true" ]]; then
  # Match exported func/method signatures or exported type aliases that use
  # interface{} or " any ". Heuristic — must be on a single line.
  hits=$(located_added | grep -E ':[^:]*(func|type)\s+\(?[A-Z][A-Za-z0-9_]*\)?[^:]*(\binterface\{\}|\bany\b)' || true)
  if [[ -n "$hits" ]]; then
    fails+=("interface{}/any in new exported API (prefer concrete types or generics):"$'\n'"$hits")
  fi
fi

# --- Check: hardcoded user-facing strings in cli/ ---------------------------
# Heuristic: fmt.{Print,Printf,Println,Errorf,Sprintf}("Foo …") or
# logger user-facing where the literal starts with an uppercase letter and
# is NOT wrapped in i18n.T(...). Scoped to cli/ to avoid false positives in
# infra packages.
if [[ "$(qg_yq '.ai_smells.checks.hardcoded_user_strings // true')" == "true" ]]; then
  hits=$(located_added \
    | grep -E '^cli/' \
    | grep -E 'fmt\.(Println|Printf|Print|Errorf|Sprintf)\(\s*"[A-ZÁÉÍÓÚÂÊÔÃÕÇ]' \
    | grep -vE '\bi18n\.T\(' \
    || true)
  if [[ -n "$hits" ]]; then
    fails+=("user-facing string under cli/ not wrapped in i18n.T(...):"$'\n'"$hits")
  fi
fi

# --- Check: obvious secrets -------------------------------------------------
# Heuristic only — gitleaks runs as a separate floor for the deep scan.
# This catches the most common AI-generated mistake: a literal key string
# pasted into a Go assignment.
if [[ "$(qg_yq '.ai_smells.checks.obvious_secrets // true')" == "true" ]]; then
  hits=$(located_added \
    | grep -Ei '(api[_-]?key|secret|password|token|bearer)\s*[:=]\s*"[A-Za-z0-9_+/=-]{20,}"' \
    || true)
  if [[ -n "$hits" ]]; then
    fails+=("possible hardcoded secret (move to env / config provider):"$'\n'"$hits")
  fi
fi

# --- Check: new go.mod dependency without deps:approved ---------------------
if [[ "$(qg_yq '.ai_smells.checks.new_dependency_without_label // true')" == "true" ]]; then
  added_modlines=$(git diff "$QG_BASE_REF"...HEAD -- go.mod 2>/dev/null \
    | awk '/^\+[[:space:]]+[a-z0-9._-]+\.[a-z0-9._\/-]+\s+v[0-9]/ { print }' \
    || true)
  if [[ -n "$added_modlines" ]]; then
    if ! qg_has_label deps:approved; then
      fails+=("new go.mod dependency without 'deps:approved' label:"$'\n'"$added_modlines")
    else
      qg_log "new dependencies present but bypass label 'deps:approved' is set"
    fi
  fi
fi

# --- Check: removed exports without breaking-change -------------------------
if [[ "$(qg_yq '.ai_smells.checks.removed_exports_without_label // true')" == "true" ]]; then
  removed_lines=$(git diff "$QG_BASE_REF"...HEAD -- '*.go' \
                    ':(exclude)*_test.go' \
                    ':(exclude)proto/**' 2>/dev/null \
                  | awk '
                      /^diff --git a\/.* b\/(.*)$/ { match($0, /b\/[^ ]+/); file = substr($0, RSTART+2, RLENGTH-2); next }
                      /^-(func|type|const|var)\s+\(?[A-Z]/ { printf "%s: %s\n", file, $0 }
                    ' \
                  || true)
  if [[ -n "$removed_lines" ]]; then
    if ! qg_has_label breaking-change; then
      fails+=("removed exported identifier(s) without 'breaking-change' label:"$'\n'"$removed_lines")
    else
      qg_log "removed exports present but bypass label 'breaking-change' is set"
    fi
  fi
fi

if [[ ${#fails[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_summary_line "✅ **AI smells**: clean"
  exit 0
fi

# Render a single grouped error
qg_set_output passed false
{
  echo "## AI smells detected"
  echo
  for f in "${fails[@]}"; do
    echo "### ${f%%$'\n'*}"
    echo
    echo '```'
    printf '%s\n' "${f#*$'\n'}"
    echo '```'
    echo
  done
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

# Workflow command — keep it short; full report is in the step summary.
qg_fail "AI smells: ${#fails[@]} category(ies) failed — see step summary"

if [[ "$enforcement" == "blocking" ]]; then
  exit 1
fi
exit 0
