#!/usr/bin/env bash
# scripts/qg/provider-parity.sh — Floor 15, LLM provider integration matrix.
#
# Verifies that every provider declared in llm/catalog/catalog.go has been
# wired through every touch point the project requires (manager factory,
# cost tracker, /config providers section, env redactor, oneshot help,
# i18n locales, operator CRD enum, operator cost tracker). The matrix
# lives in tools/qg/providerparity/providers.go.
#
# Runs against the whole repo state (not just the diff) — adding a
# provider mid-PR but forgetting one touch point in a later PR would
# both compile and pass tests; only this gate notices.
#
# Exit codes: 0 pass, 1 fail. Sets outputs: passed, providers, violations.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement provider_parity)

binary="$(mktemp -d)/qg-provider-parity"
( cd "$QG_REPO_ROOT" && go build -o "$binary" ./tools/qg/cmd/qg-provider-parity )

set +e
output=$( "$binary" \
  -root "$QG_REPO_ROOT" \
  -markdown "/tmp/qg-provider-parity.md" \
  2>&1 )
rc=$?
set -e

while IFS='=' read -r k v; do
  case "$k" in
    providers|touch_points|violations)
      qg_set_output "$k" "$v"
      ;;
  esac
done <<< "$output"

if [[ $rc -eq 0 ]]; then
  qg_set_output passed true
  providers=$(awk -F= '/^providers=/ { print $2; exit }' <<< "$output")
  qg_set_summary_line "✅ **Provider parity**: ${providers} provider(s) matrix complete"
  exit 0
fi

qg_set_output passed false
qg_set_summary_line "❌ **Provider parity**: see step summary"
qg_set_summary_line ""
if [[ -s /tmp/qg-provider-parity.md ]]; then
  qg_set_summary_line "$(cat /tmp/qg-provider-parity.md)"
fi

printf '%s\n' "$output" >&2

if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "provider parity gate failed (see step summary)"
  exit 1
fi
qg_warn "provider parity gate failed (warn-only mode)"
