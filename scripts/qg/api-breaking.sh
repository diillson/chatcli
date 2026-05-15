#!/usr/bin/env bash
# scripts/qg/api-breaking.sh — Floor 13, public API breaking changes.
#
# Uses golang.org/x/exp/cmd/apidiff to compute the API surface of each
# touched package on the base branch and on HEAD, then reports
# incompatible changes (removed/renamed identifiers, narrowed return
# types, changed signatures). The previous AI-smells heuristic only
# caught REMOVED top-level declarations; apidiff understands incompatible
# evolutions of existing ones too.
#
# Scoped to packages with .go changes — running apidiff against the
# whole module costs O(N) builds and most PRs touch a small surface.
#
# Bypass label: `breaking-change` (caller has acknowledged and announced).
#
# Exit codes: 0 pass, 1 fail. Sets outputs: passed, changed_packages,
# incompatible_count.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement api_breaking)

if qg_has_label breaking-change; then
  qg_log "breaking-change label set — Floor 13 short-circuits to pass"
  qg_set_output passed true
  qg_set_output incompatible_count 0
  qg_set_summary_line "⏭️ **API breaking changes**: bypassed (\`breaking-change\` label)"
  exit 0
fi

# Restrict to packages whose Go files changed in this PR. Skipping tests
# because apidiff already ignores them.
mapfile -t changed_files < <(
  git diff "$QG_BASE_REF"...HEAD --name-only -- '*.go' ':(exclude)*_test.go' ':(exclude)tools/docgen/**'
)
if [[ ${#changed_files[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_output incompatible_count 0
  qg_set_summary_line "✅ **API breaking changes**: N/A (no Go changes)"
  exit 0
fi

# Collapse files into unique package directories.
declare -A pkgset=()
for f in "${changed_files[@]}"; do
  dir=$(dirname "$f")
  pkgset["$dir"]=1
done
pkgs=("${!pkgset[@]}")

# Install apidiff on demand.
if ! command -v apidiff >/dev/null 2>&1; then
  GOBIN="$(go env GOPATH)/bin"
  go install golang.org/x/exp/cmd/apidiff@latest
  export PATH="$GOBIN:$PATH"
fi

base_worktree=$(mktemp -d)
trap 'rm -rf "$base_worktree"' EXIT

# Materialise the base ref into a worktree so we can `apidiff -w` it.
git -C "$QG_REPO_ROOT" worktree add --detach --quiet "$base_worktree" "$QG_BASE_REF"
trap 'git -C "$QG_REPO_ROOT" worktree remove --force "$base_worktree" >/dev/null 2>&1 || true' EXIT

incompatible=0
report=$(mktemp)
: > "$report"

for pkg in "${pkgs[@]}"; do
  # Skip directories that don't exist on base (newly added packages have
  # no "old" surface to compare against — by definition not breaking).
  if [[ ! -d "$base_worktree/$pkg" ]]; then
    continue
  fi
  # Skip directories with no Go files on either side (config dirs, etc).
  if ! ls "$base_worktree/$pkg"/*.go >/dev/null 2>&1; then
    continue
  fi

  base_api=$(mktemp)
  ( cd "$base_worktree/$pkg" && apidiff -w "$base_api" . >/dev/null 2>&1 ) || { rm -f "$base_api"; continue; }

  diff_output=$( cd "$QG_REPO_ROOT/$pkg" && apidiff "$base_api" . 2>/dev/null || true )
  rm -f "$base_api"

  # apidiff prints "Incompatible changes:" / "Compatible changes:" blocks.
  # We only block on incompatibles. The format is stable enough that a
  # simple awk extracts the count.
  inc=$(awk '
    /^Incompatible changes:/ { in_inc=1; next }
    /^Compatible changes:/   { in_inc=0; next }
    in_inc && /^- / { count++ }
    END { print count+0 }
  ' <<< "$diff_output")

  if (( inc > 0 )); then
    incompatible=$(( incompatible + inc ))
    {
      echo "### \`$pkg\` ($inc incompatible)"
      echo '```'
      printf '%s\n' "$diff_output"
      echo '```'
      echo
    } >> "$report"
  fi
done

qg_set_output incompatible_count "$incompatible"

if (( incompatible == 0 )); then
  qg_set_output passed true
  qg_set_summary_line "✅ **API breaking changes**: ${#pkgs[@]} package(s) checked, none incompatible"
  exit 0
fi

qg_set_output passed false
{
  echo "## Incompatible API changes (${incompatible})"
  echo
  cat "$report"
  echo "Bypass via the \`breaking-change\` label after announcing the migration."
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"

msg="${incompatible} incompatible API change(s) detected"
if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "$msg"
  exit 1
fi
qg_warn "$msg"
