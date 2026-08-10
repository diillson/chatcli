package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestMessageWeightContentOnly(t *testing.T) {
	msg := models.Message{Role: "user", Content: "hello world"}
	if got := messageWeight(msg); got != len(msg.Content) {
		t.Fatalf("plain message weight = %d, want %d", got, len(msg.Content))
	}
}

func TestMessageWeightCountsToolCallArguments(t *testing.T) {
	bigArg := strings.Repeat("x", 10_000)
	msg := models.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []models.ToolCall{
			{ID: "tc1", Name: "shell", Arguments: map[string]interface{}{"command": bigArg}},
		},
	}
	got := messageWeight(msg)
	// Weight must at least cover the serialized argument payload; the exact
	// figure includes JSON framing, so assert a floor, not equality.
	if got < len(bigArg) {
		t.Fatalf("tool-call weight = %d, want >= %d (arguments must count)", got, len(bigArg))
	}
	if got <= len(msg.Content) {
		t.Fatalf("tool-call weight = %d must exceed bare content length %d", got, len(msg.Content))
	}
}

func TestMessageWeightCountsImages(t *testing.T) {
	msg := models.Message{
		Role:    "user",
		Content: "see attached",
		Images: []models.ImageContent{
			{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}}, // opaque bytes: dimension-unknown fallback
		},
	}
	base := len(msg.Content)
	got := messageWeight(msg)
	want := base + models.EstimateImageTokens(msg.Images[0])*weightCharsPerToken
	if got != want {
		t.Fatalf("image weight = %d, want %d (EstimateImageTokens x %d)", got, want, weightCharsPerToken)
	}
	if got <= base {
		t.Fatalf("image weight = %d must exceed bare content length %d", got, base)
	}
}

func TestMessageWeightSystemPartsNoDoubleCount(t *testing.T) {
	parts := []models.ContentBlock{
		{Type: "text", Text: "part one"},
		{Type: "text", Text: "part two"},
	}
	flattened := parts[0].Text + "\n\n" + parts[1].Text
	msg := models.Message{Role: "system", Content: flattened, SystemParts: parts}
	// Content is the flattened join of the parts (join separators make it the
	// larger of the two) — parts must not be charged again on top.
	if got := messageWeight(msg); got != len(flattened) {
		t.Fatalf("system-parts weight = %d, want %d (no double count)", got, len(flattened))
	}
}

func TestMessageWeightSystemPartsExcessOnly(t *testing.T) {
	parts := []models.ContentBlock{{Type: "text", Text: strings.Repeat("p", 100)}}
	msg := models.Message{Role: "system", Content: "short", SystemParts: parts}
	want := len(msg.Content) + (100 - len(msg.Content))
	if got := messageWeight(msg); got != want {
		t.Fatalf("parts-exceed-content weight = %d, want %d (charge the excess only)", got, want)
	}
}

func TestTotalCharsSumsWeights(t *testing.T) {
	history := []models.Message{
		{Role: "user", Content: "abc"},
		{Role: "assistant", ToolCalls: []models.ToolCall{
			{ID: "t1", Name: "read", Arguments: map[string]interface{}{"path": "main.go"}},
		}},
	}
	want := messageWeight(history[0]) + messageWeight(history[1])
	if got := totalChars(history); got != want {
		t.Fatalf("totalChars = %d, want %d", got, want)
	}
}

func TestNeedsCompactionTriggersOnToolCallHeavyHistory(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	cfg := DefaultCompactConfig("openai", "gpt-4o-mini")
	budget := hc.CharBudget(cfg)

	// A history whose Content is tiny but whose native tool-call arguments
	// blow past the budget: before messageWeight, this was invisible.
	bigArg := strings.Repeat("y", budget+1)
	history := []models.Message{
		{Role: "user", Content: "run it"},
		{Role: "assistant", ToolCalls: []models.ToolCall{
			{ID: "t1", Name: "shell", Arguments: map[string]interface{}{"command": bigArg}},
		}},
	}
	if !hc.NeedsCompaction(history, cfg) {
		t.Fatal("NeedsCompaction = false for tool-call-heavy history exceeding budget")
	}
}

func TestShrinkToBudgetTerminatesOnUnshrinkableWeight(t *testing.T) {
	// Image-only weight with floor-sized content: nothing is shrinkable, the
	// loop must bail out instead of spinning.
	msg := models.Message{
		Role:    "user",
		Content: "tiny",
		Images: []models.ImageContent{
			{MediaType: "image/png", Data: []byte("not-a-real-image-but-big-enough-to-weigh")},
		},
	}
	history := []models.Message{msg}
	got := shrinkToBudget(history, 1, nil)
	if len(got) != len(history) {
		t.Fatalf("shrinkToBudget changed message count: %d -> %d", len(history), len(got))
	}
	if got[0].Content != msg.Content {
		t.Fatalf("floor-sized content was modified: %q", got[0].Content)
	}
}
