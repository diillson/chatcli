/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package main

import (
	"strings"
	"testing"
)

// resetEnv clears every QG-shaped env var so a test starts from a
// blank slate. Listed explicitly (not loops over os.Environ) because
// the set is small and explicit is auditable.
func resetEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"R_BUILD", "R_COV", "R_PATCH", "R_SMELLS", "R_SCOPE", "R_E2E", "R_LINT",
		"R_CYCLO", "R_SEC", "R_I18N", "R_CRD", "R_LICENSE", "R_APIDIFF",
		"R_BINSIZE", "R_PROVPARITY",
		"COV_CURRENT", "COV_BASELINE", "COV_DELTA",
		"PATCH_PCT", "PATCH_THR",
		"SCOPE_FILES", "SCOPE_LOC", "SCOPE_CODE_LOC", "SCOPE_TOOL_LOC",
		"CYCLO_CHECKED", "I18N_MISSING", "I18N_UNKNOWN", "CRD_DRIFTED",
		"LICENSE_MISSING", "API_INCOMPAT", "BIN_CHATCLI", "BIN_OPERATOR",
		"PROV_COUNT", "PROV_VIOLATIONS",
		"SKIP", "RELEASE_PLEASE", "DOCS_ONLY",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadFloors_AllSuccess(t *testing.T) {
	resetEnv(t)
	t.Setenv("R_BUILD", "success")
	t.Setenv("R_COV", "success")
	t.Setenv("R_PATCH", "success")
	t.Setenv("R_SMELLS", "success")
	t.Setenv("R_SCOPE", "success")
	t.Setenv("R_E2E", "success")
	t.Setenv("R_LINT", "success")
	t.Setenv("R_CYCLO", "success")
	t.Setenv("R_SEC", "success")
	t.Setenv("R_I18N", "success")
	t.Setenv("R_CRD", "success")
	t.Setenv("R_LICENSE", "success")
	t.Setenv("R_APIDIFF", "success")
	t.Setenv("R_BINSIZE", "success")
	t.Setenv("R_PROVPARITY", "success")

	floors, v := loadFloors(false)
	if v != verdictPass {
		t.Errorf("verdict = %s, want pass", v)
	}
	if len(floors) != 15 {
		t.Errorf("expected 15 floors, got %d", len(floors))
	}
}

func TestLoadFloors_OneFailureDrivesFail(t *testing.T) {
	resetEnv(t)
	t.Setenv("R_BUILD", "success")
	t.Setenv("R_PATCH", "failure")
	t.Setenv("R_COV", "success")

	_, v := loadFloors(false)
	if v != verdictFail {
		t.Errorf("verdict = %s, want fail", v)
	}
}

func TestLoadFloors_SkippedDoesNotFail(t *testing.T) {
	// docs_only PRs skip Floors 3/4/6/8/12/13/14 — they should pass even
	// when their statuses come back empty.
	resetEnv(t)
	t.Setenv("DOCS_ONLY", "true")
	t.Setenv("R_BUILD", "success")
	t.Setenv("R_COV", "success")
	t.Setenv("R_SCOPE", "success")
	t.Setenv("R_LINT", "success")
	t.Setenv("R_SEC", "success")
	t.Setenv("R_I18N", "success")
	t.Setenv("R_CRD", "success")
	t.Setenv("R_PROVPARITY", "success")

	_, v := loadFloors(false)
	if v != verdictPass {
		t.Errorf("docs-only with empty skipped floors should pass, got %s", v)
	}
}

func TestLoadFloors_PatchCoverageNearThresholdWarns(t *testing.T) {
	resetEnv(t)
	t.Setenv("R_PATCH", "success")
	t.Setenv("PATCH_PCT", "62")
	t.Setenv("PATCH_THR", "60")

	floors, _ := loadFloors(false)
	patch := floors[2] // Floor 3 is index 2
	if !patch.Warn {
		t.Errorf("patch coverage 62%% just above 60%% threshold should warn")
	}
}

func TestLoadFloors_ScopeLargeWarns(t *testing.T) {
	resetEnv(t)
	t.Setenv("R_SCOPE", "success")
	t.Setenv("SCOPE_LOC", "700")

	floors, _ := loadFloors(false)
	scope := floors[4] // Floor 5 is index 4
	if !scope.Warn {
		t.Errorf("scope 700 LOC near 800 warn threshold should warn")
	}
}

func TestRenderBody_TablePresent(t *testing.T) {
	resetEnv(t)
	t.Setenv("R_BUILD", "success")
	t.Setenv("COV_CURRENT", "33.7")
	t.Setenv("COV_BASELINE", "33.3")

	floors, v := loadFloors(false)
	body := renderBody(floors, v, false)

	for _, want := range []string{
		"### Quality Gate",
		"| Floor | Status | Result | Δ vs main | Budget |",
		"1 · Build & Static",
		"2 · Coverage",
		"33.7% (baseline 33.3%)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestRenderBody_BypassedShortCircuits(t *testing.T) {
	resetEnv(t)
	got := bypassedBody()
	if !strings.Contains(got, "Bypassed") {
		t.Errorf("bypassed body missing keyword")
	}
}

func TestHelpers(t *testing.T) {
	if dash("") != "—" || dash("x") != "x" {
		t.Errorf("dash failed")
	}
	if budgetThr("60", "%") != "≥ 60%" {
		t.Errorf("budgetThr failed")
	}
	if human("104857600") != "100.0MB" {
		t.Errorf("human failed: %q", human("104857600"))
	}
	if patchResult("75", "60") != "75% (req ≥ 60%)" {
		t.Errorf("patchResult failed")
	}
	if coverageResult("33.7", "") != "33.7% (bootstrap)" {
		t.Errorf("coverageResult bootstrap failed")
	}
	if coverageResult("33.7", "33.0") != "33.7% (baseline 33.0%)" {
		t.Errorf("coverageResult baseline failed")
	}
	if licenseResult("0") != "0 missing" || licenseResult("3") != "3 missing" {
		t.Errorf("licenseResult failed")
	}
}

func TestIcon(t *testing.T) {
	if icon(statusSuccess) != "✅" {
		t.Errorf("success icon")
	}
	if icon(statusSkipped) != "⏭️" {
		t.Errorf("skipped icon")
	}
	if icon("") != "⏭️" {
		t.Errorf("empty icon")
	}
	if icon("failure") != "❌" {
		t.Errorf("failure icon")
	}
}
