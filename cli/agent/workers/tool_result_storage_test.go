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

func TestOverflowToDisk_LargePreviewPreserved(t *testing.T) {
	SetResultDir(t.TempDir())
	defer SetResultDir("")

	content := strings.Repeat("worker output line\n", 3000) // ~57KB
	out := overflowToDisk("worker", content, MaxWorkerOutputBytes-256)

	if !strings.Contains(out, "full output saved to") {
		t.Fatalf("expected overflow reference, got tail %q", out[len(out)-120:])
	}
	// Preview must be near the requested budget, not the 4KB default.
	if len(out) < 20*1024 {
		t.Errorf("preview too small (%d bytes) — requested budget was ~30KB", len(out))
	}
	if len(out) > MaxWorkerOutputBytes+256 {
		t.Errorf("inline result exceeds the historical bound: %d bytes", len(out))
	}
}

func TestOverflowToDisk_SmallContentUnchanged(t *testing.T) {
	content := "small"
	if got := overflowToDisk("worker", content, 1024); got != content {
		t.Errorf("small content must pass through unchanged, got %q", got)
	}
}

func TestOverflowToDisk_PreviewLargerThanContent(t *testing.T) {
	content := strings.Repeat("x", MaxInlineResultBytes+10)
	if got := overflowToDisk("worker", content, MaxInlineResultBytes+100); got != content {
		t.Error("content below the preview budget must pass through unchanged")
	}
}
