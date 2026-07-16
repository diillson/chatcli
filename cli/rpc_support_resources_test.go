/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// i18n is initialized by TestMain in config_sections_test.go.

// newResourcesCLI wires the stores the resource surface reads: memory,
// contexts, skills and sessions, all rooted in temp dirs.
func newResourcesCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Fatalf("NewContextHandler: %v", err)
	}
	sm, err := NewSessionManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	projDir := t.TempDir()
	skillsDir := filepath.Join(projDir, ".agent", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := `---
name: res-skill
description: resource surface test skill
triggers: ["resource"]
---
RES-SKILL-BODY
`
	if err := os.WriteFile(filepath.Join(skillsDir, "res-skill.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	pm := persona.NewManager(zap.NewNop())
	pm.SetProjectDir(projDir)
	if _, err := pm.RefreshSkills(); err != nil {
		t.Fatal(err)
	}

	c := &ChatCLI{
		logger:         zap.NewNop(),
		contextHandler: ch,
		sessionManager: sm,
		memoryStore:    workspace.NewMemoryStore(t.TempDir(), zap.NewNop()),
		personaHandler: &PersonaHandler{manager: pm, logger: zap.NewNop()},
	}
	c.skillHandler = NewSkillHandler(zap.NewNop(), pm)
	return c
}

func TestRPCResources_ListAndRead(t *testing.T) {
	c := newResourcesCLI(t)

	// Seed state: memory note, a context, a saved session.
	if err := c.memoryStore.AppendLongTerm("- prefers dark mode UIs"); err != nil {
		t.Fatalf("AppendLongTerm: %v", err)
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "arch.md")
	if err := os.WriteFile(f, []byte("RESOURCE-CTX-MARKER architecture notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.contextHandler.GetManager().CreateContext(context.Background(), "arch", "architecture", []string{f}, ctxmgr.ModeFull, nil, false); err != nil {
		t.Fatalf("CreateContext: %v", err)
	}
	if err := c.sessionManager.SaveSessionV2("res-session", &SessionData{
		ChatHistory: []models.Message{{Role: "user", Content: "saved-turn"}},
	}); err != nil {
		t.Fatalf("SaveSessionV2: %v", err)
	}

	uris := map[string]bool{}
	for _, r := range c.ListRPCResources() {
		uris[r.URI] = true
	}
	for _, want := range []string{
		"chatcli://memory/index", "chatcli://memory/longterm", "chatcli://memory/profile",
		"chatcli://memory/projects", "chatcli://contexts", "chatcli://contexts/arch",
		"chatcli://skills", "chatcli://skills/res-skill", "chatcli://sessions",
	} {
		if !uris[want] {
			t.Errorf("resource list missing %s (have %v)", want, uris)
		}
	}

	reads := []struct{ uri, want, mime string }{
		{"chatcli://memory/longterm", "dark mode", "text/markdown"},
		{"chatcli://contexts/arch", "RESOURCE-CTX-MARKER", "text/plain"},
		{"chatcli://skills/res-skill", "RES-SKILL-BODY", "text/markdown"},
		{"chatcli://sessions", "res-session", "text/plain"},
		{"chatcli://sessions/res-session", "saved-turn", "application/json"},
	}
	for _, tc := range reads {
		got, err := c.ReadRPCResource(context.Background(), tc.uri)
		if err != nil {
			t.Errorf("read %s: %v", tc.uri, err)
			continue
		}
		if !strings.Contains(got.Text, tc.want) {
			t.Errorf("%s: content missing %q:\n%s", tc.uri, tc.want, got.Text)
		}
		if got.MimeType != tc.mime {
			t.Errorf("%s: mime = %q, want %q", tc.uri, got.MimeType, tc.mime)
		}
	}

	// The skills catalog must expose triggers (that is what lets an external
	// client reproduce trigger-activation semantics).
	got, err := c.ReadRPCResource(context.Background(), "chatcli://skills")
	if err != nil {
		t.Fatalf("skills catalog: %v", err)
	}
	var entries []struct {
		Name     string   `json:"name"`
		Triggers []string `json:"triggers"`
	}
	if err := json.Unmarshal([]byte(got.Text), &entries); err != nil {
		t.Fatalf("skills catalog is not JSON: %v\n%s", err, got.Text)
	}
	found := false
	for _, e := range entries {
		if e.Name == "res-skill" && len(e.Triggers) == 1 && e.Triggers[0] == "resource" {
			found = true
		}
	}
	if !found {
		t.Errorf("skills catalog missing res-skill with its trigger: %s", got.Text)
	}
}

func TestRPCResources_GateAndErrors(t *testing.T) {
	c := newResourcesCLI(t)

	t.Setenv("CHATCLI_MCP_RESOURCES", "off")
	if got := c.ListRPCResources(); len(got) != 0 {
		t.Errorf("gate off must hide every resource; got %d", len(got))
	}
	if _, err := c.ReadRPCResource(context.Background(), "chatcli://memory/index"); err == nil {
		t.Error("gate off must refuse reads")
	}

	t.Setenv("CHATCLI_MCP_RESOURCES", "")
	if _, err := c.ReadRPCResource(context.Background(), "https://evil.example/x"); err == nil {
		t.Error("non-chatcli scheme must be refused")
	}
	if _, err := c.ReadRPCResource(context.Background(), "chatcli://memory/nope"); err == nil {
		t.Error("unknown memory resource must be refused")
	}
	if _, err := c.ReadRPCResource(context.Background(), "chatcli://sessions/../../etc/passwd"); err == nil {
		t.Error("invalid session name must be refused")
	}
}

func TestRPCResources_KnowledgePagedRead(t *testing.T) {
	c := newResourcesCLI(t)

	dir := t.TempDir()
	f := filepath.Join(dir, "guide.md")
	if err := os.WriteFile(f, []byte("# Guide\n\nKNOWLEDGE-DOC-MARKER long body"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := c.contextHandler.GetManager()
	if _, err := mgr.CreateContext(context.Background(), "kbase", "test kb", []string{f}, ctxmgr.ModeKnowledge, nil, false); err != nil {
		t.Fatalf("CreateContext knowledge: %v", err)
	}

	toc, err := c.ReadRPCResource(context.Background(), "chatcli://knowledge/kbase")
	if err != nil {
		t.Fatalf("knowledge TOC: %v", err)
	}
	if !strings.Contains(toc.Text, "guide.md") {
		t.Errorf("TOC missing source path:\n%s", toc.Text)
	}

	// Read the document via the path listed in the TOC.
	source := ""
	for _, line := range strings.Split(toc.Text, "\n") {
		if strings.Contains(line, "guide.md") {
			fields := strings.Fields(line)
			for _, fd := range fields {
				if strings.HasSuffix(fd, "guide.md") {
					source = fd
				}
			}
		}
	}
	if source == "" {
		t.Fatalf("could not extract source path from TOC:\n%s", toc.Text)
	}
	doc, err := c.ReadRPCResource(context.Background(), "chatcli://knowledge/kbase/"+source)
	if err != nil {
		t.Fatalf("knowledge doc read: %v", err)
	}
	if !strings.Contains(doc.Text, "KNOWLEDGE-DOC-MARKER") {
		t.Errorf("document content missing:\n%s", doc.Text)
	}
}
