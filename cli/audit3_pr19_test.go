/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestLoadSessionV2_RefusesANewerSchema(t *testing.T) {
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "")
	dir := t.TempDir()
	sm, err := NewSessionManagerAt(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "future.json"), []byte(`{"version":99,"chat_history":[],"unknown_field":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = sm.LoadSessionV2("future")
	if !errors.Is(err, ErrSessionSchemaNewer) {
		t.Fatalf("a newer schema must be refused, got %v", err)
	}
	// The current schema and legacy files still load.
	if err := sm.SaveSession("now", []models.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if sd, err := sm.LoadSessionV2("now"); err != nil || sd.Version != models.SessionSchemaVersion {
		t.Fatalf("current schema: %+v %v", sd, err)
	}
}

func TestBuildSessionData_CarriesCostAndCCRReferences(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker()}
	c.history = []models.Message{
		{Role: "user", Content: "see <<ccr:abc123>> and <<ccr:def456>>"},
		{Role: "assistant", Content: "again <<ccr:abc123>>"},
	}
	sd := c.buildSessionData()
	if sd.CostSessionID == "" {
		t.Fatal("cost session reference must be persisted")
	}
	if len(sd.CCRKeys) != 2 || sd.CCRKeys[0] != "abc123" || sd.CCRKeys[1] != "def456" {
		t.Fatalf("ccr keys = %v", sd.CCRKeys)
	}
}

func TestForkTranscriptJournal_SeedsANewTimeline(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tr-parent.jsonl")
	if err := os.WriteFile(src, []byte("{\"kind\":\"msg\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &ChatCLI{logger: zap.NewNop(), transcript: &transcriptJournal{id: "tr-parent", path: src}}
	id := c.forkTranscriptJournal()
	if id == "" || id == "tr-parent" {
		t.Fatalf("fork must get a fresh id: %q", id)
	}
	seeded, err := os.ReadFile(filepath.Join(dir, id+".jsonl"))
	if err != nil || string(seeded) != "{\"kind\":\"msg\"}\n" {
		t.Fatalf("fork journal must be seeded from the parent: %q %v", seeded, err)
	}
	if c.transcriptID() != "tr-parent" {
		t.Fatal("the parent keeps its own journal")
	}
	if (&ChatCLI{}).forkTranscriptJournal() != "" {
		t.Fatal("no journal → no fork id")
	}
}
