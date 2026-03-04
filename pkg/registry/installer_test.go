package registry

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestInstallerInstallAndUninstall(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()
	inst := NewInstaller(tmpDir, logger)

	meta := &SkillMeta{
		Name:         "test-skill",
		Description:  "A test skill",
		Version:      "1.0.0",
		Author:       "tester",
		RegistryName: "chatcli",
	}

	content := []byte("# Test Skill\n\nThis is a test skill content.")

	// Install
	result, err := inst.Install(meta, content)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	if result.Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got %q", result.Name)
	}
	if result.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", result.Version)
	}
	if result.WasDuplicate {
		t.Error("expected WasDuplicate=false for first install")
	}

	// Verify SKILL.md exists
	skillFile := filepath.Join(tmpDir, "test-skill", "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("SKILL.md not found: %v", err)
	}

	content_str := string(data)
	if !containsString(content_str, "name: \"test-skill\"") {
		t.Error("SKILL.md missing frontmatter name")
	}
	if !containsString(content_str, "version: \"1.0.0\"") {
		t.Error("SKILL.md missing frontmatter version")
	}
	if !containsString(content_str, "This is a test skill content.") {
		t.Error("SKILL.md missing content body")
	}

	// IsInstalled
	if !inst.IsInstalled("test-skill") {
		t.Error("IsInstalled should return true")
	}
	if inst.IsInstalled("nonexistent") {
		t.Error("IsInstalled should return false for nonexistent")
	}

	// Install again (update / duplicate)
	result2, err := inst.Install(meta, content)
	if err != nil {
		t.Fatalf("second install failed: %v", err)
	}
	if !result2.WasDuplicate {
		t.Error("expected WasDuplicate=true for second install")
	}

	// Uninstall
	if err := inst.Uninstall("test-skill"); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	if inst.IsInstalled("test-skill") {
		t.Error("IsInstalled should return false after uninstall")
	}
}

func TestInstallerBlocksMalware(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()
	inst := NewInstaller(tmpDir, logger)

	meta := &SkillMeta{
		Name:    "malicious-skill",
		Version: "1.0.0",
		Moderation: ModerationFlags{
			MalwareDetected: true,
			Reason:          "contains known malware pattern",
		},
	}

	_, err := inst.Install(meta, []byte("malware"))
	if err == nil {
		t.Fatal("expected install to fail for malware-flagged skill")
	}
}

func TestInstallerBlocksQuarantined(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()
	inst := NewInstaller(tmpDir, logger)

	meta := &SkillMeta{
		Name:    "quarantined-skill",
		Version: "1.0.0",
		Moderation: ModerationFlags{
			Quarantined: true,
			Reason:      "under review",
		},
	}

	_, err := inst.Install(meta, []byte("content"))
	if err == nil {
		t.Fatal("expected install to fail for quarantined skill")
	}
}

func TestInstallerListInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()
	inst := NewInstaller(tmpDir, logger)

	// Install two skills
	meta1 := &SkillMeta{Name: "alpha", Version: "1.0", RegistryName: "chatcli"}
	meta2 := &SkillMeta{Name: "beta", Version: "2.0", RegistryName: "clawhub"}

	if _, err := inst.Install(meta1, []byte("alpha content")); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(meta2, []byte("beta content")); err != nil {
		t.Fatal(err)
	}

	installed, err := inst.ListInstalled()
	if err != nil {
		t.Fatalf("ListInstalled failed: %v", err)
	}

	if len(installed) != 2 {
		t.Fatalf("expected 2 installed skills, got %d", len(installed))
	}
}

func TestInstallerUninstallNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()
	inst := NewInstaller(tmpDir, logger)

	err := inst.Uninstall("nonexistent")
	if err == nil {
		t.Fatal("expected error for uninstalling nonexistent skill")
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Clean-Code", "clean-code"},
		{"My Skill", "my-skill"},
		{"some/path", "some-path"},
		{"../escape", "-escape"},
		{"  spaces  ", "spaces"},
	}

	for _, tt := range tests {
		got := sanitizeName(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeName(%q): got %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
