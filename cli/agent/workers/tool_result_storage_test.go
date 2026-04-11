package workers

import (
	"strings"
	"testing"
)

func TestTruncateToolResult_SmallResult(t *testing.T) {
	small := "hello world"
	result := TruncateToolResult("read", small)
	if result != small {
		t.Errorf("small result should pass through unchanged, got %q", result)
	}
}

func TestTruncateToolResult_LargeResult(t *testing.T) {
	// Create a result larger than MaxInlineResultBytes
	large := strings.Repeat("x", MaxInlineResultBytes+1000)
	result := TruncateToolResult("read", large)

	if len(result) >= len(large) {
		t.Error("large result should be truncated")
	}

	if !strings.Contains(result, "full output saved to") {
		t.Error("truncated result should contain file reference")
	}

	if !strings.Contains(result, "bytes total") {
		t.Error("truncated result should mention total byte count")
	}
}

func TestTruncateToolResult_ExactBoundary(t *testing.T) {
	exact := strings.Repeat("x", MaxInlineResultBytes)
	result := TruncateToolResult("read", exact)
	if result != exact {
		t.Error("result at exact boundary should pass through unchanged")
	}
}

func TestCleanupResultFiles(t *testing.T) {
	// Just verify it doesn't panic
	CleanupResultFiles()
}
