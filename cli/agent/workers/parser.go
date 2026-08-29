package workers

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// callIDCounter is used to generate unique IDs for agent calls.
var callIDCounter uint64

func nextCallID() string {
	id := atomic.AddUint64(&callIDCounter, 1)
	return fmt.Sprintf("ac-%d", id)
}

// ParseAgentCalls extracts <agent_call> tags from AI response text.
// Supports both self-closing and paired tag forms:
//
//	<agent_call agent="file" task="read main.go" />
//	<agent_call agent="coder" task="write new file">optional body</agent_call>
//
// Uses a stateful scanner (same approach as toolcall_parser.go) to handle
// quoted attributes containing special characters.
func ParseAgentCalls(text string) ([]AgentCall, error) {
	var calls []AgentCall
	remaining := text

	for {
		// Find the next <agent_call tag
		idx := strings.Index(remaining, "<agent_call")
		if idx < 0 {
			break
		}

		remaining = remaining[idx:]

		// Find the end of the tag using stateful scanning
		endPos, selfClosing := scanAgentTagEnd(remaining)
		if endPos < 0 {
			// Malformed tag — skip past the opening and continue
			remaining = remaining[len("<agent_call"):]
			continue
		}

		tagContent := remaining[:endPos+1]

		// Extract attributes
		agentType := extractAgentAttr(tagContent, "agent")
		task := extractAgentAttr(tagContent, "task")

		if agentType == "" || task == "" {
			remaining = remaining[endPos+1:]
			continue
		}

		// If not self-closing, capture body content up to </agent_call>
		raw := tagContent
		if !selfClosing {
			closeTag := "</agent_call>"
			closeIdx := strings.Index(remaining[endPos+1:], closeTag)
			if closeIdx >= 0 {
				bodyEnd := endPos + 1 + closeIdx + len(closeTag)
				raw = remaining[:bodyEnd]
				// Body content could augment the task
				body := strings.TrimSpace(remaining[endPos+1 : endPos+1+closeIdx])
				if body != "" {
					task = task + "\n" + body
				}
			}
		}

		call := AgentCall{
			Agent: AgentType(strings.ToLower(strings.TrimSpace(agentType))),
			Task:  task,
			ID:    nextCallID(),
			Raw:   raw,
		}
		// Optional per-task session-plugin grant:
		// <agent_call agent="coder" task="..." tools="@browser,@websearch" />
		if toolsAttr := extractAgentAttr(tagContent, "tools"); toolsAttr != "" {
			if grant := NormalizePluginGrant(strings.Split(toolsAttr, ",")); len(grant) > 0 {
				call.Plugins = &PluginGrant{Plugins: grant}
			}
		}

		calls = append(calls, call)
		remaining = remaining[len(raw):]
	}

	return calls, nil
}

// CountAgentCallTags counts every <agent_call opening tag in text —
// parseable or not. The dispatch loop compares it against the parsed count
// to detect malformed calls: ParseAgentCalls silently skips tags with a
// broken end or missing agent/task attributes, and without this signal the
// orchestrator never learns its dispatch was dropped — it drifts out of
// the squad flow and starts executing specialists' work with direct tool
// calls.
func CountAgentCallTags(text string) int {
	const open = "<agent_call"
	n := 0
	for i := 0; ; {
		idx := strings.Index(text[i:], open)
		if idx < 0 {
			break
		}
		pos := i + idx + len(open)
		if pos >= len(text) {
			n++
			break
		}
		// Word boundary: "<agent_calls" or similar is not a tag.
		switch text[pos] {
		case ' ', '\t', '\n', '\r', '>', '/':
			n++
		}
		i = pos
	}
	return n
}

// MalformedAgentCallFeedback builds the corrective user-role message
// injected into the orchestrator's history when some of its <agent_call>
// tags failed to parse. The instruction is explicit about staying in the
// delegation flow — the observed failure mode is the model silently
// falling back to doing the specialist's work itself.
func MalformedAgentCallFeedback(dropped, parsed int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[AGENT_CALL PARSE ERROR] %d of your <agent_call> tag(s) were malformed and were NOT dispatched", dropped)
	if parsed > 0 {
		fmt.Fprintf(&b, " (%d parsed correctly and did dispatch)", parsed)
	}
	b.WriteString(".\n\n")
	b.WriteString("Required syntax — both attributes are mandatory, quotes required:\n")
	b.WriteString("  <agent_call agent=\"coder\" task=\"describe the task here\" />\n")
	b.WriteString("or with a body:\n")
	b.WriteString("  <agent_call agent=\"coder\" task=\"short title\">detailed instructions</agent_call>\n\n")
	b.WriteString("In your NEXT response, RE-EMIT the corrected <agent_call> tag(s) for the dropped task(s). ")
	b.WriteString("Do NOT execute those tasks yourself with direct tool calls and do NOT abandon the squad flow — ")
	b.WriteString("delegation to the specialized agents remains the default.")
	return b.String()
}

// scanAgentTagEnd finds the end of an <agent_call ...> or <agent_call ... /> tag,
// respecting quotes so that '>' inside attributes doesn't terminate the scan.
// Returns the index of '>' and whether the tag is self-closing.
func scanAgentTagEnd(tag string) (int, bool) {
	inSingleQuote := false
	inDoubleQuote := false

	for i := 0; i < len(tag); i++ {
		ch := tag[i]

		if ch == '\\' && i+1 < len(tag) {
			i++ // skip escaped char
			continue
		}

		if ch == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}
		if ch == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}

		if !inSingleQuote && !inDoubleQuote {
			if ch == '/' && i+1 < len(tag) && tag[i+1] == '>' {
				return i + 1, true // self-closing
			}
			if ch == '>' {
				return i, false // paired tag
			}
		}
	}
	return -1, false
}

// extractAgentAttr extracts an attribute value from a tag string.
// Handles both single-quoted and double-quoted values.
func extractAgentAttr(tag string, key string) string {
	// Search for key= (case-insensitive)
	lower := strings.ToLower(tag)
	keyPattern := strings.ToLower(key) + "="

	idx := strings.Index(lower, keyPattern)
	if idx < 0 {
		return ""
	}

	valStart := idx + len(keyPattern)
	if valStart >= len(tag) {
		return ""
	}

	quote := tag[valStart]
	if quote != '"' && quote != '\'' {
		// Unquoted value — read until whitespace or > or /
		end := valStart
		for end < len(tag) && tag[end] != ' ' && tag[end] != '\t' && tag[end] != '>' && tag[end] != '/' {
			end++
		}
		return tag[valStart:end]
	}

	// Quoted value — find matching close quote
	closeIdx := -1
	for i := valStart + 1; i < len(tag); i++ {
		if tag[i] == '\\' && i+1 < len(tag) {
			i++ // skip escaped
			continue
		}
		if tag[i] == quote {
			closeIdx = i
			break
		}
	}

	if closeIdx < 0 {
		return ""
	}

	return tag[valStart+1 : closeIdx]
}
