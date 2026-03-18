package memory

import (
	"os"
	"strings"
	"testing"
)

func TestManager_BackwardCompatible(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	// Test AppendLongTerm
	err := mgr.AppendLongTerm("- Go project uses goroutines\n- User prefers concise answers")
	if err != nil {
		t.Fatalf("AppendLongTerm failed: %v", err)
	}

	if mgr.Facts.Count() < 1 {
		t.Error("expected facts to be added")
	}

	// Test ReadLongTerm
	content := mgr.ReadLongTerm()
	if !strings.Contains(content, "goroutines") {
		t.Error("expected fact content in ReadLongTerm")
	}

	// Test WriteDailyNote
	err = mgr.WriteDailyNote("Test daily entry")
	if err != nil {
		t.Fatalf("WriteDailyNote failed: %v", err)
	}

	notes := mgr.GetRecentDailyNotes(3)
	if len(notes) == 0 {
		t.Error("expected at least one daily note")
	}
}

func TestManager_GetMemoryContext(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	_ = mgr.AppendLongTerm("- Important architectural decision about memory")
	_ = mgr.WriteDailyNote("Worked on memory system today")

	ctx := mgr.GetMemoryContext()
	if ctx == "" {
		t.Error("expected non-empty memory context")
	}
	if !strings.Contains(ctx, "Important architectural") {
		t.Error("expected fact in context")
	}
}

func TestManager_ProcessExtraction(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	response := `## DAILY
- Modified cli/workspace/memory/store.go
- Added new profile system

## LONGTERM
- ChatCLI uses Go 1.25 with Bubble Tea TUI framework
- User prefers Portuguese for conversation

## PROFILE_UPDATE
name=Edilson
role=Software Engineer
expertise_level=expert
preferred_language=Portuguese

## TOPICS
Go, Bubble Tea, memory systems, TUI

## PROJECTS
project_name=chatcli
project_path=/Users/edilson/GolandProjects/chatcli
project_status=active
project_technologies=Go, Bubble Tea`

	mgr.ProcessExtraction(response)

	// Check profile
	profile := mgr.Profile.Get()
	if profile.Name != "Edilson" {
		t.Errorf("expected name Edilson, got %q", profile.Name)
	}
	if profile.Role != "Software Engineer" {
		t.Errorf("expected role, got %q", profile.Role)
	}

	// Check topics
	topics := mgr.Topics.GetAll()
	if len(topics) < 3 {
		t.Errorf("expected at least 3 topics, got %d", len(topics))
	}

	// Check projects
	projects := mgr.Projects.GetAll()
	if len(projects) != 1 {
		t.Errorf("expected 1 project, got %d", len(projects))
	}

	// Check facts
	if mgr.Facts.Count() < 1 {
		t.Error("expected at least one fact from LONGTERM section")
	}
}

func TestManager_RelevantContext(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, DefaultConfig(), testLogger())

	mgr.Facts.AddFact("Go uses goroutines for concurrency", "pattern", []string{"go"})
	mgr.Facts.AddFact("Python uses asyncio for async", "pattern", []string{"python"})

	// Search with Go hints should prioritize Go facts
	ctx := mgr.GetRelevantContext([]string{"go", "goroutine"})
	if !strings.Contains(ctx, "goroutines") {
		t.Error("expected Go fact in relevant context")
	}
}

func TestManager_Migration(t *testing.T) {
	dir := t.TempDir()

	// Create legacy MEMORY.md
	memDir := dir
	content := `# Key Facts
- Go project uses Bubble Tea for TUI
- OAuth requires plain http.Client
- User prefers concise answers

## Architecture
- Agent mode in cli/agent_mode.go
- CLIBridge pattern avoids import cycles
`
	if err := writeFile(memDir+"/MEMORY.md", content); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(memDir, DefaultConfig(), testLogger())

	if mgr.Facts.Count() < 3 {
		t.Errorf("expected at least 3 migrated facts, got %d", mgr.Facts.Count())
	}

	// Verify backup was created
	_, err := readFile(memDir + "/MEMORY.md.bak")
	if err != nil {
		t.Error("expected MEMORY.md.bak to exist after migration")
	}
}

func TestParseEnhancedResponse(t *testing.T) {
	response := `## DAILY
- Read config files

## LONGTERM
- Go uses embed.FS for static assets

## PROFILE_UPDATE
name=Test
role=Dev

## TOPICS
Go, Testing

## PROJECTS
project_name=myapp
project_status=active`

	daily, longTerm, profile, topics, projects := parseEnhancedResponse(response)

	if !strings.Contains(daily, "Read config") {
		t.Errorf("expected daily content, got %q", daily)
	}
	if !strings.Contains(longTerm, "embed.FS") {
		t.Errorf("expected longterm content, got %q", longTerm)
	}
	if profile["name"] != "Test" {
		t.Errorf("expected profile name=Test, got %v", profile)
	}
	if len(topics) < 2 {
		t.Errorf("expected 2 topics, got %v", topics)
	}
	if projects["project_name"] != "myapp" {
		t.Errorf("expected project_name=myapp, got %v", projects)
	}
}

func TestParseEnhancedResponse_NothingNew(t *testing.T) {
	daily, longTerm, profile, topics, projects := parseEnhancedResponse("NOTHING_NEW")

	if daily != "" || longTerm != "" || len(profile) > 0 || len(topics) > 0 || len(projects) > 0 {
		t.Error("expected all empty for NOTHING_NEW")
	}
}

// helpers

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
