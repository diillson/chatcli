package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// CompactStage identifies which level of the pipeline is running.
// Used by the UI to render meaningful progress messages instead of a
// generic "Processando..." while the compactor is working.
type CompactStage string

const (
	CompactStageStart     CompactStage = "start"
	CompactStageTrim      CompactStage = "trim"
	CompactStageSummarize CompactStage = "summarize"
	CompactStageEmergency CompactStage = "emergency"
	CompactStageDone      CompactStage = "done"
)

// StatusCallback is invoked by the compactor at the start/end of each level
// so callers can update spinners, status bars, or animation messages. It must
// be non-blocking and safe to call from any goroutine.
type StatusCallback func(stage CompactStage, msg string)

// HistoryCompactor manages conversation history size through a 3-level pipeline:
//
//	Level 1: Near-lossless trimming (strip reasoning, compact XML, dedup)
//	Level 2: Structured summarization (extract facts, not prose)
//	Level 3: Emergency truncation (last resort)
type HistoryCompactor struct {
	logger   *zap.Logger
	trimmer  *MessageTrimmer
	statusMu sync.RWMutex
	onStatus StatusCallback
}

// CompactConfig holds parameters for a compaction operation.
type CompactConfig struct {
	Provider      string
	Model         string
	BudgetRatio   float64 // fraction of context window to use (default 0.75)
	MinKeepRecent int     // minimum recent messages to keep verbatim (default 10)
	CharsPerToken int     // character-to-token ratio estimate (default 4)

	// MaxPayloadBytes caps the serialized request body size in bytes.
	// When > 0, overrides the context-window budget if it would yield
	// a larger payload than the corporate proxy / gateway accepts
	// (many enterprise proxies cap POST bodies at 1-5 MB). 0 disables
	// the cap. Honors env CHATCLI_MAX_PAYLOAD (human-friendly: "5MB",
	// "512KB", "5"=5MB when unit is omitted).
	MaxPayloadBytes int
}

// ParsePayloadSize accepts human-friendly size strings and returns bytes.
// A bare number is interpreted as MB (the most common unit users think
// in for proxy caps). Explicit suffixes: B, KB/K, MB/M, GB/G (case
// insensitive, whitespace tolerated: "5 MB" works). Returns 0 for any
// non-positive or unparseable input.
func ParsePayloadSize(s string) int {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}

	var mult int64 = 1024 * 1024 // default unit: MB
	switch {
	case strings.HasSuffix(s, "GB"):
		s, mult = strings.TrimSuffix(s, "GB"), 1024*1024*1024
	case strings.HasSuffix(s, "MB"):
		s, mult = strings.TrimSuffix(s, "MB"), 1024*1024
	case strings.HasSuffix(s, "KB"):
		s, mult = strings.TrimSuffix(s, "KB"), 1024
	case strings.HasSuffix(s, "G"):
		s, mult = strings.TrimSuffix(s, "G"), 1024*1024*1024
	case strings.HasSuffix(s, "M"):
		s, mult = strings.TrimSuffix(s, "M"), 1024*1024
	case strings.HasSuffix(s, "K"):
		s, mult = strings.TrimSuffix(s, "K"), 1024
	case strings.HasSuffix(s, "B"):
		s, mult = strings.TrimSuffix(s, "B"), 1
	}
	s = strings.TrimSpace(s)

	// Support fractional sizes like "2.5MB"
	if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
		return int(f * float64(mult))
	}
	return 0
}

// FormatPayloadSize returns a human-readable size for display.
func FormatPayloadSize(bytes int) string {
	switch {
	case bytes >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// DefaultCompactConfig returns sensible defaults for chat mode.
func DefaultCompactConfig(provider, model string) CompactConfig {
	cfg := CompactConfig{
		Provider:      provider,
		Model:         model,
		BudgetRatio:   0.75,
		MinKeepRecent: 10,
		CharsPerToken: 4,
	}
	// Human-friendly env: CHATCLI_MAX_PAYLOAD=5MB / 512KB / 2.5MB.
	if v := os.Getenv("CHATCLI_MAX_PAYLOAD"); v != "" {
		if n := ParsePayloadSize(v); n > 0 {
			cfg.MaxPayloadBytes = n
		}
	}
	return cfg
}

// NewHistoryCompactor creates a new HistoryCompactor with its embedded trimmer.
func NewHistoryCompactor(logger *zap.Logger) *HistoryCompactor {
	return &HistoryCompactor{
		logger:  logger,
		trimmer: NewMessageTrimmer(logger),
	}
}

// SetStatusCallback registers a progress callback for UI feedback.
// Pass nil to clear. Safe to call concurrently.
func (hc *HistoryCompactor) SetStatusCallback(cb StatusCallback) {
	hc.statusMu.Lock()
	hc.onStatus = cb
	hc.statusMu.Unlock()
}

// emitStatus invokes the current status callback (if any). Never panics
// if the callback is nil or the caller has cleared it mid-flight.
func (hc *HistoryCompactor) emitStatus(stage CompactStage, msg string) {
	hc.statusMu.RLock()
	cb := hc.onStatus
	hc.statusMu.RUnlock()
	if cb != nil {
		cb(stage, msg)
	}
}

// CharBudget returns the character budget based on the model's context window,
// additionally capped by MaxPayloadBytes if set (corporate-proxy scenarios).
// A safety factor leaves headroom for JSON overhead, system prompt and tools.
func (hc *HistoryCompactor) CharBudget(cfg CompactConfig) int {
	contextWindow := catalog.GetContextWindow(cfg.Provider, cfg.Model)
	tokenBudget := int(float64(contextWindow) * cfg.BudgetRatio)
	budget := tokenBudget * cfg.CharsPerToken

	// Proxy / gateway payload cap. Leave 30% headroom for system prompt,
	// tool definitions and JSON serialization overhead.
	if cfg.MaxPayloadBytes > 0 {
		payloadBudget := int(float64(cfg.MaxPayloadBytes) * 0.7)
		if payloadBudget < budget {
			budget = payloadBudget
		}
	}
	return budget
}

// totalChars sums the character count of all message contents.
func totalChars(history []models.Message) int {
	total := 0
	for _, msg := range history {
		total += len(msg.Content)
	}
	return total
}

// NeedsCompaction returns true if the total character count exceeds the budget.
func (hc *HistoryCompactor) NeedsCompaction(history []models.Message, cfg CompactConfig) bool {
	return totalChars(history) > hc.CharBudget(cfg)
}

// Compact runs the 3-level compaction pipeline.
// Each level is progressively more aggressive. Most of the time, Level 1 (trim) suffices.
func (hc *HistoryCompactor) Compact(
	ctx context.Context,
	history []models.Message,
	llmClient client.LLMClient,
	cfg CompactConfig,
) ([]models.Message, error) {
	budget := hc.CharBudget(cfg)
	before := totalChars(history)
	beforeMsgs := len(history)

	hc.logger.Info("History compaction triggered",
		zap.Int("budget_chars", budget),
		zap.Int("current_chars", before),
		zap.Int("messages", beforeMsgs),
		zap.Int("max_payload_bytes", cfg.MaxPayloadBytes),
	)
	hc.emitStatus(CompactStageStart, i18n.T("compact.status.start",
		beforeMsgs, FormatPayloadSize(before), FormatPayloadSize(budget)))

	// LEVEL 1: Near-lossless trimming — pure Go, no network
	hc.emitStatus(CompactStageTrim, i18n.T("compact.status.trim"))
	history = hc.trimmer.TrimHistory(history)
	current := totalChars(history)
	if current <= budget {
		hc.logger.Info("Level 1 (trim) sufficient",
			zap.Int("before_chars", before),
			zap.Int("after_chars", current),
		)
		hc.emitStatus(CompactStageDone, i18n.T("compact.status.trim_sufficient",
			FormatPayloadSize(before), FormatPayloadSize(current)))
		return history, nil
	}

	// LEVEL 2: Structured summarization of old messages (requires LLM call)
	hc.emitStatus(CompactStageSummarize, i18n.T("compact.status.summarize"))
	summarized, err := hc.structuredSummarize(ctx, history, llmClient, cfg)
	if err != nil {
		// A cancellation from the user should propagate, not silently fall
		// through to emergency truncation — the user's own choice to abort
		// must not mangle their history.
		if ctx.Err() != nil {
			hc.emitStatus(CompactStageDone, i18n.T("compact.status.cancelled"))
			return nil, ctx.Err()
		}
		hc.logger.Warn("Level 2 (summarization) failed, falling back",
			zap.Error(err),
		)
		hc.emitStatus(CompactStageSummarize,
			i18n.T("compact.status.summarize_failed", err))
	} else {
		history = summarized
		current = totalChars(history)
		if current <= budget {
			hc.logger.Info("Level 2 (structured summarization) sufficient",
				zap.Int("before_chars", before),
				zap.Int("after_chars", current),
				zap.Int("before_msgs", beforeMsgs),
				zap.Int("after_msgs", len(history)),
			)
			hc.emitStatus(CompactStageDone, i18n.T("compact.status.summarize_applied",
				beforeMsgs, len(history), FormatPayloadSize(before), FormatPayloadSize(current)))
			return history, nil
		}
	}

	// LEVEL 3: Emergency truncation (last resort)
	hc.emitStatus(CompactStageEmergency, i18n.T("compact.status.emergency"))
	history = hc.emergencyTruncate(history, cfg)
	hc.logger.Warn("Level 3 (emergency truncation) used",
		zap.Int("before_chars", before),
		zap.Int("after_chars", totalChars(history)),
		zap.Int("before_msgs", beforeMsgs),
		zap.Int("after_msgs", len(history)),
	)
	hc.emitStatus(CompactStageDone, i18n.T("compact.status.truncated", beforeMsgs, len(history)))

	return history, nil
}

// structuredSummarize summarizes the "middle" block of messages using
// a structured fact-extraction prompt.
func (hc *HistoryCompactor) structuredSummarize(
	ctx context.Context,
	history []models.Message,
	llmClient client.LLMClient,
	cfg CompactConfig,
) ([]models.Message, error) {
	// Find boundaries: [system messages | middle (to summarize) | recent (keep verbatim)]
	systemEnd := 0
	for i, msg := range history {
		if msg.Role == "system" && i == systemEnd {
			systemEnd = i + 1
		} else {
			break
		}
	}

	recentStart := len(history) - cfg.MinKeepRecent
	if recentStart <= systemEnd {
		// Not enough messages to split — nothing to summarize
		return history, nil
	}

	middleMessages := history[systemEnd:recentStart]
	if len(middleMessages) < 4 {
		return history, nil
	}

	// Build input for the summarizer
	var sb strings.Builder
	for _, msg := range middleMessages {
		content := msg.Content
		if len(content) > 2000 {
			content = content[:1500] + "\n... [truncated for summarization] ...\n" + content[len(content)-300:]
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, content))
	}

	prompt := structuredSummaryPrompt + "\n\nCONVERSATION SEGMENT TO EXTRACT FROM:\n\n" + sb.String()

	summaryHistory := []models.Message{
		{Role: "user", Content: prompt},
	}

	// Derive from parent ctx so that a user-initiated cancel (Ctrl+C / ESC)
	// propagates and aborts the long summarization. We add our OWN generous
	// timeout (10 min) to protect against ambient turn-level deadlines that
	// might be shorter than the summary LLM call.
	summarizeCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	response, err := llmClient.SendPrompt(summarizeCtx, prompt, summaryHistory, 0)
	if err != nil {
		return nil, fmt.Errorf("structured summarization LLM call failed: %w", err)
	}

	// Reconstruct: system + summary message + recent messages
	result := make([]models.Message, 0, systemEnd+1+cfg.MinKeepRecent)
	result = append(result, history[:systemEnd]...)
	result = append(result, models.Message{
		Role:    "user",
		Content: fmt.Sprintf("[STRUCTURED SUMMARY — covering %d earlier messages]\n\n%s", len(middleMessages), response),
		Meta: &models.MessageMeta{
			IsSummary: true,
			SummaryOf: len(middleMessages),
		},
	})
	result = append(result, history[recentStart:]...)

	return result, nil
}

// emergencyTruncate is the last resort: drops middle messages without summarization.
func (hc *HistoryCompactor) emergencyTruncate(history []models.Message, cfg CompactConfig) []models.Message {
	systemEnd := 0
	for i, msg := range history {
		if msg.Role == "system" && i == systemEnd {
			systemEnd = i + 1
		} else {
			break
		}
	}

	keepRecent := cfg.MinKeepRecent
	if keepRecent > len(history)-systemEnd {
		keepRecent = len(history) - systemEnd
	}

	recentStart := len(history) - keepRecent
	if recentStart <= systemEnd {
		return history
	}

	droppedCount := recentStart - systemEnd

	result := make([]models.Message, 0, systemEnd+1+keepRecent)
	result = append(result, history[:systemEnd]...)
	result = append(result, models.Message{
		Role:    "user",
		Content: fmt.Sprintf("[CONTEXT TRUNCATED: %d messages removed due to context window limit. Recent context preserved below.]", droppedCount),
		Meta: &models.MessageMeta{
			IsSummary: true,
			SummaryOf: droppedCount,
		},
	})
	result = append(result, history[recentStart:]...)

	return result
}

// structuredSummaryPrompt is the prompt template for fact extraction.
// Written in English for best model performance across all providers.
const structuredSummaryPrompt = `You are a precise technical note-taker. Extract ONLY factual information from this conversation segment.

OUTPUT FORMAT (use exactly this structure, omit empty sections):

## Files Read
- <path> (<line count> lines) - <one-line description of content/purpose>

## Files Modified
- <path>:<lines> - <exact description of what was changed and why>

## Commands Executed
- <command> → <outcome (success/failure + key output)>

## Key Decisions
- <decision made and rationale>

## Errors & Resolutions
- <error> → <how it was resolved>

## Current Task State
- Done: <what's completed>
- Pending: <what remains>

RULES:
- Include EXACT file paths and line numbers — never paraphrase paths
- Include EXACT error messages (first line only)
- Do NOT paraphrase code — reference by file:line
- Do NOT add information that is not explicitly in the conversation
- If a file was read and then modified, show both entries
- Keep each bullet to ONE line
- If nothing fits a section, omit that section entirely
- Do NOT use code blocks or XML tags in the output`
