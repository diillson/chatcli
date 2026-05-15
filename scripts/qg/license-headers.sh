#!/usr/bin/env bash
# scripts/qg/license-headers.sh — Floor 12, license header presence.
#
# Enforces that NEW Go source files added in the diff carry the project's
# copyright/license header. Existing files are grandfathered: 66 percent of
# the tree predates this gate and a backfill is its own refactor PR. The
# bar for new code is "carry a header", not "match an exact template",
# because the format has drifted over time and exact-match would create
# noise without security/legal value.
#
# Exempt paths: tests, generated code, tools/docgen, proto.
#
# Exit codes: 0 pass, 1 fail. Sets outputs: passed, missing_count.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement license_headers)

mapfile -t added < <(
  git diff "$QG_BASE_REF"...HEAD --name-only --diff-filter=A \
    -- '*.go' \
       ':(exclude)*_test.go' \
       ':(exclude)*.pb.go' \
       ':(exclude)*_generated.go' \
       ':(exclude)zz_generated*.go' \
       ':(exclude)proto/**' \
       ':(exclude)tools/docgen/**'
)

if [[ ${#added[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_output missing_count 0
  qg_set_summary_line "✅ **License headers**: no new Go files"
  exit 0
fi

missing=()
for f in "${added[@]}"; do
  [[ -f "$f" ]] || continue
  # Check the first 12 lines for both "Copyright" and "License" — the
  # canonical header has them on adjacent lines, but a few alternative
  # forms in the repo place them differently. Both must appear.
  head_block=$(head -n 12 "$f")
  if ! grep -q "Copyright" <<< "$head_block" || ! grep -q "License" <<< "$head_block"; then
    missing+=("$f")
  fi
done

qg_set_output missing_count "${#missing[@]}"

if [[ ${#missing[@]} -eq 0 ]]; then
  qg_set_output passed true
  qg_set_summary_line "✅ **License headers**: ${#added[@]} new file(s) clean"
  exit 0
fi

qg_set_output passed false
{
  echo "## License headers missing"
  echo
  echo "New Go files must carry a header containing **Copyright** and **License**"
  echo "(see existing files for the template — e.g. \`cli/cli.go\`)."
  echo
  echo '```'
  printf '%s\n' "${missing[@]}"
  echo '```'
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
qg_set_summary_line ""

msg="${#missing[@]} new Go file(s) missing license header"
if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "$msg"
  exit 1
fi
qg_warn "$msg"
