#!/usr/bin/env bash
# scripts/qg/i18n-parity.sh — Floor 10, i18n cross-locale and usage parity.
#
# Catches the failure mode where a PR adds an i18n key to one locale file
# (en.json) but forgets the others (en-US.json, pt-BR.json). The
# resulting runtime behaviour is silent: the missing-locale users see the
# raw key string, the gate stays green, no test fails because nothing
# imports the missing key explicitly.
#
# Runs against the WHOLE repo (not just the diff) — locale parity is a
# global invariant: a key added before this gate existed can still
# diverge silently. The check is fast enough (~hundreds of ms) that we
# don't gate by diff.
#
# Exit codes: 0 pass, 1 fail. Sets outputs: passed, locales, usages,
# missing_keys, unknown_usages.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement i18n_parity)

binary=$(qg_tool qg-i18n-parity)

set +e
output=$( "$binary" \
  -locales-dir "$QG_REPO_ROOT/i18n/locales" \
  -source-root "$QG_REPO_ROOT" \
  -markdown "/tmp/qg-i18n-parity.md" \
  -exclude vendor \
  -exclude tools/docgen \
  -exclude proto \
  -exclude node_modules \
  -exclude .git \
  2>&1 )
rc=$?
set -e

# Forward outputs.
while IFS='=' read -r k v; do
  case "$k" in
    locales|usages|missing_keys|unknown_usages)
      qg_set_output "$k" "$v"
      ;;
  esac
done <<< "$output"

if [[ $rc -eq 0 ]]; then
  qg_set_output passed true
  locales=$(awk -F= '/^locales=/ { print $2; exit }' <<< "$output")
  usages=$(awk -F= '/^usages=/ { print $2; exit }' <<< "$output")
  qg_set_summary_line "✅ **i18n parity**: ${locales} locale(s), ${usages} call site(s) clean"
  exit 0
fi

qg_set_output passed false
qg_set_summary_line "❌ **i18n parity**: see step summary"
qg_set_summary_line ""
if [[ -s /tmp/qg-i18n-parity.md ]]; then
  qg_set_summary_line "$(cat /tmp/qg-i18n-parity.md)"
fi

printf '%s\n' "$output" >&2

if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "i18n parity gate failed (see step summary)"
  exit 1
fi
qg_warn "i18n parity gate failed (warn-only mode)"
