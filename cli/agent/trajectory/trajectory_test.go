package trajectory

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestToTurns(t *testing.T) {
	msgs := []models.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "List files."},
		{Role: "assistant", Content: "", ToolCalls: []models.ToolCall{
			{Name: "@read", Arguments: map[string]interface{}{"path": "a.go"}},
		}},
		{Role: "tool", ToolCallID: "1", Content: "package main"},
		{Role: "assistant", Content: "Done."},
		{Role: "user", Content: "   "}, // blank -> skipped
	}

	turns := ToTurns(msgs)
	if len(turns) != 5 {
		t.Fatalf("expected 5 turns (blank skipped), got %d: %+v", len(turns), turns)
	}
	if turns[0].From != "system" || turns[1].From != "human" || turns[2].From != "gpt" || turns[3].From != "tool" {
		t.Errorf("role mapping wrong: %+v", turns[:4])
	}
	if !strings.Contains(turns[2].Value, `<tool_call name="@read">`) || !strings.Contains(turns[2].Value, `"path":"a.go"`) {
		t.Errorf("tool_call not rendered: %q", turns[2].Value)
	}
	if !strings.Contains(turns[3].Value, "<tool_response>package main</tool_response>") {
		t.Errorf("tool_response not rendered: %q", turns[3].Value)
	}
}

func TestWriteJSONL(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	var sb strings.Builder
	n, err := WriteJSONL(&sb, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 lines, got %d", n)
	}
	lines := strings.Split(strings.TrimSpace(sb.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines, got %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], `{"from":"human"`) {
		t.Errorf("unexpected first line: %s", lines[0])
	}
}
