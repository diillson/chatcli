package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	return &SessionManager{sessionsDir: t.TempDir(), logger: zap.NewNop()}
}

func TestSearchSessions(t *testing.T) {
	sm := newTestSessionManager(t)

	if err := sm.SaveSessionV2("alpha", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "How do I configure the Bedrock provider?"},
			{Role: "assistant", Content: "Set the AWS region and credentials."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sm.SaveSessionV2("beta", &SessionData{
		Version: 2,
		AgentHistory: []models.Message{
			{Role: "user", Content: "Refactor the memory package."},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Single-term match in one session.
	hits, err := sm.SearchSessions("bedrock", 3)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Session != "alpha" {
		t.Fatalf("expected only alpha to match bedrock, got %+v", hits)
	}
	if hits[0].Matches != 1 || len(hits[0].Snippets) != 1 {
		t.Errorf("expected 1 match with 1 snippet, got %+v", hits[0])
	}

	// AND semantics: both terms must appear in the SAME message.
	if hits, _ := sm.SearchSessions("aws region", 3); len(hits) != 1 {
		t.Errorf("expected AND match for 'aws region', got %v", hits)
	}
	if hits, _ := sm.SearchSessions("bedrock refactor", 3); len(hits) != 0 {
		t.Errorf("terms spread across sessions must not match, got %v", hits)
	}

	// Agent history is searched too.
	if hits, _ := sm.SearchSessions("refactor", 3); len(hits) != 1 || hits[0].Session != "beta" {
		t.Errorf("expected beta to match 'refactor', got %v", hits)
	}

	// Empty query is an error.
	if _, err := sm.SearchSessions("   ", 3); err == nil {
		t.Error("expected error for empty query")
	}

	// No match returns empty, not error.
	if hits, err := sm.SearchSessions("nonexistentxyz", 3); err != nil || len(hits) != 0 {
		t.Errorf("expected no hits and no error, got %v / %v", hits, err)
	}
}

func TestSnippetAround(t *testing.T) {
	content := "prefix " + repeat("x", 200) + " bedrock " + repeat("y", 200) + " suffix"
	s := snippetAround(content, toLower(content), "bedrock")
	if len(s) > 160 {
		t.Errorf("snippet should be windowed, got len %d", len(s))
	}
	if !containsStr(s, "bedrock") {
		t.Errorf("snippet should contain the term, got %q", s)
	}
}

// tiny local helpers to avoid extra imports in the test
func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
