package agent

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// microcompactHistory builds a history where the first tool result is
// `age` turns old relative to the last assistant message.
func microcompactHistory(toolContent string, age int) []models.Message {
	h := []models.Message{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "reading"},
		{Role: "tool", Content: toolContent, ToolCallID: "tc-1"},
	}
	for i := 0; i < age; i++ {
		h = append(h,
			models.Message{Role: "user", Content: "next"},
			models.Message{Role: "assistant", Content: "ok"},
		)
	}
	return h
}

func TestApplyMicrocompact_TruncatePreservesContentViaCCR(t *testing.T) {
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore()})
	cfg := DefaultMicrocompactConfig()
	cfg.CCR = layer

	original := strings.Repeat("line of important tool output\n", 300)
	history := microcompactHistory(original, cfg.TurnsBeforeTruncate)

	got, report := ApplyMicrocompact(history, cfg.TurnsBeforeTruncate, cfg, zap.NewNop())
	if report.Truncated != 1 {
		t.Fatalf("expected 1 truncation, got %+v", report)
	}

	keys := compress.ExtractKeys(got[2].Content)
	if len(keys) != 1 {
		t.Fatalf("truncated stub must carry exactly one recall marker, got %d in %q", len(keys), got[2].Content)
	}
	recovered, ok := layer.Recall(keys[0])
	if !ok || recovered != original {
		t.Error("recall via the embedded marker must return the original tool result verbatim")
	}
}

func TestApplyMicrocompact_SummarizeCarriesEarlierMarker(t *testing.T) {
	// A result truncated at level 1 gains a marker; when level 2 later
	// replaces the whole content with a one-line summary, that marker must
	// survive on the summary — otherwise recoverability is lost exactly
	// when the most bytes are dropped.
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore()})
	cfg := DefaultMicrocompactConfig()
	cfg.CCR = layer
	// Keep the level-1 preview above MinContentSize so the same message
	// later qualifies for level-2 summarization (with default knobs the
	// preview drops below the minimum and level 2 never fires).
	cfg.TruncateHeadChars = 4000
	cfg.TruncateTailChars = 1000

	original := strings.Repeat("payload row 12345\n", 400)
	history := microcompactHistory(original, cfg.TurnsBeforeTruncate)

	// Level 1 pass.
	history, _ = ApplyMicrocompact(history, cfg.TurnsBeforeTruncate, cfg, zap.NewNop())
	keysAfterTruncate := compress.ExtractKeys(history[2].Content)
	if len(keysAfterTruncate) != 1 {
		t.Fatalf("level 1 must leave one marker, got %d", len(keysAfterTruncate))
	}

	// Age the conversation to level 2 and re-run.
	for i := 0; i < cfg.TurnsBeforeSummarize; i++ {
		history = append(history,
			models.Message{Role: "user", Content: "next"},
			models.Message{Role: "assistant", Content: "ok"},
		)
	}
	history, report := ApplyMicrocompact(history, cfg.TurnsBeforeSummarize+2, cfg, zap.NewNop())
	if report.Summarized != 1 {
		t.Fatalf("expected 1 summarization, got %+v", report)
	}

	keysAfterSummary := compress.ExtractKeys(history[2].Content)
	if len(keysAfterSummary) != 1 || keysAfterSummary[0] != keysAfterTruncate[0] {
		t.Fatalf("summary must carry over the level-1 marker %q, got %v", keysAfterTruncate[0], keysAfterSummary)
	}
	recovered, ok := layer.Recall(keysAfterSummary[0])
	if !ok || recovered != original {
		t.Error("the original content must remain recoverable after summarization")
	}
}

func TestApplyMicrocompact_NilCCRKeepsLegacyBehavior(t *testing.T) {
	cfg := DefaultMicrocompactConfig()
	original := strings.Repeat("x", cfg.MinContentSize*3)
	history := microcompactHistory(original, cfg.TurnsBeforeTruncate)

	got, report := ApplyMicrocompact(history, cfg.TurnsBeforeTruncate, cfg, zap.NewNop())
	if report.Truncated != 1 {
		t.Fatalf("expected 1 truncation, got %+v", report)
	}
	if keys := compress.ExtractKeys(got[2].Content); len(keys) != 0 {
		t.Error("without a CCR layer no marker must be emitted")
	}
}

func TestApplyMicrocompact_AgentFeedbackAged(t *testing.T) {
	// Squad results are injected as a user-role message flagged AgentFeedback;
	// microcompact must age them like tool results.
	layer := compress.NewLayer(compress.Config{Mode: compress.ModeLossyWithCCR, Store: compress.NewMemoryStore()})
	cfg := DefaultMicrocompactConfig()
	cfg.CCR = layer

	original := "--- Agent Results ---\n\n" + strings.Repeat("[coder] finding line\n", 400)
	h := []models.Message{
		{Role: "user", Content: "do the task"},
		{Role: "assistant", Content: "dispatching"},
		{Role: "user", Content: original, Meta: &models.MessageMeta{AgentFeedback: true}},
	}
	for i := 0; i < cfg.TurnsBeforeTruncate; i++ {
		h = append(h,
			models.Message{Role: "user", Content: "next"},
			models.Message{Role: "assistant", Content: "ok"},
		)
	}

	got, report := ApplyMicrocompact(h, cfg.TurnsBeforeTruncate, cfg, zap.NewNop())
	if report.Truncated != 1 {
		t.Fatalf("agent feedback must be truncated at level 1, got %+v", report)
	}
	keys := compress.ExtractKeys(got[2].Content)
	if len(keys) != 1 {
		t.Fatalf("stub must carry a recall marker, got %q", got[2].Content)
	}
	if recovered, ok := layer.Recall(keys[0]); !ok || recovered != original {
		t.Error("original squad feedback must be recoverable via the marker")
	}
}

func TestApplyMicrocompact_PlainUserMessageUntouched(t *testing.T) {
	cfg := DefaultMicrocompactConfig()
	long := strings.Repeat("user context the model must keep seeing\n", 400)
	h := []models.Message{
		{Role: "user", Content: long},
		{Role: "assistant", Content: "ok"},
	}
	for i := 0; i < cfg.TurnsBeforeSummarize; i++ {
		h = append(h,
			models.Message{Role: "user", Content: "next"},
			models.Message{Role: "assistant", Content: "ok"},
		)
	}
	got, report := ApplyMicrocompact(h, cfg.TurnsBeforeSummarize, cfg, zap.NewNop())
	if report.Truncated != 0 || report.Summarized != 0 {
		t.Fatalf("plain user messages must never be compacted, got %+v", report)
	}
	if got[0].Content != long {
		t.Error("plain user message content changed")
	}
}
