/*
 * ChatCLI - Context Overflow Recovery
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Provides recovery strategies when API calls fail due to context window overflow
 * (prompt too long) or max output token limits being hit.
 *
 * Recovery strategy for context overflow:
 *   1. Aggressive tool result budget enforcement (halved limits)
 *   2. Tool result pairing cleanup
 *   3. Emergency history truncation (keep system + last N messages)
 *
 * Recovery strategy for max output tokens:
 *   1. Escalate max_tokens (double, up to provider cap)
 *   2. Inject continuation message
 *   3. Track escalation count to prevent infinite loops
 */
package agent

import (
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// ContextRecoveryConfig controls recovery behavior.
type ContextRecoveryConfig struct {
	// MaxRecoveryAttempts is the maximum number of context-too-long recoveries per session.
	MaxRecoveryAttempts int

	// MaxTokenEscalations is the maximum number of max_tokens escalations per session.
	MaxTokenEscalations int

	// EmergencyKeepMessages is how many recent messages to keep during emergency truncation.
	// System messages are always preserved.
	EmergencyKeepMessages int

	// AggressiveBudgetRatio reduces the tool result budget to this fraction during recovery.
	// 0.5 means half the normal budget.
	AggressiveBudgetRatio float64
}

// DefaultContextRecoveryConfig returns the default recovery configuration.
func DefaultContextRecoveryConfig() ContextRecoveryConfig {
	cfg := ContextRecoveryConfig{
		MaxRecoveryAttempts:   3,
		MaxTokenEscalations:   2,
		EmergencyKeepMessages: 10,
		AggressiveBudgetRatio: 0.5,
	}

	if v := os.Getenv("CHATCLI_MAX_RECOVERY_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxRecoveryAttempts = n
		}
	}
	if v := os.Getenv("CHATCLI_MAX_TOKEN_ESCALATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxTokenEscalations = n
		}
	}
	if v := os.Getenv("CHATCLI_EMERGENCY_KEEP_MESSAGES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.EmergencyKeepMessages = n
		}
	}

	return cfg
}

// ContextRecovery manages recovery state across a session.
type ContextRecovery struct {
	config           ContextRecoveryConfig
	recoveryAttempts int
	escalationCount  int
	logger           *zap.Logger
}

// NewContextRecovery creates a recovery manager.
func NewContextRecovery(config ContextRecoveryConfig, logger *zap.Logger) *ContextRecovery {
	return &ContextRecovery{
		config: config,
		logger: logger,
	}
}

// CanRecoverContextOverflow returns true if more recovery attempts are available.
func (cr *ContextRecovery) CanRecoverContextOverflow() bool {
	return cr.recoveryAttempts < cr.config.MaxRecoveryAttempts
}

// CanEscalateMaxTokens returns true if more escalation attempts are available.
func (cr *ContextRecovery) CanEscalateMaxTokens() bool {
	return cr.escalationCount < cr.config.MaxTokenEscalations
}

// RecoverContextOverflow applies increasingly aggressive recovery strategies
// to reduce the conversation history size.
//
// Strategy progression:
//  1. First attempt: aggressive budget enforcement + pairing cleanup
//  2. Second attempt: emergency truncation (keep only recent messages)
//  3. Third attempt: nuclear truncation (keep only system + last 2 messages)
//
// Returns the recovered history and true if recovery was applied.
func (cr *ContextRecovery) RecoverContextOverflow(history []models.Message) ([]models.Message, bool) {
	if !cr.CanRecoverContextOverflow() {
		return history, false
	}

	cr.recoveryAttempts++
	attempt := cr.recoveryAttempts

	cr.logger.Warn("Attempting context overflow recovery",
		zap.Int("attempt", attempt),
		zap.Int("max_attempts", cr.config.MaxRecoveryAttempts),
		zap.Int("history_size", len(history)))

	switch attempt {
	case 1:
		// Level 1: Aggressive tool result budget + pairing cleanup
		return cr.level1Recovery(history), true
	case 2:
		// Level 2: Emergency truncation - keep system + last N messages
		return cr.level2Recovery(history), true
	default:
		// Level 3+: Nuclear - keep system + last 2 exchanges
		return cr.level3Recovery(history), true
	}
}

// level1Recovery applies aggressive budget enforcement.
func (cr *ContextRecovery) level1Recovery(history []models.Message) []models.Message {
	cr.logger.Info("Context recovery level 1: aggressive budget enforcement")

	// Step 1: Tool result pairing cleanup
	history, _ = EnsureToolResultPairing(history, cr.logger)

	// Step 2: Halve the budget limits
	origTurnBudget := DefaultTurnBudgetChars
	origPerResult := DefaultPerResultMaxChars
	DefaultTurnBudgetChars = int(float64(origTurnBudget) * cr.config.AggressiveBudgetRatio)
	DefaultPerResultMaxChars = int(float64(origPerResult) * cr.config.AggressiveBudgetRatio)

	history, _ = EnforceToolResultBudget(history, cr.logger)

	// Restore original limits
	DefaultTurnBudgetChars = origTurnBudget
	DefaultPerResultMaxChars = origPerResult

	// Step 3: Truncate large assistant messages (reasoning, explanations)
	for i := range history {
		if history[i].Role == "assistant" && len(history[i].Content) > 5000 {
			history[i].Content = truncatePreservingEnd(history[i].Content, 5000)
		}
	}

	return history
}

// level2Recovery keeps system messages + the last N messages.
func (cr *ContextRecovery) level2Recovery(history []models.Message) []models.Message {
	cr.logger.Info("Context recovery level 2: emergency truncation",
		zap.Int("keep_messages", cr.config.EmergencyKeepMessages))

	return emergencyTruncate(history, cr.config.EmergencyKeepMessages)
}

// level3Recovery keeps only system messages + last 2 user-assistant exchanges.
func (cr *ContextRecovery) level3Recovery(history []models.Message) []models.Message {
	cr.logger.Info("Context recovery level 3: nuclear truncation")
	return emergencyTruncate(history, 4) // system + 2 exchanges (user+assistant each)
}

// emergencyTruncate preserves system messages and the last N non-system messages.
func emergencyTruncate(history []models.Message, keepLast int) []models.Message {
	var systemMsgs []models.Message
	var nonSystemMsgs []models.Message

	for _, msg := range history {
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		} else {
			nonSystemMsgs = append(nonSystemMsgs, msg)
		}
	}

	// Keep the last N non-system messages
	start := len(nonSystemMsgs) - keepLast
	if start < 0 {
		start = 0
	}
	kept := nonSystemMsgs[start:]

	// Ensure the kept messages start with a user message (API requirement)
	for len(kept) > 0 && kept[0].Role != "user" {
		kept = kept[1:]
	}

	// Inject a context recovery notice
	recoveryNotice := models.Message{
		Role: "user",
		Content: "[Context was automatically compacted due to size limits. " +
			"Previous conversation history has been summarized. " +
			"Continue from where you left off.]",
	}

	result := make([]models.Message, 0, len(systemMsgs)+1+len(kept))
	result = append(result, systemMsgs...)
	if len(kept) == 0 || kept[0].Role != "user" {
		result = append(result, recoveryNotice)
	}
	result = append(result, kept...)

	// Validate tool result pairing in the truncated history
	result, _ = EnsureToolResultPairing(result, nil)

	return result
}

// MaxTokensEscalation computes the escalated max_tokens value.
// Returns (newMaxTokens, shouldEscalate).
func (cr *ContextRecovery) MaxTokensEscalation(currentMaxTokens, providerCap int) (int, bool) {
	if !cr.CanEscalateMaxTokens() {
		return currentMaxTokens, false
	}

	cr.escalationCount++

	// Double the current max, capped at provider limit
	escalated := currentMaxTokens * 2
	if escalated > providerCap {
		escalated = providerCap
	}
	if escalated <= currentMaxTokens {
		return currentMaxTokens, false
	}

	cr.logger.Info("Escalating max output tokens",
		zap.Int("previous", currentMaxTokens),
		zap.Int("escalated", escalated),
		zap.Int("provider_cap", providerCap),
		zap.Int("escalation_count", cr.escalationCount))

	return escalated, true
}

// ContinuationMessage returns the message to inject when max_tokens is hit.
func ContinuationMessage() models.Message {
	return models.Message{
		Role: "user",
		Content: "Your response was cut off at the token limit. " +
			"Resume DIRECTLY from where you stopped — do not repeat any content. " +
			"Continue the implementation or explanation from the exact point of interruption.",
	}
}

// IsContextTooLongError checks if an error is a context overflow error.
func IsContextTooLongError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context length") ||
		strings.Contains(msg, "too long") ||
		strings.Contains(msg, "maximum context") ||
		strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "request too large") ||
		strings.Contains(msg, "max_tokens") && strings.Contains(msg, "exceed") ||
		strings.Contains(msg, "input too long") ||
		strings.Contains(msg, "token limit")
}

// truncatePreservingEnd truncates text to maxLen, keeping the end portion.
func truncatePreservingEnd(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	headLen := maxLen * 3 / 4
	tailLen := maxLen - headLen - 50 // 50 for the separator
	if tailLen < 0 {
		tailLen = 0
	}

	head := text[:headLen]
	tail := text[len(text)-tailLen:]

	return head + "\n... [truncated] ...\n" + tail
}
