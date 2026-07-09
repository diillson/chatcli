package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestHistoryContainsPayloadHint(t *testing.T) {
	base := []models.Message{
		{Role: "system", Content: "agent charter"},
		{Role: "user", Content: "read the big file"},
	}
	if historyContainsPayloadHint(base) {
		t.Error("must be false when no hint is present")
	}

	withHint := append(append([]models.Message{}, base...), models.Message{
		Role:    "user",
		Content: payloadRecoveryHintMarker + " A proxy/gateway rejected the previous request.",
	})
	if !historyContainsPayloadHint(withHint) {
		t.Error("must detect the hint appended as its own message")
	}

	// Compaction may fold the hint into a summary message — content match,
	// not position/prefix match, must still find it.
	folded := []models.Message{
		{Role: "system", Content: "agent charter"},
		{Role: "user", Content: "[STRUCTURED SUMMARY]\nearlier: " + payloadRecoveryHintMarker + " ...\nmore facts"},
	}
	if !historyContainsPayloadHint(folded) {
		t.Error("must detect the hint folded inside a summary message")
	}
}

func TestShrinkToBudget_ReducesShortOversizedHistory(t *testing.T) {
	// Regression: agent-mode histories are short (system + a few huge tool
	// results). emergencyTruncate cannot drop anything, so before the fix
	// Level 3 was a no-op and the oversized request was resent verbatim to
	// the proxy/WAF that had just rejected it.
	system := models.Message{Role: "system", Content: strings.Repeat("s", 10_000)}
	history := []models.Message{
		system,
		{Role: "user", Content: "analyze the incidents"},
		{Role: "assistant", Content: strings.Repeat("a", 30_000)},
		{Role: "user", Content: strings.Repeat("t", 80_000)}, // huge tool result
	}

	budget := 50_000
	got := shrinkToBudget(history, budget)

	if total := totalChars(got); total > budget {
		t.Errorf("total after shrink = %d, want <= %d", total, budget)
	}
	if got[0].Content != system.Content {
		t.Error("system message must never be truncated")
	}
	if len(got) != len(history) {
		t.Errorf("shrink must not drop messages: got %d, want %d", len(got), len(history))
	}
	// The caller's slice must not be mutated (copy-on-write contract).
	if len(history[3].Content) != 80_000 {
		t.Error("input history must not be mutated")
	}
}

func TestShrinkToBudget_SystemLargerThanBudgetStopsAtFloor(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: strings.Repeat("s", 100_000)},
		{Role: "user", Content: strings.Repeat("u", 50_000)},
	}

	got := shrinkToBudget(history, 60_000) // budget below the system size

	if got[0].Content != history[0].Content {
		t.Error("system message must never be truncated even when it alone exceeds the budget")
	}
	if len(got[1].Content) >= 50_000 {
		t.Error("non-system content must still be shrunk toward the floor")
	}
}

func TestShrinkToBudget_NoopWhenWithinBudget(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "small"},
		{Role: "user", Content: "also small"},
	}
	got := shrinkToBudget(history, 10_000)
	if got[1].Content != "also small" {
		t.Error("within-budget history must be returned unchanged")
	}
}
