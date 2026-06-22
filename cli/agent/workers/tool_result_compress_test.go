package workers

import (
	"strings"
	"testing"
)

func TestTruncateToolResultUsesRegisteredCompressor(t *testing.T) {
	// A large result that the registered compressor shrinks below the inline
	// limit must be returned inline — no disk overflow, no "saved to" suffix.
	big := strings.Repeat("x", MaxInlineResultBytes+10_000)

	called := false
	RegisterToolOutputCompressor(func(toolName, output string) string {
		called = true
		if toolName != "search" {
			t.Errorf("tool name not forwarded: %q", toolName)
		}
		return "COMPRESSED<<ccr:worker-ccr-key>>"
	})
	defer RegisterToolOutputCompressor(nil)

	out := TruncateToolResult("search", big)
	if !called {
		t.Fatal("registered compressor was not invoked")
	}
	if !strings.Contains(out, "<<ccr:worker-ccr-key>>") {
		t.Fatalf("compressed output not used: %q", out)
	}
	if strings.Contains(out, "saved to") {
		t.Fatal("result fit inline after compression; must not overflow to disk")
	}
}

func TestTruncateToolResultNoCompressorFallsBack(t *testing.T) {
	RegisterToolOutputCompressor(nil)
	small := "tiny result"
	if got := TruncateToolResult("read", small); got != small {
		t.Fatalf("small result must pass through unchanged, got %q", got)
	}
}
