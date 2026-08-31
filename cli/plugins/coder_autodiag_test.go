/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * coder_autodiag_test.go
 *
 * Post-edit diagnostics: a successful @coder mutation must surface language-
 * server findings immediately, stay silent on clean files, respect the env
 * kill switch, and degrade to a no-op when no capable adapter is wired.
 */
package plugins

import (
	"strings"
	"testing"
	"time"
)

// fakeDiagAdapter implements LSPAdapter plus the LSPQuickDiagnoser capability.
type fakeDiagAdapter struct {
	findings map[string]string // file -> rendered findings ("" = clean)
	calls    []string
}

func (f *fakeDiagAdapter) Diagnostics(file string) (string, error) { return f.findings[file], nil }
func (f *fakeDiagAdapter) Definition(string, int, int) (string, error) {
	return "", nil
}
func (f *fakeDiagAdapter) References(string, int, int, bool, int) (string, error) {
	return "", nil
}
func (f *fakeDiagAdapter) Symbols(string) (string, error)         { return "", nil }
func (f *fakeDiagAdapter) Hover(string, int, int) (string, error) { return "", nil }

func (f *fakeDiagAdapter) QuickDiagnostics(file string) (string, bool, error) {
	f.calls = append(f.calls, file)
	text := f.findings[file]
	return text, text != "", nil
}

func withLSPAdapter(t *testing.T, a LSPAdapter) {
	t.Helper()
	SetLSPAdapter(a)
	t.Cleanup(func() { SetLSPAdapter(nil) })
}

func TestAutoDiagTargets(t *testing.T) {
	cases := []struct {
		name   string
		subcmd string
		args   []string
		want   []string
	}{
		{"write two-token", "write", []string{"--file", "main.go", "--content", "x"}, []string{"main.go"}},
		{"patch inline form", "patch", []string{"--file=pkg/a/b.go", "--search", "x", "--replace", "y"}, []string{"pkg/a/b.go"}},
		{"multipatch json dedup", "multipatch", []string{"--edits", `[{"file":"a.go"},{"file":"b.go"},{"file":"a.go"}]`}, []string{"a.go", "b.go"}},
		{"multipatch bad json", "multipatch", []string{"--edits", "not json"}, nil},
		{"read is not mutating", "read", []string{"--file", "main.go"}, nil},
		{"write without file", "write", []string{"--content", "x"}, nil},
	}
	for _, tc := range cases {
		got := autoDiagTargets(tc.subcmd, tc.args)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
			}
		}
	}
}

func TestAppendAutoDiagnostics_InjectsFindings(t *testing.T) {
	fake := &fakeDiagAdapter{findings: map[string]string{
		"main.go": "1 diagnostic(s) in main.go:\n- L3:1 [error] undefined: foo (compiler)",
	}}
	withLSPAdapter(t, fake)

	out := appendAutoDiagnostics("write", []string{"--file", "main.go"}, "File written.")
	if !strings.Contains(out, "[DIAGNOSTICS]") || !strings.Contains(out, "undefined: foo") {
		t.Fatalf("expected findings appended, got:\n%s", out)
	}
	if !strings.HasPrefix(out, "File written.") {
		t.Fatalf("original output must be preserved, got:\n%s", out)
	}
}

func TestAppendAutoDiagnostics_SilentOnCleanFile(t *testing.T) {
	fake := &fakeDiagAdapter{findings: map[string]string{}}
	withLSPAdapter(t, fake)

	out := appendAutoDiagnostics("write", []string{"--file", "clean.go"}, "File written.")
	if out != "File written." {
		t.Fatalf("clean file must append nothing, got:\n%s", out)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "clean.go" {
		t.Fatalf("expected exactly one diagnostics call, got %v", fake.calls)
	}
}

func TestAppendAutoDiagnostics_KillSwitch(t *testing.T) {
	fake := &fakeDiagAdapter{findings: map[string]string{"main.go": "boom"}}
	withLSPAdapter(t, fake)
	t.Setenv(coderAutoDiagEnv, "off")

	out := appendAutoDiagnostics("write", []string{"--file", "main.go"}, "File written.")
	if out != "File written." {
		t.Fatalf("kill switch must disable the hook, got:\n%s", out)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("no diagnostics call expected with the kill switch on, got %v", fake.calls)
	}
}

func TestAppendAutoDiagnostics_NoAdapterOrCapability(t *testing.T) {
	// No adapter wired at all.
	SetLSPAdapter(nil)
	if out := appendAutoDiagnostics("write", []string{"--file", "x.go"}, "ok"); out != "ok" {
		t.Fatalf("nil adapter must no-op, got:\n%s", out)
	}
	// Adapter without the quick capability: the interface holds a
	// *fakeDiagAdapter inner value only — pass the plain LSPAdapter subset.
	var subset LSPAdapter = &lspAdapterOnly{}
	withLSPAdapter(t, subset)
	if out := appendAutoDiagnostics("write", []string{"--file", "x.go"}, "ok"); out != "ok" {
		t.Fatalf("adapter without capability must no-op, got:\n%s", out)
	}
}

// lspAdapterOnly implements LSPAdapter and deliberately NOT LSPQuickDiagnoser.
type lspAdapterOnly struct{}

func (*lspAdapterOnly) Diagnostics(string) (string, error)                     { return "", nil }
func (*lspAdapterOnly) Definition(string, int, int) (string, error)            { return "", nil }
func (*lspAdapterOnly) References(string, int, int, bool, int) (string, error) { return "", nil }
func (*lspAdapterOnly) Symbols(string) (string, error)                         { return "", nil }
func (*lspAdapterOnly) Hover(string, int, int) (string, error)                 { return "", nil }

func TestAppendAutoDiagnostics_BudgetCap(t *testing.T) {
	big := strings.Repeat("x", maxAutoDiagBytes)
	fake := &fakeDiagAdapter{findings: map[string]string{
		"a.go": big,
		"b.go": "1 diagnostic(s) in b.go:\n- L1:1 [error] boom",
	}}
	withLSPAdapter(t, fake)

	out := appendAutoDiagnostics("multipatch",
		[]string{"--edits", `[{"file":"a.go"},{"file":"b.go"}]`}, "done")
	if !strings.Contains(out, big) {
		t.Fatal("first file findings should fit the budget")
	}
	if strings.Contains(out, "boom") {
		t.Fatal("second file must be elided once the budget is spent")
	}
	if !strings.Contains(out, "findings omitted (diagnostics budget)") {
		t.Fatalf("elision must be explicit, got:\n%s", out)
	}
}

func TestAppendAutoDiagnostics_FileCap(t *testing.T) {
	fake := &fakeDiagAdapter{findings: map[string]string{}}
	withLSPAdapter(t, fake)

	edits := `[{"file":"a.go"},{"file":"b.go"},{"file":"c.go"},{"file":"d.go"},{"file":"e.go"},{"file":"f.go"},{"file":"g.go"}]`
	appendAutoDiagnostics("multipatch", []string{"--edits", edits}, "done")
	if len(fake.calls) != maxAutoDiagFiles {
		t.Fatalf("expected at most %d diagnostics calls, got %d", maxAutoDiagFiles, len(fake.calls))
	}
}

// slowDiagAdapter is a fakeDiagAdapter whose every QuickDiagnostics call burns
// wall-clock time, to exercise the pass-level budget.
type slowDiagAdapter struct {
	fakeDiagAdapter
	delay time.Duration
}

func (s *slowDiagAdapter) QuickDiagnostics(file string) (string, bool, error) {
	time.Sleep(s.delay)
	return s.fakeDiagAdapter.QuickDiagnostics(file)
}

func TestAppendAutoDiagnostics_TimeBudget(t *testing.T) {
	prev := autoDiagPassBudget
	autoDiagPassBudget = 50 * time.Millisecond
	t.Cleanup(func() { autoDiagPassBudget = prev })

	slow := &slowDiagAdapter{
		fakeDiagAdapter: fakeDiagAdapter{findings: map[string]string{}},
		delay:           40 * time.Millisecond,
	}
	withLSPAdapter(t, slow)

	edits := `[{"file":"a.go"},{"file":"b.go"},{"file":"c.go"},{"file":"d.go"},{"file":"e.go"}]`
	out := appendAutoDiagnostics("multipatch", []string{"--edits", edits}, "done")

	if len(slow.calls) >= 5 {
		t.Fatalf("time budget must stop the pass early, but all %d files were checked", len(slow.calls))
	}
	if !strings.Contains(out, "not checked (diagnostics time budget)") {
		t.Fatalf("skipped files must be reported explicitly, got:\n%s", out)
	}
}
