#!/usr/bin/env bash
# scripts/qg/lib.sh — shared helpers for the Quality Gate scripts.
#
# Sourced (not executed) by the other qg/*.sh scripts. Exposes:
#   qg_load_config        read .github/quality-gate.yml into env vars
#   qg_diff_added_lines   stream "+" lines from PR diff (Go-aware filters)
#   qg_pr_labels          space-joined PR labels (works in GitHub Actions)
#   qg_log_kv             structured key=value logging for the Actions UI
#   qg_set_output         write to $GITHUB_OUTPUT defensively
#   qg_set_summary_line   append a single line to $GITHUB_STEP_SUMMARY
#   qg_fail / qg_warn     emit ::error / ::warning workflow commands
#   qg_enforcement        read enforcement (blocking|warn) for a given floor
#
# Designed for `set -euo pipefail`; callers must source this BEFORE setting
# their own pipefail flags or after — both work, the lib does not change
# global shell options.

# shellcheck shell=bash

# Bash 4+ required (mapfile, associative arrays, ${var^^}). GitHub Actions
# runners ship bash 5; macOS users running scripts locally need to
# `brew install bash` and call them via /opt/homebrew/bin/bash.
if ((BASH_VERSINFO[0] < 4)); then
  printf '[qg] error: bash >= 4 required (current: %s). On macOS: brew install bash\n' "$BASH_VERSION" >&2
  exit 64
fi

QG_REPO_ROOT="${QG_REPO_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
QG_CONFIG_FILE="${QG_CONFIG_FILE:-$QG_REPO_ROOT/.github/quality-gate.yml}"
QG_BASE_REF="${QG_BASE_REF:-${BASE_REF:-origin/main}}"

qg_log() {
  printf '[qg] %s\n' "$*" >&2
}

qg_log_kv() {
  printf '[qg] %s=%s\n' "$1" "$2" >&2
}

qg_warn() {
  printf '::warning::%s\n' "$*"
}

qg_fail() {
  printf '::error::%s\n' "$*"
}

qg_set_output() {
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"
  fi
}

qg_set_summary_line() {
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    printf '%s\n' "$*" >> "$GITHUB_STEP_SUMMARY"
  fi
}

# qg_enforcement <floor_key> -> echoes "blocking" or "warn"
qg_enforcement() {
  local key="$1"
  yq -r ".${key}.enforcement // \"blocking\"" "$QG_CONFIG_FILE" 2>/dev/null || echo "blocking"
}

# qg_yq <yq-expression> -> reads from quality-gate.yml
qg_yq() {
  yq -r "$1" "$QG_CONFIG_FILE"
}

# qg_pr_labels -> echoes a single space-separated string (with leading/trailing
# spaces) so callers can grep with " label-name " safely. Pulls from GitHub
# event payload when running in Actions, falls back to PR_LABELS env.
qg_pr_labels() {
  if [[ -n "${PR_LABELS:-}" ]]; then
    printf ' %s ' "$PR_LABELS"
    return 0
  fi
  if [[ -n "${GITHUB_EVENT_PATH:-}" && -r "${GITHUB_EVENT_PATH:-}" ]]; then
    local raw
    raw=$(jq -r '.pull_request.labels[]?.name // empty' "$GITHUB_EVENT_PATH" 2>/dev/null | tr '\n' ' ')
    printf ' %s ' "$raw"
    return 0
  fi
  printf ' '
}

qg_has_label() {
  local label="$1"
  [[ "$(qg_pr_labels)" == *" $label "* ]]
}

# qg_diff_added_lines [pathspec...]
# Streams "+" lines (without the leading +) from the PR diff. Pathspecs are
# passed straight to `git diff --` so callers can scope to .go files etc.
qg_diff_added_lines() {
  git diff "$QG_BASE_REF"...HEAD --no-color --unified=0 -- "$@" \
    | awk '
        /^diff --git / { in_hunk=0; next }
        /^@@ / { in_hunk=1; next }
        in_hunk && /^\+\+\+/ { next }
        in_hunk && /^\+/ {
          # strip the leading + and emit
          sub(/^\+/, "")
          print
        }
      '
}

# qg_diff_changed_files [pathspec...]
# Lists files changed in the PR (added or modified, not deleted), one per line.
qg_diff_changed_files() {
  git diff "$QG_BASE_REF"...HEAD --no-color --name-only --diff-filter=AM -- "$@"
}

# qg_diff_deleted_files [pathspec...]
qg_diff_deleted_files() {
  git diff "$QG_BASE_REF"...HEAD --no-color --name-only --diff-filter=D -- "$@"
}

# qg_pr_commits -> SHAs of commits in the PR (exclusive of base)
qg_pr_commits() {
  git log --no-merges --format='%H' "$QG_BASE_REF"..HEAD
}

# qg_is_docs_only -> exit 0 if the PR touches no .go files (docs/CI only)
qg_is_docs_only() {
  local go_files
  go_files=$(qg_diff_changed_files '*.go' || true)
  [[ -z "$go_files" ]]
}

# qg_require_yq / qg_require_jq -> install on demand (Actions images have apt)
qg_require_yq() {
  command -v yq >/dev/null 2>&1 && return 0
  if command -v sudo >/dev/null 2>&1; then
    sudo wget -qO /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64
    sudo chmod +x /usr/local/bin/yq
  else
    qg_fail "yq not installed and cannot install (no sudo)"
    return 1
  fi
}

qg_require_jq() {
  command -v jq >/dev/null 2>&1 && return 0
  qg_fail "jq is required but not installed"
  return 1
}

# qg_tool <name> — echoes the absolute path of an in-repo qg helper binary.
#
# Resolution order:
#   1. $PATH (the composite .github/actions/setup-qg-tools pre-builds the
#      binaries into $HOME/.qg-bin and prepends it to GITHUB_PATH).
#   2. A cached build under $QG_REPO_ROOT/.qg-cache, rebuilt only when
#      the binary is missing.
#
# Each wrapper script that needs a Go helper calls this once and relies
# on the path it returns. Avoids the "every floor pays for go build"
# tax on every CI run.
qg_tool() {
  local name="$1"
  if command -v "$name" >/dev/null 2>&1; then
    command -v "$name"
    return 0
  fi
  local cache_dir="${QG_REPO_ROOT}/.qg-cache"
  mkdir -p "$cache_dir"
  local out="$cache_dir/$name"
  if [[ ! -x "$out" ]]; then
    ( cd "$QG_REPO_ROOT" && go build -o "$out" "./tools/qg/cmd/$name" ) >&2
  fi
  echo "$out"
}
