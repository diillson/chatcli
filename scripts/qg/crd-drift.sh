#!/usr/bin/env bash
# scripts/qg/crd-drift.sh — Floor 11, generated CRD vs checked-in YAML.
#
# Runs `controller-gen` against operator/api/v1alpha1 and compares the
# result with the YAML committed to operator/config/crd/bases/. The CRD
# is editable kubebuilder source plus a generated artefact: forgetting to
# regenerate after editing the Go types ships a stale CRD that diverges
# from runtime expectations.
#
# Skips entirely when the PR does not touch operator/api/ — re-generating
# CRDs costs ~5 seconds and is pure noise on PRs that don't risk drift.
#
# Exit codes: 0 pass, 1 fail. Sets outputs: passed, drifted_files.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

qg_require_yq

enforcement=$(qg_enforcement crd_drift)

# Skip if nothing under operator/api/ changed — that's the only input to
# the CRD generator.
changed_api=$(git diff "$QG_BASE_REF"...HEAD --name-only -- 'operator/api/**' | wc -l | tr -d ' ')
changed_crd=$(git diff "$QG_BASE_REF"...HEAD --name-only -- 'operator/config/crd/**' | wc -l | tr -d ' ')

if [[ "$changed_api" == "0" && "$changed_crd" == "0" ]]; then
  qg_set_output passed true
  qg_set_output drifted_files 0
  qg_set_summary_line "✅ **CRD drift**: N/A (no operator/api or operator/config/crd changes)"
  exit 0
fi

# Install controller-gen on demand. Pin the version that matches what the
# operator's Makefile uses so locally-run drift checks agree with CI.
CONTROLLER_GEN_VERSION="v0.16.5"
if ! command -v controller-gen >/dev/null 2>&1; then
  GOBIN="$(go env GOPATH)/bin"
  go install "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}"
  export PATH="$GOBIN:$PATH"
fi

# Re-generate into the checked-in path. controller-gen overwrites files
# in place so a `git diff --exit-code` afterwards is the verdict.
#
# `crd:allowDangerousTypes=true` is required because operator/api/v1alpha1
# carries a float field (slo_types.go's burn-rate budget); without the
# flag controller-gen refuses to emit and the gate fails for the wrong
# reason. The operator team owns whether to keep the float — until that
# is resolved, allow it through so the gate measures REAL drift.
( cd "$QG_REPO_ROOT/operator" && \
    controller-gen \
      'crd:allowDangerousTypes=true' \
      paths=./api/... \
      output:crd:dir=config/crd/bases ) >/dev/null

if git -C "$QG_REPO_ROOT" diff --quiet -- 'operator/config/crd/bases/'; then
  qg_set_output passed true
  qg_set_output drifted_files 0
  qg_set_summary_line "✅ **CRD drift**: regenerated YAML matches commit"
  exit 0
fi

drifted=$(git -C "$QG_REPO_ROOT" diff --name-only -- 'operator/config/crd/bases/' | tr '\n' ' ')
qg_set_output passed false
qg_set_output drifted_files "${drifted% }"
{
  echo "## CRD drift detected"
  echo
  echo "Running \`controller-gen crd paths=./api/...\` inside \`operator/\` produced"
  echo "a different YAML than the one checked in. Run the same command locally and"
  echo "commit the result so the CRD reflects the Go types in this PR."
  echo
  echo "Drifted files:"
  echo '```'
  echo "${drifted% }"
  echo '```'
  echo
  echo "<details><summary>Diff</summary>"
  echo
  echo '```diff'
  # awk reads the whole stream and prints the first 120 lines without
  # closing stdin early; using `head` here triggers SIGPIPE on git diff
  # under `set -o pipefail` and kills the script before the summary is
  # written (exit 141, no diagnostics in the workflow log).
  git -C "$QG_REPO_ROOT" diff -- 'operator/config/crd/bases/' | awk 'NR<=120'
  echo '```'
  echo
  echo "</details>"
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"

# Restore the working tree so subsequent gates don't see the regenerated
# files — they'd otherwise show up in scope-budget LOC and confuse Floor 8.
git -C "$QG_REPO_ROOT" checkout -- 'operator/config/crd/bases/' >/dev/null 2>&1 || true

msg="CRD drift: ${drifted% }"
if [[ "$enforcement" == "blocking" ]]; then
  qg_fail "$msg"
  exit 1
fi
qg_warn "$msg"
