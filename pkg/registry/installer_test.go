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

	// Install — qualified name should be "chatcli--test-skill"
	result, err := inst.Install(meta, content)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	expectedName := "chatcli--test-skill"
	if result.Name != expectedName {
		t.Errorf("expected name %q, got %q", expectedName, result.Name)
	}
	if result.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", result.Version)
	}
	if result.WasDuplicate {
		t.Error("expected WasDuplicate=false for first install")
	}

	// Verify SKILL.md exists at qualified path
	skillFile := filepath.Join(tmpDir, expectedName, "SKILL.md")
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

	// IsInstalled — exact qualified name should work
	if !inst.IsInstalled(expectedName) {
		t.Error("IsInstalled should return true for qualified name")
	}
	// FindInstalled by base name should also find it
	matches := inst.FindInstalled("test-skill")
	if len(matches) != 1 {
		t.Errorf("FindInstalled should return 1 match, got %d", len(matches))
	}
	if len(matches) > 0 && matches[0].BaseName != "test-skill" {
		t.Errorf("expected BaseName 'test-skill', got %q", matches[0].BaseName)
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

	// Uninstall by qualified name
	if err := inst.Uninstall(expectedName); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	if inst.IsInstalled(expectedName) {
		t.Error("IsInstalled should return false after uninstall")
	}
}

func TestInstallerInstallLocalSkill(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()
	inst := NewInstaller(tmpDir, logger)

	// Local skill (no registry) — should use plain name
	meta := &SkillMeta{
		Name:    "my-local-skill",
		Version: "1.0.0",
	}

	result, err := inst.Install(meta, []byte("local content"))
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	if result.Name != "my-local-skill" {
		t.Errorf("local skill should use plain name, got %q", result.Name)
	}

	if !inst.IsInstalled("my-local-skill") {
		t.Error("should be installed")
	}
}

func TestInstallerNamespaceCollision(t *testing.T) {
	tmpDir := t.TempDir()
	logger := zap.NewNop()
	inst := NewInstaller(tmpDir, logger)

	// Install local version
	localMeta := &SkillMeta{Name: "frontend-design"}
	if _, err := inst.Install(localMeta, []byte("local version")); err != nil {
		t.Fatal(err)
	}

	// Install registry version — should NOT overwrite local
	registryMeta := &SkillMeta{
		Name:         "frontend-design",
		Slug:         "anthropics/skills/frontend-design",
		RegistryName: "skills.sh",
	}
	if _, err := inst.Install(registryMeta, []byte("registry version")); err != nil {
		t.Fatal(err)
	}

	// Both should exist
	matches := inst.FindInstalled("frontend-design")
	if len(matches) != 2 {
		t.Fatalf("expected 2 installs, got %d", len(matches))
	}

	// Check sources
	sources := map[string]bool{}
	for _, m := range matches {
		sources[m.Source] = true
	}
	if !sources["local"] {
		t.Error("should have local version")
	}
	if !sources["skills.sh"] {
		t.Error("should have skills.sh version")
	}

	// IsInstalledFromSource should distinguish
	if !inst.IsInstalledFromSource("frontend-design", "local") {
		t.Error("should find local version")
	}
	if !inst.IsInstalledFromSource("frontend-design", "skills.sh") {
		t.Error("should find skills.sh version")
	}
	if inst.IsInstalledFromSource("frontend-design", "clawhub") {
		t.Error("should NOT find clawhub version")
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

	// Install two skills from registries
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

	// Verify base names are populated
	for _, s := range installed {
		if s.BaseName == "" {
			t.Errorf("BaseName should not be empty for skill %q", s.Name)
		}
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

func TestQualifiedInstallName(t *testing.T) {
	tests := []struct {
		name     string
		meta     *SkillMeta
		expected string
	}{
		{
			"local skill — no prefix",
			&SkillMeta{Name: "my-skill"},
			"my-skill",
		},
		{
			"local source — no prefix",
			&SkillMeta{Name: "my-skill", RegistryName: "local"},
			"my-skill",
		},
		{
			"skills.sh with slug",
			&SkillMeta{Name: "frontend-design", Slug: "anthropics/skills/frontend-design", RegistryName: "skills.sh"},
			"anthropics-skills--frontend-design",
		},
		{
			"clawhub registry",
			&SkillMeta{Name: "code-review", RegistryName: "clawhub"},
			"clawhub--code-review",
		},
		{
			"chatcli registry",
			&SkillMeta{Name: "my-tool", RegistryName: "chatcli"},
			"chatcli--my-tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qualifiedInstallName(tt.meta)
			if got != tt.expected {
				t.Errorf("qualifiedInstallName: got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestParseQualifiedName(t *testing.T) {
	tests := []struct {
		input    string
		prefix   string
		baseName string
	}{
		{"frontend-design", "", "frontend-design"},
		{"anthropics-skills--frontend-design", "anthropics-skills", "frontend-design"},
		{"clawhub--code-review", "clawhub", "code-review"},
		{"skills.sh--my-skill", "skills.sh", "my-skill"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			prefix, baseName := parseQualifiedName(tt.input)
			if prefix != tt.prefix {
				t.Errorf("prefix: got %q, want %q", prefix, tt.prefix)
			}
			if baseName != tt.baseName {
				t.Errorf("baseName: got %q, want %q", baseName, tt.baseName)
			}
		})
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
