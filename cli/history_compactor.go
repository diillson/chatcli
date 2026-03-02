package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// HistoryCompactor manages conversation history size through a 3-level pipeline:
//   Level 1: Near-lossless trimming (strip reasoning, compact XML, dedup)
//   Level 2: Structured summarization (extract facts, not prose)
//   Level 3: Emergency truncation (last resort)
type HistoryCompactor struct {
	logger  *zap.Logger
	trimmer *MessageTrimmer
}

// CompactConfig holds parameters for a compaction operation.
type CompactConfig struct {
	Provider      string
	Model         string
	BudgetRatio   float64 // fraction of context window to use (default 0.75)
	MinKeepRecent int     // minimum recent messages to keep verbatim (default 10)
	CharsPerToken int     // character-to-token ratio estimate (default 4)
}

// DefaultCompactConfig returns sensible defaults for chat mode.
func DefaultCompactConfig(provider, model string) CompactConfig {
	return CompactConfig{
		Provider:      provider,
		Model:         model,
		BudgetRatio:   0.75,
		MinKeepRecent: 10,
		CharsPerToken: 4,
	}
}

// NewHistoryCompactor creates a new HistoryCompactor with its embedded trimmer.
func NewHistoryCompactor(logger *zap.Logger) *HistoryCompactor {
	return &HistoryCompactor{
		logger:  logger,
		trimmer: NewMessageTrimmer(logger),
	}
}

// CharBudget returns the character budget based on the model's context window.
func (hc *HistoryCompactor) CharBudget(cfg CompactConfig) int {
	contextWindow := catalog.GetContextWindow(cfg.Provider, cfg.Model)
	tokenBudget := int(float64(contextWindow) * cfg.BudgetRatio)
	return tokenBudget * cfg.CharsPerToken
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
	)

	// LEVEL 1: Near-lossless trimming
	history = hc.trimmer.TrimHistory(history)
	current := totalChars(history)
	if current <= budget {
		hc.logger.Info("Level 1 (trim) sufficient",
			zap.Int("before_chars", before),
			zap.Int("after_chars", current),
		)
		return history, nil
	}

	// LEVEL 2: Structured summarization of old messages
	summarized, err := hc.structuredSummarize(ctx, history, llmClient, cfg)
	if err != nil {
		hc.logger.Warn("Level 2 (summarization) failed, falling back",
			zap.Error(err),
		)
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
			return history, nil
		}
	}

	// LEVEL 3: Emergency truncation (last resort)
	history = hc.emergencyTruncate(history, cfg)
	hc.logger.Warn("Level 3 (emergency truncation) used",
		zap.Int("before_chars", before),
		zap.Int("after_chars", totalChars(history)),
		zap.Int("before_msgs", beforeMsgs),
		zap.Int("after_msgs", len(history)),
	)

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

	// Use a timeout to avoid blocking too long
	summarizeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
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

// GenerateModeSummary creates a structured summary of a mode session (agent/coder)
// to be stored as shared memory when transitioning back to chat mode.
func (hc *HistoryCompactor) GenerateModeSummary(
	ctx context.Context,
	history []models.Message,
	llmClient client.LLMClient,
	modeName string,
) string {
	if len(history) <= 2 || llmClient == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Summarize this %s session using the structured format below. ", modeName))
	sb.WriteString("Include ONLY facts from the conversation. Under 300 words.\n\n")
	sb.WriteString(structuredSummaryPrompt)
	sb.WriteString("\n\nSESSION TO SUMMARIZE:\n\n")

	for _, msg := range history {
		if msg.Role == "system" {
			continue
		}
		content := msg.Content
		if len(content) > 1000 {
			content = content[:600] + "\n...[truncated]...\n" + content[len(content)-200:]
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n\n", msg.Role, content))
	}

	summaryHistory := []models.Message{
		{Role: "user", Content: sb.String()},
	}

	summarizeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	response, err := llmClient.SendPrompt(summarizeCtx, sb.String(), summaryHistory, 0)
	if err != nil {
		hc.logger.Warn("Failed to generate mode summary", zap.String("mode", modeName), zap.Error(err))
		return ""
	}

	return fmt.Sprintf("[/%s session summary]\n\n%s", modeName, response)
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
