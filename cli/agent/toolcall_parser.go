package agent

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

// ToolCall represents a parsed tool invocation from AI output text.
type ToolCall struct {
	Name string
	Args string
	Raw  string
}

// ParseToolCalls extracts tool calls from AI response text.
//
// Supported formats:
//   - XML self-closing: <tool_call name="@x" args="..." />
//   - XML paired:       <tool_call name="@x" args="..."></tool_call>
//   - Attributes in any order, single or double quotes
//   - Args containing '>' characters (JSON, HTML entities, etc.)
//   - JSON tool calls:  {"tool_call":"@coder","args":{...}}
//   - Multiple tool calls in a single response
func ParseToolCalls(text string) ([]ToolCall, error) {
	var calls []ToolCall

	// Try XML-style parsing first (primary format)
	xmlCalls, xmlErr := parseXMLToolCalls(text)
	if xmlErr == nil && len(xmlCalls) > 0 {
		calls = append(calls, xmlCalls...)
	}

	// Also try JSON-style tool calls (for models that output JSON).
	// Only add JSON calls that are NOT duplicates of already-parsed XML calls.
	// The JSON scanner can pick up JSON embedded inside XML args attributes
	// (e.g. the {"cmd":"read",...} inside args='{"cmd":"read",...}'), which
	// would cause the same tool call to appear twice in the batch.
	jsonCalls := parseJSONToolCalls(text)
	for _, jc := range jsonCalls {
		if !isDuplicateToolCall(calls, jc) {
			calls = append(calls, jc)
		}
	}

	// Try extracting from markdown code blocks (```xml or ```json)
	if len(calls) == 0 {
		mdCalls := parseMarkdownCodeBlockToolCalls(text)
		calls = append(calls, mdCalls...)
	}

	// Apply JSON recovery/normalization to ALL parsed calls (XML, JSON, markdown).
	// This fixes single quotes, unquoted keys, and other malformations.
	// Must run AFTER all parsing stages so that every source benefits from recovery.
	for i := range calls {
		calls[i].Args = recoverToolCallArgs(calls[i].Name, calls[i].Args)
	}

	if xmlErr != nil && len(calls) == 0 {
		return nil, xmlErr
	}

	return calls, nil
}

// recoverToolCallArgs applies JSON recovery to tool call args.
// If args is already valid JSON, returns as-is.
// Otherwise attempts multiple recovery strategies (single quotes, unquoted keys, etc.).
func recoverToolCallArgs(toolName, args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return args
	}

	// If it's already valid JSON, no recovery needed
	var v interface{}
	if json.Unmarshal([]byte(trimmed), &v) == nil {
		return args
	}

	// Try recovery
	if normalized, ok := NormalizeToolArgs(toolName, trimmed); ok {
		return normalized
	}

	return args
}

// isDuplicateToolCall checks whether candidate is a duplicate of any existing
// call. Two calls are considered duplicates when they target the same tool and
// the candidate's args string appears inside (or is equal to) an existing
// call's args. This catches the common case where parseJSONToolCalls extracts
// the JSON object that is already embedded in an XML tool_call's args attribute.
func isDuplicateToolCall(existing []ToolCall, candidate ToolCall) bool {
	for _, ec := range existing {
		if ec.Name != candidate.Name {
			continue
		}
		if ec.Args == candidate.Args {
			return true
		}
		if strings.Contains(ec.Args, candidate.Args) {
			return true
		}
		if candidate.Raw != "" && ec.Raw != "" && strings.Contains(ec.Raw, candidate.Raw) {
			return true
		}
	}
	return false
}

// parseXMLToolCalls uses a stateful scanner to properly handle quoted attributes
// containing special characters like '>' that would break regex-based parsing.
func parseXMLToolCalls(text string) ([]ToolCall, error) {
	var calls []ToolCall
	searchFrom := 0

	for searchFrom < len(text) {
		// Find the start of a <tool_call tag (case-insensitive)
		idx := indexCaseInsensitive(text[searchFrom:], "<tool_call")
		if idx < 0 {
			break
		}
		tagStart := searchFrom + idx

		// Ensure it's followed by whitespace or '>' (not part of another tag like <tool_caller>)
		afterTag := tagStart + len("<tool_call")
		if afterTag < len(text) {
			ch := text[afterTag]
			if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' && ch != '>' && ch != '/' {
				searchFrom = afterTag
				continue
			}
		}

		// Scan forward through the tag, respecting quoted attribute values
		tagEnd, selfClosing := scanTagEnd(text, afterTag)
		if tagEnd < 0 {
			// Malformed tag - skip past the opening
			searchFrom = afterTag
			continue
		}

		attrText := text[afterTag:tagEnd]
		var rawEnd int

		if selfClosing {
			// Self-closing: <tool_call ... />
			rawEnd = tagEnd + 2 // skip "/>"
		} else {
			// Opening tag: <tool_call ... >
			rawEnd = tagEnd + 1 // skip ">"

			// Look for optional closing </tool_call>
			closeIdx := indexCaseInsensitive(text[rawEnd:], "</tool_call>")
			if closeIdx >= 0 {
				rawEnd = rawEnd + closeIdx + len("</tool_call>")
			}
		}

		raw := text[tagStart:rawEnd]

		// Extract attributes from the attribute text
		name, nameErr := extractAttrStateful(attrText, "name")
		if nameErr != nil {
			// Skip malformed tool_calls instead of failing the entire batch
			searchFrom = rawEnd
			continue
		}

		args, _ := extractAttrStateful(attrText, "args") // args can be empty

		calls = append(calls, ToolCall{
			Name: strings.TrimSpace(name),
			Args: args,
			Raw:  raw,
		})

		searchFrom = rawEnd
	}

	return calls, nil
}

// scanTagEnd scans from position pos in text to find the end of the opening tag.
// It respects single and double quotes so that '>' inside attribute values is not
// mistaken for the end of the tag.
// Returns (position_before_close, isSelfClosing).
// position is the index of '/' in "/>" for self-closing, or '>' for normal close.
// Returns (-1, false) if end not found.
func scanTagEnd(text string, pos int) (int, bool) {
	inSingle := false
	inDouble := false
	n := len(text)

	for i := pos; i < n; i++ {
		ch := text[i]

		if inDouble {
			if ch == '\\' && i+1 < n {
				i++ // skip escaped char
				continue
			}
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if ch == '\\' && i+1 < n {
				i++ // skip escaped char
				continue
			}
			if ch == '\'' {
				inSingle = false
			}
			continue
		}

		switch ch {
		case '"':
			inDouble = true
		case '\'':
			inSingle = true
		case '/':
			if i+1 < n && text[i+1] == '>' {
				return i, true // self-closing "/>"
			}
		case '>':
			return i, false // normal close ">"
		}
	}

	return -1, false
}

// extractAttrStateful extracts an attribute value using stateful scanning
// instead of regex, properly handling nested quotes and special characters.
func extractAttrStateful(attrText, key string) (string, error) {
	lower := strings.ToLower(attrText)
	keyLower := strings.ToLower(key)

	// Find key= pattern
	searchFrom := 0
	for {
		idx := strings.Index(lower[searchFrom:], keyLower)
		if idx < 0 {
			return "", fmt.Errorf("attribute %q not found", key)
		}
		pos := searchFrom + idx

		// Verify it's a word boundary (not part of another attribute name)
		if pos > 0 {
			prev := attrText[pos-1]
			if isAttrNameChar(prev) {
				searchFrom = pos + len(key)
				continue
			}
		}

		// Skip past key name
		afterKey := pos + len(key)

		// Skip whitespace
		for afterKey < len(attrText) && (attrText[afterKey] == ' ' || attrText[afterKey] == '\t') {
			afterKey++
		}

		// Expect '='
		if afterKey >= len(attrText) || attrText[afterKey] != '=' {
			searchFrom = afterKey
			continue
		}
		afterKey++ // skip '='

		// Skip whitespace after '='
		for afterKey < len(attrText) && (attrText[afterKey] == ' ' || attrText[afterKey] == '\t') {
			afterKey++
		}

		if afterKey >= len(attrText) {
			return "", fmt.Errorf("attribute %q has no value", key)
		}

		// Extract quoted value
		quote := attrText[afterKey]
		if quote != '"' && quote != '\'' {
			// Unquoted value - read until whitespace
			end := afterKey
			for end < len(attrText) && attrText[end] != ' ' && attrText[end] != '\t' && attrText[end] != '\n' {
				end++
			}
			val := attrText[afterKey:end]
			return html.UnescapeString(val), nil
		}

		// Scan for matching closing quote, respecting escapes
		val, err := extractQuotedValue(attrText, afterKey)
		if err != nil {
			return "", fmt.Errorf("attribute %q: %w", key, err)
		}

		return val, nil
	}
}

// extractQuotedValue extracts a quoted string starting at pos, handling escape sequences.
func extractQuotedValue(text string, pos int) (string, error) {
	if pos >= len(text) {
		return "", fmt.Errorf("unexpected end of input")
	}

	quote := text[pos]
	var buf strings.Builder
	i := pos + 1

	for i < len(text) {
		ch := text[i]

		if ch == '\\' && i+1 < len(text) {
			next := text[i+1]
			// Only treat as escape if it's escaping the quote char or another backslash
			if next == quote || next == '\\' {
				buf.WriteByte(next)
				i += 2
				continue
			}
			// For other escape sequences, keep them as-is for downstream processing
			buf.WriteByte(ch)
			i++
			continue
		}

		if ch == quote {
			// Found closing quote
			return buf.String(), nil
		}

		buf.WriteByte(ch)
		i++
	}

	// If we reach here, the quote was never closed.
	// Be lenient: return what we have (common with malformed AI output)
	return buf.String(), nil
}

func isAttrNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') || b == '-' || b == '_'
}

// indexCaseInsensitive finds needle in haystack (case-insensitive).
func indexCaseInsensitive(haystack, needle string) int {
	lower := strings.ToLower(haystack)
	return strings.Index(lower, strings.ToLower(needle))
}

// parseJSONToolCalls attempts to find JSON-formatted tool calls in the text.
// Some newer models may output tool calls as JSON objects instead of XML.
// Supports formats like:
//
//	{"tool_call":"@coder","args":{...}}
//	{"name":"@coder","arguments":{...}}
//	{"cmd":"read","args":{"file":"main.go"}}  (implicit @coder)
func parseJSONToolCalls(text string) []ToolCall {
	var calls []ToolCall

	// Look for JSON objects that contain tool call patterns
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}

		// Try to find matching closing brace
		jsonStr := extractJSONObject(text, i)
		if jsonStr == "" {
			continue
		}

		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &obj); err != nil {
			// Try JSON recovery before giving up
			if recovered, ok := NormalizeToolArgs("", jsonStr); ok && recovered != jsonStr {
				if json.Unmarshal([]byte(recovered), &obj) != nil {
					continue
				}
				jsonStr = recovered
			} else {
				continue
			}
		}

		// Check if this is a tool call object
		tc, ok := jsonObjToToolCall(obj)
		if !ok {
			continue
		}

		calls = append(calls, ToolCall{
			Name: tc.Name,
			Args: tc.Args,
			Raw:  jsonStr,
		})

		i += len(jsonStr) - 1
	}

	return calls
}

// parseMarkdownCodeBlockToolCalls extracts tool calls from markdown code blocks.
// LLMs sometimes wrap tool calls in ```xml or ```json blocks.
func parseMarkdownCodeBlockToolCalls(text string) []ToolCall {
	var calls []ToolCall

	// Find ```xml ... ``` or ```json ... ``` blocks
	searchFrom := 0
	for searchFrom < len(text) {
		startIdx := strings.Index(text[searchFrom:], "```")
		if startIdx < 0 {
			break
		}
		startIdx += searchFrom

		// Find the end of the opening fence line
		lineEnd := strings.Index(text[startIdx+3:], "\n")
		if lineEnd < 0 {
			break
		}
		lineEnd += startIdx + 3

		// Find closing ```
		closeIdx := strings.Index(text[lineEnd:], "```")
		if closeIdx < 0 {
			break
		}
		closeIdx += lineEnd

		blockContent := text[lineEnd:closeIdx]

		// Try parsing the block content as tool calls
		xmlCalls, _ := parseXMLToolCalls(blockContent)
		calls = append(calls, xmlCalls...)

		jsonCalls := parseJSONToolCalls(blockContent)
		calls = append(calls, jsonCalls...)

		searchFrom = closeIdx + 3
	}

	return calls
}

// extractJSONObject attempts to extract a balanced JSON object starting at pos.
// Tracks both single and double quoted strings so that braces inside quoted
// values (e.g., {'cmd': 'echo }'}) don't cause premature termination.
func extractJSONObject(text string, pos int) string {
	if pos >= len(text) || text[pos] != '{' {
		return ""
	}

	depth := 0
	inDouble := false
	inSingle := false
	escaped := false

	for i := pos; i < len(text); i++ {
		ch := text[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' && (inDouble || inSingle) {
			escaped = true
			continue
		}

		if inDouble {
			if ch == '"' {
				inDouble = false
			}
			continue
		}

		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			continue
		}

		switch ch {
		case '"':
			inDouble = true
		case '\'':
			inSingle = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[pos : i+1]
			}
		}
	}

	return ""
}

// jsonObjToToolCall checks if a JSON object represents a tool call and converts it.
// Supports multiple formats LLMs commonly output:
//
//	{"tool_call":"@coder", "args":{...}}
//	{"name":"@coder", "arguments":{...}}
//	{"cmd":"read", "args":{"file":"main.go"}}  (implicit @coder)
//	{"tool":"@coder", "args":"read --file main.go"}
func jsonObjToToolCall(obj map[string]interface{}) (ToolCall, bool) {
	// Reject objects that look like search results, API responses, or data payloads.
	// These commonly appear in tool outputs and should NOT be parsed as tool calls.
	if _, hasURL := obj["url"]; hasURL {
		return ToolCall{}, false // search result or web response
	}
	if _, hasTitle := obj["title"]; hasTitle {
		if _, hasSnippet := obj["snippet"]; hasSnippet {
			return ToolCall{}, false // search result
		}
	}
	if _, hasType := obj["type"]; hasType {
		if _, hasError := obj["error"]; hasError {
			return ToolCall{}, false // API error response
		}
	}

	// Try various common key patterns for the tool name
	name := ""
	if v, ok := obj["tool_call"].(string); ok {
		name = v
	} else if v, ok := obj["name"].(string); ok {
		name = v
	} else if v, ok := obj["tool"].(string); ok {
		name = v
	}

	// Extract args
	var argsStr string
	extractArgs := func(v interface{}) string {
		if s, ok := v.(string); ok {
			return s
		}
		b, err := json.Marshal(v)
		if err == nil {
			return string(b)
		}
		return ""
	}

	if v, ok := obj["args"]; ok {
		argsStr = extractArgs(v)
	} else if v, ok := obj["arguments"]; ok {
		argsStr = extractArgs(v)
	}

	// If we have a name with @, return directly
	if name != "" && strings.HasPrefix(name, "@") {
		return ToolCall{Name: name, Args: argsStr}, true
	}

	// Implicit @coder format: {"cmd":"read", "args":{"file":"main.go"}}
	// This is the most common format LLMs produce when confused.
	//
	// IMPORTANT: This must NOT match tool call args from other tools like @websearch.
	// The @websearch plugin uses {"cmd":"search","args":{"query":"..."}} which has the
	// same structure. We distinguish by checking the inner args keys:
	//   - @coder search uses: term, dir, regex, glob, context, max_results
	//   - @websearch uses: query, num_results, language
	if cmd, ok := obj["cmd"].(string); ok && cmd != "" {
		validCmds := map[string]bool{
			"read": true, "write": true, "patch": true, "tree": true,
			"search": true, "exec": true, "test": true, "rollback": true, "clean": true,
			"git-status": true, "git-diff": true, "git-log": true,
			"git-changed": true, "git-branch": true,
		}
		if validCmds[cmd] {
			// Extra validation for "search" — reject if inner args look like @websearch
			if cmd == "search" {
				if innerArgs, ok := obj["args"].(map[string]interface{}); ok {
					if _, hasQuery := innerArgs["query"]; hasQuery {
						// This is @websearch format, not @coder search
						return ToolCall{}, false
					}
				}
			}

			wrapped := map[string]interface{}{"cmd": cmd}
			if v, ok := obj["args"]; ok {
				wrapped["args"] = v
			}
			b, err := json.Marshal(wrapped)
			if err == nil {
				return ToolCall{Name: "@coder", Args: string(b)}, true
			}
		}
	}

	// Try name without @ prefix (some models drop it).
	// IMPORTANT: Only "coder" is valid here. Do NOT add generic names like
	// "search", "file", "shell" — they collide with JSON fields in tool outputs
	// (e.g., websearch results containing {"name":"search",...} would be
	// falsely detected as @search tool calls).
	if name == "coder" {
		return ToolCall{Name: "@coder", Args: argsStr}, true
	}

	return ToolCall{}, false
}
