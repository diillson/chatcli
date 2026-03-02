package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// MessageTrimmer performs near-lossless trimming of conversation messages
// to reduce token usage without losing semantic information.
type MessageTrimmer struct {
	logger *zap.Logger
}

// NewMessageTrimmer creates a new MessageTrimmer.
func NewMessageTrimmer(logger *zap.Logger) *MessageTrimmer {
	return &MessageTrimmer{logger: logger}
}

// Regex patterns for extracting/stripping content.
var (
	reasoningBlockRe   = regexp.MustCompile(`(?s)<reasoning>.*?</reasoning>`)
	explanationBlockRe = regexp.MustCompile(`(?s)<explanation>.*?</explanation>`)
	toolCallRe         = regexp.MustCompile(`(?s)<tool_call\s+name="([^"]*?)"\s+args='([^']*?)'\s*/>`)
	toolCallAltRe      = regexp.MustCompile(`(?s)<tool_call\s+name="([^"]*?)"\s+args="([^"]*?)"\s*/>`)
	toolOutputBlockRe  = regexp.MustCompile(`(?s)<tool_output>\n?(.*?)\n?</tool_output>`)
	// Pattern to detect tool feedback messages (from agent_mode.go format)
	toolFeedbackPrefixRe = regexp.MustCompile(`^(?:The tool '|A ferramenta '|--- Resultado da Ação)`)
	formatErrorPrefixRe  = regexp.MustCompile(`^FORMAT ERROR:`)
)

// TrimHistory performs near-lossless trimming on all messages in the history.
// It preserves system messages, user-authored messages, and summary messages intact.
func (t *MessageTrimmer) TrimHistory(history []models.Message) []models.Message {
	if len(history) == 0 {
		return history
	}

	result := make([]models.Message, len(history))
	for i, msg := range history {
		result[i] = t.trimMessage(msg, history, i)
	}

	// Second pass: deduplicate consecutive FORMAT ERROR messages (keep only the last)
	result = t.deduplicateFormatErrors(result)

	return result
}

// trimMessage trims a single message based on its role and content type.
func (t *MessageTrimmer) trimMessage(msg models.Message, history []models.Message, idx int) models.Message {
	// Never touch system messages
	if msg.Role == "system" {
		return msg
	}

	// Never touch summary messages
	if msg.Meta != nil && msg.Meta.IsSummary {
		return msg
	}

	trimmed := msg

	switch msg.Role {
	case "assistant":
		trimmed.Content = t.trimAssistantMessage(msg.Content)
	case "user":
		if isToolFeedbackMessage(msg.Content) {
			trimmed.Content = t.trimToolFeedback(msg.Content)
		}
		// Real user messages are never trimmed
	}

	return trimmed
}

// trimAssistantMessage removes verbose parts from assistant responses.
func (t *MessageTrimmer) trimAssistantMessage(content string) string {
	original := content

	// Strip <reasoning> blocks — intermediate reasoning doesn't need to be re-sent
	content = reasoningBlockRe.ReplaceAllString(content, "")

	// Strip <explanation> blocks
	content = explanationBlockRe.ReplaceAllString(content, "")

	// Compact tool_call XML into short references
	content = t.compactToolCalls(content)

	// Clean up excessive whitespace left by removals
	content = cleanExcessiveWhitespace(content)

	if len(content) < len(original) {
		t.logger.Debug("Trimmed assistant message",
			zap.Int("before", len(original)),
			zap.Int("after", len(content)),
		)
	}

	return content
}

// compactToolCalls replaces verbose <tool_call> XML with compact references.
// Example: <tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />
// becomes: [tool: @coder read main.go]
func (t *MessageTrimmer) compactToolCalls(content string) string {
	// Handle single-quoted args
	content = toolCallRe.ReplaceAllStringFunc(content, func(match string) string {
		return compactSingleToolCall(match, toolCallRe)
	})
	// Handle double-quoted args
	content = toolCallAltRe.ReplaceAllStringFunc(content, func(match string) string {
		return compactSingleToolCall(match, toolCallAltRe)
	})
	return content
}

// compactSingleToolCall converts a tool_call match into a compact reference.
func compactSingleToolCall(match string, re *regexp.Regexp) string {
	groups := re.FindStringSubmatch(match)
	if len(groups) < 3 {
		return match
	}
	toolName := groups[1]
	argsStr := groups[2]

	// Try to extract cmd and key arg for a readable compact form
	cmd := extractJSONField(argsStr, "cmd")
	file := extractJSONField(argsStr, "file")
	term := extractJSONField(argsStr, "term")
	cmdExec := extractJSONField(argsStr, "cmd") // for exec commands nested in args

	var compact string
	switch {
	case file != "":
		compact = fmt.Sprintf("[tool: %s %s %s]", toolName, cmd, file)
	case term != "":
		compact = fmt.Sprintf("[tool: %s %s term=%q]", toolName, cmd, truncateStr(term, 40))
	case cmd != "" && cmdExec != "":
		compact = fmt.Sprintf("[tool: %s %s]", toolName, truncateStr(cmd, 60))
	default:
		compact = fmt.Sprintf("[tool: %s %s]", toolName, truncateStr(argsStr, 60))
	}
	return compact
}

// extractJSONField extracts a simple string field from a JSON-like string.
// This is a lightweight extraction that avoids full JSON parsing for performance.
func extractJSONField(jsonStr, field string) string {
	// Match "field":"value" or "field": "value"
	pattern := fmt.Sprintf(`"%s"\s*:\s*"([^"]*)"`, regexp.QuoteMeta(field))
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(jsonStr)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// trimToolFeedback trims verbose tool output from feedback messages.
func (t *MessageTrimmer) trimToolFeedback(content string) string {
	// Truncate large <tool_output> blocks
	content = toolOutputBlockRe.ReplaceAllStringFunc(content, func(match string) string {
		groups := toolOutputBlockRe.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		output := groups[1]
		if len(output) <= 5000 {
			return match
		}
		// Keep first 2000 chars + last 500 chars
		truncated := output[:2000] +
			fmt.Sprintf("\n\n... [%d chars omitted] ...\n\n", len(output)-2500) +
			output[len(output)-500:]
		return "<tool_output>\n" + truncated + "\n</tool_output>"
	})

	// If content doesn't have <tool_output> tags but is still huge (raw output),
	// truncate the overall message
	if len(content) > 8000 && !strings.Contains(content, "<tool_output>") {
		// Looks for "--- Resultado da Ação" pattern (batch output)
		// Keep header + first portion + tail
		content = truncatePreservingStructure(content, 8000)
	}

	return content
}

// deduplicateFormatErrors keeps only the last FORMAT ERROR message in a consecutive run.
func (t *MessageTrimmer) deduplicateFormatErrors(history []models.Message) []models.Message {
	if len(history) <= 1 {
		return history
	}

	result := make([]models.Message, 0, len(history))
	for i, msg := range history {
		if msg.Role == "user" && formatErrorPrefixRe.MatchString(msg.Content) {
			// Check if the NEXT message is also a FORMAT ERROR
			if i+1 < len(history) {
				next := history[i+1]
				if next.Role == "user" && formatErrorPrefixRe.MatchString(next.Content) {
					// Skip this one — a newer FORMAT ERROR follows
					continue
				}
			}
		}
		result = append(result, msg)
	}

	if len(result) < len(history) {
		t.logger.Debug("Deduplicated FORMAT ERROR messages",
			zap.Int("removed", len(history)-len(result)),
		)
	}

	return result
}

// isToolFeedbackMessage detects messages that are tool output feedback (not real user input).
func isToolFeedbackMessage(content string) bool {
	return toolFeedbackPrefixRe.MatchString(content) ||
		formatErrorPrefixRe.MatchString(content) ||
		strings.Contains(content, "<tool_output>") ||
		strings.HasPrefix(content, "--- Resultado da Ação")
}

// truncatePreservingStructure truncates long text keeping beginning and end,
// preserving any section headers.
func truncatePreservingStructure(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	headLen := maxLen * 2 / 3
	tailLen := maxLen / 3
	return s[:headLen] +
		fmt.Sprintf("\n\n... [%d chars omitted for context efficiency] ...\n\n", len(s)-headLen-tailLen) +
		s[len(s)-tailLen:]
}

// cleanExcessiveWhitespace collapses runs of 3+ newlines into 2.
func cleanExcessiveWhitespace(s string) string {
	multiNewline := regexp.MustCompile(`\n{3,}`)
	return strings.TrimSpace(multiNewline.ReplaceAllString(s, "\n\n"))
}

// truncateStr truncates a string to maxLen characters, adding "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
