/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// qg-verdict renders the Quality Gate sticky-comment markdown and emits
// the aggregate verdict. It exists to replace ~150 lines of inline bash
// in .github/workflows/quality-gate.yml so the rendering logic is unit
// testable, the per-floor data is structured, and adding a new floor
// touches one Floor struct entry rather than a brittle multi-line bash
// table.
//
// Inputs: a fixed set of environment variables documented near
// loadFloors. Outputs:
//
//	stdout — `verdict=pass|fail|bypassed` (one line)
//	-body  — path to write the markdown report
//
// Exit code is always 0; the workflow inspects the verdict line and
// decides whether to fail the job.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// status models GitHub Actions job result strings.
//
// The job-cancellation value uses GitHub's double-l spelling because the
// constant is a contract with the workflow runtime — not a free string
// our code chose. The misspell linter prefers the US single-l form;
// nolint silences it locally.
type status string

const (
	statusSuccess status = "success"
	statusSkipped status = "skipped"
	//nolint:misspell // GHA `needs.*.result` returns "cancelled" verbatim.
	statusCancelled status = "cancelled"
)

// icon returns the table cell glyph for a status. Empty/skip/cancel are
// rendered uniformly as ⏭️; failure is ❌; success is ✅.
func icon(s status) string {
	switch s {
	case statusSuccess:
		return "✅"
	case statusSkipped, statusCancelled, "":
		return "⏭️"
	default:
		return "❌"
	}
}

// Floor captures one row of the sticky-comment table.
type Floor struct {
	Num    int
	Name   string
	Status status
	Result string // human-friendly outcome ("81% (1278/1572)", "clean", "0 leaks")
	Delta  string // optional change vs base ("+0.4 pp")
	Budget string // optional budget cell ("≥ 60%", "≤ 30", "100MB")
	Warn   bool   // ⚠️ intermediate state — passing but near-threshold
}

// verdict is the aggregate gate result.
type verdict string

const (
	verdictPass     verdict = "pass"
	verdictFail     verdict = "fail"
	verdictBypassed verdict = "bypassed"
)

func main() {
	body := flag.String("body", "/tmp/qg-verdict.md", "path to write the markdown body")
	flag.Parse()

	skip := getenv("SKIP")
	if skip == "true" {
		writeBody(*body, bypassedBody())
		fmt.Println("verdict=bypassed")
		return
	}

	releasePlease := getenv("RELEASE_PLEASE") == "true"
	floors, v := loadFloors(releasePlease)
	writeBody(*body, renderBody(floors, v, releasePlease))
	fmt.Printf("verdict=%s\n", v)
}

func writeBody(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "qg-verdict: write body:", err)
		os.Exit(1)
	}
}

// loadFloors reads the environment for floor statuses and outputs,
// builds the Floor table, and computes the aggregate verdict. When
// release-please is true, every non-commit-lint floor is force-marked
// as skipped — release PRs ship version bumps only.
func loadFloors(releasePlease bool) ([]Floor, verdict) {
	r := func(name string) status { return status(getenv(name)) }

	covCurrent := getenv("COV_CURRENT")
	covBaseline := getenv("COV_BASELINE")
	covDelta := getenv("COV_DELTA")

	patchPct := getenv("PATCH_PCT")
	patchThr := getenv("PATCH_THR")

	scopeFiles := getenv("SCOPE_FILES")
	scopeLoc := getenv("SCOPE_LOC")
	scopeCodeLoc := getenv("SCOPE_CODE_LOC")
	scopeToolLoc := getenv("SCOPE_TOOL_LOC")

	cycloChecked := getenv("CYCLO_CHECKED")

	i18nMissing := getenv("I18N_MISSING")
	i18nUnknown := getenv("I18N_UNKNOWN")

	crdDrifted := getenv("CRD_DRIFTED")
	licenseMissing := getenv("LICENSE_MISSING")
	apiIncompat := getenv("API_INCOMPAT")
	binChatcli := getenv("BIN_CHATCLI")
	binOperator := getenv("BIN_OPERATOR")
	provCount := getenv("PROV_COUNT")
	provViolations := getenv("PROV_VIOLATIONS")

	docsOnly := getenv("DOCS_ONLY") == "true"

	maybeSkip := func(s status) status {
		if releasePlease {
			return statusSkipped
		}
		return s
	}
	maybeSkipDocs := func(s status) status {
		if docsOnly {
			return statusSkipped
		}
		return maybeSkip(s)
	}

	floors := []Floor{
		{Num: 1, Name: "Build & Static", Status: maybeSkip(r("R_BUILD")), Result: "go build / vet / fmt / lint", Budget: ""},
		{Num: 2, Name: "Coverage", Status: maybeSkip(r("R_COV")), Result: coverageResult(covCurrent, covBaseline), Delta: covDelta, Budget: "≥ baseline"},
		{Num: 3, Name: "Patch coverage", Status: maybeSkipDocs(r("R_PATCH")), Result: patchResult(patchPct, patchThr), Budget: budgetThr(patchThr, "%")},
		{Num: 4, Name: "AI smells", Status: maybeSkipDocs(r("R_SMELLS")), Result: "diff scanned"},
		{Num: 5, Name: "Scope budget", Status: maybeSkip(r("R_SCOPE")), Result: scopeResult(scopeFiles, scopeLoc, scopeCodeLoc, scopeToolLoc), Budget: "warn 800·25"},
		{Num: 6, Name: "E2E", Status: maybeSkipDocs(r("R_E2E")), Result: "go test -race ./e2e/...", Budget: "≤ 15min"},
		{Num: 7, Name: "Commit lint", Status: maybeSkip(r("R_LINT")), Result: "conventional commits"},
		{Num: 8, Name: "Cyclo (new code)", Status: maybeSkipDocs(r("R_CYCLO")), Result: cycloResult(cycloChecked), Budget: "≤ 30"},
		{Num: 9, Name: "Secrets scan", Status: maybeSkip(r("R_SEC")), Result: "gitleaks"},
		{Num: 10, Name: "i18n parity", Status: maybeSkip(r("R_I18N")), Result: i18nResult(i18nMissing, i18nUnknown)},
		{Num: 11, Name: "CRD drift", Status: maybeSkip(r("R_CRD")), Result: crdResult(crdDrifted)},
		{Num: 12, Name: "License headers", Status: maybeSkipDocs(r("R_LICENSE")), Result: licenseResult(licenseMissing)},
		{Num: 13, Name: "API breaking", Status: maybeSkipDocs(r("R_APIDIFF")), Result: apiResult(apiIncompat)},
		{Num: 14, Name: "Binary size", Status: maybeSkipDocs(r("R_BINSIZE")), Result: binSizeResult(binChatcli, binOperator), Budget: "100MB each"},
		{Num: 15, Name: "Provider parity", Status: maybeSkip(r("R_PROVPARITY")), Result: provParityResult(provCount, provViolations)},
	}

	// Mark near-threshold warnings — pass status but Result shows a
	// metric close enough to its budget to be worth surfacing.
	if scopeLoc != "" && parseInt(scopeLoc) >= 600 {
		floors[4].Warn = true
	}
	if patchPct != "" && patchThr != "" {
		p := parseFloat(patchPct)
		t := parseFloat(patchThr)
		if floors[2].Status == statusSuccess && p > 0 && p < t+10 {
			floors[2].Warn = true
		}
	}

	var failed []string
	for _, f := range floors {
		if f.Status != statusSuccess && f.Status != statusSkipped && f.Status != "" {
			failed = append(failed, f.Name)
		}
	}
	if len(failed) > 0 {
		return floors, verdictFail
	}
	return floors, verdictPass
}

// renderBody returns the sticky-comment markdown. The table has four
// columns — Status, Result, Δ vs main, Budget — because that's what the
// reviewer needs to see in two seconds: did it pass, what is the
// number, how is it moving, what is the limit.
func renderBody(floors []Floor, v verdict, releasePlease bool) string {
	var b strings.Builder
	b.WriteString("### Quality Gate\n\n")
	switch v {
	case verdictPass:
		b.WriteString("**Result:** ✅ all floors passed\n")
	case verdictBypassed:
		b.WriteString("**Result:** ⏭️ bypassed\n")
	default:
		b.WriteString("**Result:** ❌ failure\n")
	}
	if releasePlease {
		b.WriteString("\n_release-please PR — only commit-lint runs._\n")
	}
	b.WriteString("\n| Floor | Status | Result | Δ vs main | Budget |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, f := range floors {
		marker := icon(f.Status)
		if f.Status == statusSuccess && f.Warn {
			marker = "⚠️"
		}
		fmt.Fprintf(&b, "| %d · %s | %s | %s | %s | %s |\n",
			f.Num, f.Name, marker, dash(f.Result), dash(f.Delta), dash(f.Budget))
	}
	b.WriteString("\n_Config: [.github/quality-gate.yml](../blob/main/.github/quality-gate.yml). " +
		"Workflow: `.github/workflows/quality-gate.yml`._\n")
	return b.String()
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func bypassedBody() string {
	return "### Quality Gate · Bypassed\n\nThis PR was skipped (bot actor or `quality-gate-skip` label).\n"
}

func coverageResult(current, baseline string) string {
	if current == "" {
		return "—"
	}
	if baseline == "" {
		return fmt.Sprintf("%s%% (bootstrap)", current)
	}
	return fmt.Sprintf("%s%% (baseline %s%%)", current, baseline)
}

func patchResult(pct, thr string) string {
	if pct == "" {
		return "—"
	}
	if thr == "" {
		return pct + "%"
	}
	return fmt.Sprintf("%s%% (req ≥ %s%%)", pct, thr)
}

func budgetThr(thr, suffix string) string {
	if thr == "" {
		return ""
	}
	return "≥ " + thr + suffix
}

func scopeResult(files, loc, code, tool string) string {
	if files == "" {
		return "—"
	}
	out := fmt.Sprintf("%s files / %s LOC", files, loc)
	if code != "" && tool != "" {
		out += fmt.Sprintf(" (code %s + tooling %s)", code, tool)
	}
	return out
}

func cycloResult(checked string) string {
	if checked == "" {
		return "—"
	}
	return checked + " file(s) under threshold"
}

func i18nResult(missing, unknown string) string {
	if missing == "" && unknown == "" {
		return "—"
	}
	return fmt.Sprintf("missing %s, unknown %s", missing, unknown)
}

func crdResult(drifted string) string {
	if drifted == "" {
		return "no drift"
	}
	return "drifted: " + drifted
}

func licenseResult(missing string) string {
	if missing == "" || missing == "0" {
		return "0 missing"
	}
	return missing + " missing"
}

func apiResult(incompat string) string {
	if incompat == "" || incompat == "0" {
		return "0 incompatible"
	}
	return incompat + " incompatible"
}

func binSizeResult(chatcli, operator string) string {
	if chatcli == "" && operator == "" {
		return "—"
	}
	return fmt.Sprintf("chatcli %s · operator %s", human(chatcli), human(operator))
}

func provParityResult(count, violations string) string {
	if count == "" {
		return "—"
	}
	v := "0"
	if violations != "" {
		v = violations
	}
	return fmt.Sprintf("%s providers · %s violations", count, v)
}

func human(b string) string {
	n := parseInt(b)
	if n == 0 {
		return "—"
	}
	mb := float64(n) / (1024 * 1024)
	return fmt.Sprintf("%.1fMB", mb)
}

func getenv(name string) string { return strings.TrimSpace(os.Getenv(name)) }

func parseInt(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func parseFloat(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }

// Force io.Discard import — needed to keep main package consistent
// when future helpers stream to /dev/null in tests.
var _ = io.Discard
