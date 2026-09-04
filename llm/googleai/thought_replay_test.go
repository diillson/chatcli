package googleai

import (
	"encoding/json"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Gemini's signature is an encrypted handle to the model's reasoning
// state, and the stateless path this client uses must resend every thought
// part exactly as received.
func TestParseGeminiThoughtBody(t *testing.T) {
	blocks := client.ParseGeminiThoughtBody([]byte(`{"candidates":[{"content":{"parts":[
		{"text":"weighing","thought":true,"thoughtSignature":"sig-1"},
		{"text":"unsigned thought","thought":true},
		{"text":"visible answer"}
	]}}]}`))
	if len(blocks) != 1 {
		t.Fatalf("want only the signed thought, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Type != "thought" || blocks[0].Signature != "sig-1" || blocks[0].Thinking != "weighing" {
		t.Errorf("thought not carried verbatim: %+v", blocks[0])
	}
}

func TestParseGeminiThoughtBodyAcceptsStepSignature(t *testing.T) {
	blocks := client.ParseGeminiThoughtBody([]byte(`{"candidates":[{"content":{"parts":[
		{"text":"step","thought":true,"signature":"sig-2"}
	]}}]}`))
	if len(blocks) != 1 || blocks[0].Signature != "sig-2" {
		t.Errorf("step-level signature spelling not accepted: %+v", blocks)
	}
	if got := client.ParseGeminiThoughtBody([]byte("garbage")); got != nil {
		t.Errorf("unparseable body must clear, got %+v", got)
	}
}

func TestGeminiReplaysThoughtOnModelTurn(t *testing.T) {
	c := NewGeminiClient(nil, "gemini-3-pro", zap.NewNop(), 1, 0)
	contents, _, _ := c.buildContentsAndSystem([]models.Message{
		{Role: "user", Content: "go"},
		{
			Role:     "assistant",
			Content:  "done",
			Thinking: []models.ThinkingBlock{{Type: "thought", Thinking: "weighing", Signature: "sig-1"}},
		},
	}, "")
	raw, err := json.Marshal(contents)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []struct {
		Role  string           `json:"role"`
		Parts []map[string]any `json:"parts"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, turn := range decoded {
		if turn.Role != "model" {
			continue
		}
		if len(turn.Parts) < 2 {
			t.Fatalf("want thought + text, got %s", raw)
		}
		if turn.Parts[0]["thought"] != true || turn.Parts[0]["thoughtSignature"] != "sig-1" {
			t.Fatalf("thought must lead the model turn: %s", raw)
		}
		return
	}
	t.Fatal("no model turn in the payload")
}

func TestGeminiTurnWithoutThoughtIsUnchanged(t *testing.T) {
	c := NewGeminiClient(nil, "gemini-3-pro", zap.NewNop(), 1, 0)
	with, _, _ := c.buildContentsAndSystem([]models.Message{{Role: "assistant", Content: "done"}}, "")
	a, _ := json.Marshal(with)
	if string(a) == "" {
		t.Fatal("empty payload")
	}
	if got := string(a); got == "" || !json.Valid(a) {
		t.Fatalf("invalid payload: %s", a)
	}
	c.storeThinking(nil)
	if c.LastThinking() != nil {
		t.Error("cleared state must report no blocks")
	}
}
