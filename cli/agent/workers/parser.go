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

		calls = append(calls, call)
		remaining = remaining[len(raw):]
	}

	return calls, nil
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
