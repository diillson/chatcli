package workers

import "testing"

func TestClassifyToolConcurrency(t *testing.T) {
	tests := []struct {
		subcmd string
		want   ConcurrencyClass
	}{
		{"read", ConcurrencySafe},
		{"tree", ConcurrencySafe},
		{"search", ConcurrencySafe},
		{"git-status", ConcurrencySafe},
		{"git-diff", ConcurrencySafe},
		{"git-log", ConcurrencySafe},
		{"git-changed", ConcurrencySafe},
		{"git-branch", ConcurrencySafe},
		{"write", ConcurrencyFileScoped},
		{"patch", ConcurrencyFileScoped},
		{"rollback", ConcurrencyFileScoped},
		{"exec", ConcurrencySerial},
		{"test", ConcurrencySerial},
		{"clean", ConcurrencySerial},
		{"unknown", ConcurrencySerial},
	}

	for _, tt := range tests {
		t.Run(tt.subcmd, func(t *testing.T) {
			got := ClassifyToolConcurrency(tt.subcmd)
			if got != tt.want {
				t.Errorf("ClassifyToolConcurrency(%q) = %d, want %d", tt.subcmd, got, tt.want)
			}
		})
	}
}

func TestCanParallelizeToolCalls_AllReads(t *testing.T) {
	calls := []resolvedToolCall{
		{Subcmd: "read", RawArgs: `{"file":"a.go"}`},
		{Subcmd: "search", RawArgs: `{"term":"TODO"}`},
		{Subcmd: "tree", RawArgs: `{"dir":"."}`},
	}

	canPar, parallel, _ := CanParallelizeToolCalls(calls)
	if !canPar {
		t.Error("expected canParallel=true for all-read calls")
	}
	if len(parallel) != 3 {
		t.Errorf("expected 3 parallel calls, got %d", len(parallel))
	}
}

func TestCanParallelizeToolCalls_WriteDifferentFiles(t *testing.T) {
	calls := []resolvedToolCall{
		{Subcmd: "write", NativeArgs: map[string]interface{}{"file": "a.go"}, Native: true},
		{Subcmd: "write", NativeArgs: map[string]interface{}{"file": "b.go"}, Native: true},
		{Subcmd: "read", RawArgs: `{"file":"c.go"}`},
	}

	canPar, parallel, serial := CanParallelizeToolCalls(calls)
	if !canPar {
		t.Error("expected canParallel=true for writes to different files")
	}
	if len(parallel) != 3 {
		t.Errorf("expected 3 parallel calls, got %d (serial=%d)", len(parallel), len(serial))
	}
}

func TestCanParallelizeToolCalls_WriteSameFile(t *testing.T) {
	calls := []resolvedToolCall{
		{Subcmd: "write", NativeArgs: map[string]interface{}{"file": "a.go"}, Native: true},
		{Subcmd: "patch", NativeArgs: map[string]interface{}{"file": "a.go"}, Native: true},
	}

	canPar, parallel, serial := CanParallelizeToolCalls(calls)
	if canPar {
		t.Error("expected canParallel=false for writes to same file")
	}
	_ = parallel
	if len(serial) != 1 {
		t.Errorf("expected 1 serial call (conflict), got %d", len(serial))
	}
}

func TestCanParallelizeToolCalls_WithExec(t *testing.T) {
	calls := []resolvedToolCall{
		{Subcmd: "read", RawArgs: `{"file":"a.go"}`},
		{Subcmd: "exec", RawArgs: `{"cmd":"ls"}`},
	}

	canPar, _, _ := CanParallelizeToolCalls(calls)
	if canPar {
		t.Error("expected canParallel=false when exec is present")
	}
}

func TestCanParallelizeToolCalls_Single(t *testing.T) {
	calls := []resolvedToolCall{
		{Subcmd: "read", RawArgs: `{"file":"a.go"}`},
	}

	canPar, _, _ := CanParallelizeToolCalls(calls)
	if canPar {
		t.Error("expected canParallel=false for single call")
	}
}
