package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"go.uber.org/zap"
)

func newTestCLIWithMemory(t *testing.T) *ChatCLI {
	t.Helper()
	ms := workspace.NewMemoryStore(t.TempDir(), zap.NewNop())
	return &ChatCLI{memoryStore: ms}
}

func TestMemoryPluginAdapter(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	a := &memoryPluginAdapter{cli: cli}

	// Remember a fact, then a duplicate (no-op), then forget it.
	if _, err := a.Remember("User earned the AWS SAA certification", "personal"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if _, err := a.Remember("User earned the AWS SAA certification", "personal"); err != nil {
		t.Fatalf("remember dup: %v", err)
	}
	out, err := a.Forget("AWS SAA")
	if err != nil || out == "" {
		t.Fatalf("forget: %q %v", out, err)
	}

	// Profile update lands.
	if _, err := a.UpdateProfile(map[string]string{"company": "Acme", "skills": "Go"}); err != nil {
		t.Fatalf("profile: %v", err)
	}
	if got := cli.memoryStore.Manager().Profile.Get().Company; got != "Acme" {
		t.Errorf("expected company Acme, got %q", got)
	}

	// Recall returns a string (empty-query path uses the general context).
	if _, err := a.Recall(""); err != nil {
		t.Errorf("recall: %v", err)
	}
	if _, err := a.Recall("certification"); err != nil {
		t.Errorf("recall query: %v", err)
	}
}

func TestMemoryPluginAdapter_NoStore(t *testing.T) {
	a := &memoryPluginAdapter{cli: &ChatCLI{}}
	if _, err := a.Remember("x", ""); err == nil {
		t.Error("expected error with no memory store")
	}
}

func TestFormatMemoryNotice(t *testing.T) {
	// Empty summary -> empty notice.
	if s := formatMemoryNotice(memory.ExtractionSummary{}); s != "" {
		t.Errorf("empty summary should yield empty notice, got %q", s)
	}
	// Non-empty summary -> non-empty notice mentioning the prefix.
	s := formatMemoryNotice(memory.ExtractionSummary{FactsAdded: 2, ProfileUpdated: true, ProjectsUpserted: 1, TopicsRecorded: 3})
	if s == "" {
		t.Error("non-empty summary should yield a notice")
	}
}

func TestSplitProfileKV(t *testing.T) {
	cases := []struct {
		in           string
		wantK, wantV string
		ok           bool
	}{
		{"company=Acme", "company", "Acme", true},
		{"role: SRE", "role", "SRE", true},
		{"  skills = Go, k8s ", "skills", "Go, k8s", true},
		{"noseparator", "", "", false},
		{"=novalue", "", "", false},
	}
	for _, c := range cases {
		k, v, ok := splitProfileKV(c.in)
		if ok != c.ok || k != c.wantK || v != c.wantV {
			t.Errorf("splitProfileKV(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, k, v, ok, c.wantK, c.wantV, c.ok)
		}
	}
}

func TestPushDrainMemoryNotices(t *testing.T) {
	cli := &ChatCLI{}
	cli.pushMemoryNotice("") // empty is ignored
	cli.pushMemoryNotice("memory: +1 fact")
	cli.memNoticeMu.Lock()
	n := len(cli.memNotices)
	cli.memNoticeMu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 queued notice, got %d", n)
	}
	cli.drainMemoryNotices() // prints + clears
	cli.memNoticeMu.Lock()
	n = len(cli.memNotices)
	cli.memNoticeMu.Unlock()
	if n != 0 {
		t.Errorf("expected notices drained, got %d", n)
	}
}

func TestSnippetAroundHelper(t *testing.T) {
	// Reuse the session snippet helper to ensure it stays covered/working.
	long := "alpha " + strings.Repeat("z", 300) + " needle " + strings.Repeat("y", 300)
	s := snippetAround(long, strings.ToLower(long), "needle")
	if !strings.Contains(s, "needle") {
		t.Errorf("snippet should contain the term: %q", s)
	}
}
