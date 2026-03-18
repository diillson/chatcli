package memory

import (
	"strings"
	"testing"
)

func TestTopicTracker_Record(t *testing.T) {
	dir := t.TempDir()
	tt := NewTopicTracker(dir, testLogger())

	tt.Record([]string{"Go", "Bubble Tea", "memory"})
	tt.Record([]string{"Go", "TUI"})

	topics := tt.GetAll()
	if len(topics) != 4 {
		t.Errorf("expected 4 topics, got %d", len(topics))
	}

	// Go should have 2 mentions
	for _, topic := range topics {
		if strings.EqualFold(topic.Name, "Go") && topic.Mentions != 2 {
			t.Errorf("expected Go mentions=2, got %d", topic.Mentions)
		}
	}
}

func TestTopicTracker_GetTopTopics(t *testing.T) {
	dir := t.TempDir()
	tt := NewTopicTracker(dir, testLogger())

	tt.Record([]string{"Go"})
	tt.Record([]string{"Go"})
	tt.Record([]string{"Go"})
	tt.Record([]string{"Python"})
	tt.Record([]string{"React"})

	top := tt.GetTopTopics(2)
	if len(top) != 2 {
		t.Errorf("expected 2 top topics, got %d", len(top))
	}
	if top[0].Name != "Go" {
		t.Errorf("expected Go as top topic, got %q", top[0].Name)
	}
}

func TestTopicTracker_Persistence(t *testing.T) {
	dir := t.TempDir()
	tt := NewTopicTracker(dir, testLogger())

	tt.Record([]string{"Docker", "K8s"})

	tt2 := NewTopicTracker(dir, testLogger())
	topics := tt2.GetAll()
	if len(topics) != 2 {
		t.Errorf("expected 2 persisted topics, got %d", len(topics))
	}
}

func TestTopicTracker_FormatForPrompt(t *testing.T) {
	dir := t.TempDir()
	tt := NewTopicTracker(dir, testLogger())

	if tt.FormatForPrompt(5) != "" {
		t.Error("expected empty prompt for empty topics")
	}

	tt.Record([]string{"Go", "Memory"})
	prompt := tt.FormatForPrompt(5)
	if !strings.Contains(prompt, "Go") {
		t.Errorf("expected Go in prompt, got %q", prompt)
	}
}
