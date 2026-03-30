package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextBuilder_BuildSystemPromptPrefix(t *testing.T) {
	wsDir := t.TempDir()
	globalDir := t.TempDir()
	memDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("You are a helpful assistant"), 0o644)
	_ = os.WriteFile(filepath.Join(globalDir, "USER.md"), []byte("I prefer concise answers"), 0o644)

	// Create memory
	_ = os.MkdirAll(filepath.Join(memDir, "memory"), 0o755)
	_ = os.WriteFile(filepath.Join(memDir, "memory", "MEMORY.md"), []byte("# Key facts\n- User likes Go"), 0o644)

	bl := NewBootstrapLoader(wsDir, globalDir, testLogger())
	ms := NewMemoryStore(memDir, testLogger())
	cb := NewContextBuilder(bl, ms, t.TempDir())

	result := cb.BuildSystemPromptPrefix()

	if !strings.Contains(result, "You are a helpful assistant") {
		t.Error("expected SOUL.md content in prefix")
	}
	if !strings.Contains(result, "I prefer concise answers") {
		t.Error("expected USER.md content in prefix")
	}
	if !strings.Contains(result, "User likes Go") {
		t.Error("expected MEMORY.md content in prefix")
	}
}

func TestContextBuilder_EmptyFiles(t *testing.T) {
	bl := NewBootstrapLoader(t.TempDir(), t.TempDir(), testLogger())
	ms := NewMemoryStore(t.TempDir(), testLogger())
	cb := NewContextBuilder(bl, ms, t.TempDir())

	result := cb.BuildSystemPromptPrefix()
	if result != "" {
		t.Errorf("expected empty result for missing files, got %q", result)
	}
}

func TestContextBuilder_Cache(t *testing.T) {
	wsDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("v1"), 0o644)

	bl := NewBootstrapLoader(wsDir, "", testLogger())
	ms := NewMemoryStore(t.TempDir(), testLogger())
	cb := NewContextBuilder(bl, ms, t.TempDir())

	r1 := cb.BuildSystemPromptPrefix()
	r2 := cb.BuildSystemPromptPrefix()

	if r1 != r2 {
		t.Error("cached result should be identical")
	}
}

func TestContextBuilder_DynamicContext(t *testing.T) {
	bl := NewBootstrapLoader(t.TempDir(), t.TempDir(), testLogger())
	ms := NewMemoryStore(t.TempDir(), testLogger())
	wsDir := t.TempDir()
	cb := NewContextBuilder(bl, ms, wsDir)

	dyn := cb.BuildDynamicContext()
	if !strings.Contains(dyn, "Current date and time") {
		t.Errorf("expected dynamic context with date, got %q", dyn)
	}
	if !strings.Contains(dyn, "Current working directory: "+wsDir) {
		t.Errorf("expected CWD in dynamic context, got %q", dyn)
	}
	if !strings.Contains(dyn, "IMPORTANT") {
		t.Errorf("expected disambiguation instruction in dynamic context, got %q", dyn)
	}
}

func TestContextBuilder_InvalidateCache(t *testing.T) {
	wsDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("v1"), 0o644)

	bl := NewBootstrapLoader(wsDir, "", testLogger())
	ms := NewMemoryStore(t.TempDir(), testLogger())
	cb := NewContextBuilder(bl, ms, t.TempDir())

	cb.BuildSystemPromptPrefix()
	cb.InvalidateCache()

	// Should rebuild after invalidation
	result := cb.BuildSystemPromptPrefix()
	if !strings.Contains(result, "v1") {
		t.Error("expected content after cache invalidation")
	}
}
