package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/textfold"
	"go.uber.org/zap"
)

// MessageTrimmer performs near-lossless trimming of conversation messages
// to reduce token usage without losing semantic information.
type MessageTrimmer struct {
	logger *zap.Logger
	// comp, when set and enabled, replaces the byte-level head/tail truncation
	// of oversized tool feedback and injected context with content-aware,
	// reversible compression (CCR). It engages during compaction across all
	// modes — including chat, where bulky attached context is the main target.
	comp *compress.Layer
}

// NewMessageTrimmer creates a new MessageTrimmer.
func NewMessageTrimmer(logger *zap.Logger) *MessageTrimmer {
	return &MessageTrimmer{logger: logger}
}

// SetCompressionLayer wires the content-aware compression layer (optional).
func (t *MessageTrimmer) SetCompressionLayer(l *compress.Layer) { t.comp = l }

// compressOrEmpty returns a content-aware, reversible reduction of s when the
// compression layer is active and actually shrinks it; otherwise "" so the
// caller keeps its existing byte-level fallback. Already-CCR-compressed content
// is left untouched (idempotent across repeated compaction passes).
func (t *MessageTrimmer) compressOrEmpty(s string) string {
	if t.comp == nil || !t.comp.Enabled() {
		return ""
	}
	if compress.ExtractKeys(s) != nil {
		return ""
	}
	out, res := t.comp.CompressToolOutput("", s)
	if res.CompressedSize < res.OriginalSize {
		return out
	}
	return ""
}

// Regex patterns for extracting/stripping content.
var (
	reasoningBlockRe   = regexp.MustCompile(`(?s)<plan>.*?</plan>|<reasoning>.*?</reasoning>`)
	explanationBlockRe = regexp.MustCompile(`(?s)<explanation>.*?</explanation>`)
	toolOutputBlockRe  = regexp.MustCompile(`(?s)<tool_output>\n?(.*?)\n?</tool_output>`)
	// Pattern to detect tool feedback messages (from agent_mode.go format)
	toolFeedbackPrefixRe = regexp.MustCompile(`^(?:The tool '|A ferramenta '|--- Action Result|--- Resultado da Ação)`)
	formatErrorPrefixRe  = regexp.MustCompile(`^FORMAT ERROR:`)
	// Patterns to detect injected context/memory messages
	memoryContextMarkerRe   = regexp.MustCompile(`^\[MEMORY CONTEXT — loaded from \d{4}-\d{2}-\d{2}\]`)
	attachedContextMarkerRe = regexp.MustCompile(`(?m)^📦 CONTEX?TO?:`)
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

	// Never reduce messages explicitly marked verbatim. This is how @recall
	// output survives compaction: the producer flags it structurally
	// (MessageMeta.PreserveVerbatim) so the trimmer needs no knowledge of the
	// tool-output text format. Re-trimming it would discard the original the
	// model asked to see in full and force another recall.
	if msg.Meta != nil && msg.Meta.PreserveVerbatim {
		return msg
	}

	trimmed := msg

	switch msg.Role {
	case "assistant":
		trimmed.Content = t.trimAssistantMessage(msg.Content)
	case "user":
		if isInjectedContextMessage(msg.Content) {
			trimmed.Content = t.trimInjectedContext(msg.Content)
		} else if isToolFeedbackMessage(msg.Content) {
			trimmed.Content = t.trimToolFeedback(msg.Content)
		}
		// Real user messages are never trimmed
	}

	return trimmed
}

// trimAssistantMessage removes verbose parts from assistant responses.
func (t *MessageTrimmer) trimAssistantMessage(content string) string {
	original := content

	// Strip <plan> blocks (and the older <reasoning> spelling) — the
	// intermediate plan doesn't need to be re-sent
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
//
// Calls are located by the agent's own tool call parser, so an argument
// holding an apostrophe ("Mary's") is compacted like any other instead of
// slipping past a quote-delimited pattern and staying verbatim.
func (t *MessageTrimmer) compactToolCalls(content string) string {
	calls, _ := agent.ParseToolCalls(content)
	for _, c := range calls {
		raw := strings.TrimSpace(c.Raw)
		if !strings.HasPrefix(raw, "<tool") || !strings.HasSuffix(raw, "/>") {
			continue // only self-closing XML calls are compacted
		}
		content = strings.Replace(content, c.Raw, compactSingleToolCall(c.Name, c.Args), 1)
	}
	return content
}

// compactSingleToolCall converts one tool call into a compact reference.
func compactSingleToolCall(toolName, argsStr string) string {
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
		truncated := textfold.Cut(output, 2000) +
			fmt.Sprintf("\n\n... [%d chars omitted] ...\n\n", len(output)-2500) +
			textfold.Tail(output, 500)
		return "<tool_output>\n" + truncated + "\n</tool_output>"
	})

	// If content doesn't have <tool_output> tags but is still huge (raw output),
	// reduce it. Prefer content-aware reversible compression (CCR); fall back to
	// byte-level head/tail truncation when compression isn't active or doesn't help.
	if len(content) > 8000 && !strings.Contains(content, "<tool_output>") {
		if c := t.compressOrEmpty(content); c != "" {
			return c
		}
		// Looks for the action-result header pattern (batch output)
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

// isInjectedContextMessage detects messages that were injected as context
// (/memory load, /context attach summaries, etc.).
func isInjectedContextMessage(content string) bool {
	return memoryContextMarkerRe.MatchString(content) ||
		attachedContextMarkerRe.MatchString(content)
}

// trimInjectedContext aggressively truncates injected context messages.
// These are not real conversation — they are reference material that can be summarized.
func (t *MessageTrimmer) trimInjectedContext(content string) string {
	if len(content) <= 3000 {
		return content
	}

	// Extract the marker/header line(s) and keep a truncated version
	lines := strings.SplitN(content, "\n", 4)
	header := strings.Join(lines[:min(3, len(lines))], "\n")

	// Prefer content-aware reversible compression of the body (keeps the
	// header so the model still sees what the context is). This is the main
	// chat-mode win: bulky attached/knowledge context becomes recoverable via
	// @recall instead of being permanently truncated.
	if len(header)+1 < len(content) {
		if c := t.compressOrEmpty(content[len(header)+1:]); c != "" {
			return header + "\n\n" + c
		}
	}

	truncated := header + "\n\n" +
		textfold.Cut(content[len(header)+1:], 2000) +
		fmt.Sprintf("\n\n... [%d chars of context omitted during compaction] ...", len(content)-2000)

	t.logger.Debug("Trimmed injected context message",
		zap.Int("before", len(content)),
		zap.Int("after", len(truncated)),
	)
	return truncated
}

// isToolFeedbackMessage detects messages that are tool output feedback (not real user input).
func isToolFeedbackMessage(content string) bool {
	return toolFeedbackPrefixRe.MatchString(content) ||
		formatErrorPrefixRe.MatchString(content) ||
		strings.Contains(content, "<tool_output>") ||
		strings.HasPrefix(content, agent.ActionResultPrefix) ||
		strings.HasPrefix(content, agent.LegacyActionResultPrefix)
}

// truncatePreservingStructure truncates long text keeping beginning and end,
// preserving any section headers.
func truncatePreservingStructure(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	headLen := maxLen * 2 / 3
	tailLen := maxLen / 3
	return textfold.Cut(s, headLen) +
		fmt.Sprintf("\n\n... [%d chars omitted for context efficiency] ...\n\n", len(s)-headLen-tailLen) +
		textfold.Tail(s, tailLen)
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
	return textfold.Cut(s, maxLen-3) + "..."
}
