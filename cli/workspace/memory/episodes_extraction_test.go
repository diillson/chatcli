/*
 * ChatCLI - Episode extraction pipeline tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package memory

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestProcessExtractionResult_Episodes(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(m.WaitGraphPersist)
	m.SetWorkspaceDir("/home/u/chatcli")

	sum := m.ProcessExtractionResult(`## DAILY
- worked on things

## EPISODES
- Fixed the OAuth refresh loop :: shipped as PR 1047 :: llm/anthropic/auth.go, go test
- Migrated the schema
`)

	if sum.EpisodesAdded != 2 {
		t.Fatalf("expected 2 episodes added, got %d", sum.EpisodesAdded)
	}
	eps := m.Timeline(time.Time{}, time.Time{}, "", "", 0)
	if len(eps) != 2 {
		t.Fatalf("expected 2 stored episodes, got %d", len(eps))
	}
	var oauth *Episode
	for _, e := range eps {
		if strings.Contains(e.Summary, "OAuth") {
			oauth = e
		}
	}
	if oauth == nil {
		t.Fatal("OAuth episode missing")
	}
	if oauth.Outcome != "shipped as PR 1047" {
		t.Errorf("outcome wrong: %q", oauth.Outcome)
	}
	if len(oauth.Refs) != 2 || oauth.Refs[0] != "llm/anthropic/auth.go" {
		t.Errorf("refs wrong: %v", oauth.Refs)
	}
	if oauth.Project != "/home/u/chatcli" {
		t.Errorf("episode must inherit the session workspace, got %q", oauth.Project)
	}
	if oauth.Source != ProvenanceExtraction {
		t.Errorf("source must be extraction, got %q", oauth.Source)
	}

	// Re-processing the same response must not duplicate (idempotent segments).
	again := m.ProcessExtractionResult("## EPISODES\n- Migrated the schema\n")
	if again.EpisodesAdded != 0 {
		t.Errorf("re-extraction must dedup, added %d", again.EpisodesAdded)
	}
}

func TestProcessExtractionResult_EpisodesNothingNew(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(m.WaitGraphPersist)
	sum := m.ProcessExtractionResult("## EPISODES\nNOTHING_NEW\n")
	if sum.EpisodesAdded != 0 {
		t.Errorf("NOTHING_NEW section must add nothing, got %d", sum.EpisodesAdded)
	}
	if !sum.IsEmpty() {
		t.Error("summary must be empty")
	}
}

func TestGetMemoryIndex_IncludesEpisodes(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(m.WaitGraphPersist)
	m.Episodes.Add(Episode{Date: time.Now(), Summary: "Shipped the exporter"})

	idx := m.GetMemoryIndex(600)
	if !strings.Contains(idx, "Episodes: 1") {
		t.Errorf("index digest must count episodes, got: %s", idx)
	}
}

func TestRetriever_RecentEpisodesSection(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(m.WaitGraphPersist)
	m.Episodes.Add(Episode{Date: time.Now().Add(-24 * time.Hour), Summary: "Hardened the parser", Outcome: "merged"})

	out := m.GetMemoryContext()
	if !strings.Contains(out, "## Recent Episodes") || !strings.Contains(out, "Hardened the parser") {
		t.Errorf("retrieval must include the recent episodes section, got: %s", out)
	}
}
