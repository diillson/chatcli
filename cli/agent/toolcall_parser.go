package agent

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
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
//   - <tool> alias:     <tool name="@x" args="..." /> — models backed by
//     other agent CLIs (Devin CLI, Codex CLI, Claude Code) shorten the tag
//   - Attributes in any order, single or double quotes
//   - Args containing '>' characters (JSON, HTML entities, etc.)
//   - JSON tool calls:  {"tool_call":"@coder","args":{...}}
//   - Multiple tool calls in a single response
//
// Quoted-context rule: tool-call syntax inside inline code spans (`...`) is
// always illustrative and never parsed. Fenced code blocks are illustrative by
// default too — a model DESCRIBING a tool (e.g. echoing @tools describe usage
// examples) must not trigger an execution — with one recovery exception: when
// the response contains no call outside a fence, fences whose info string is
// empty/xml/json are re-scanned, because some models wrap their one real call
// in such a fence. Fenced calls whose args are documentation placeholders
// ("...") are still rejected there.
func ParseToolCalls(text string) ([]ToolCall, error) {
	var calls []ToolCall

	// Split quoted contexts out first: fenced blocks are collected for the
	// recovery pass below, and the scannable copy has fenced lines and
	// inline code spans blanked so the primary parsers never see them.
	fences, scannable := maskQuotedSegments(text)

	// Try XML-style parsing first (primary format)
	xmlCalls, xmlErr := parseXMLToolCalls(scannable)
	if xmlErr == nil && len(xmlCalls) > 0 {
		calls = append(calls, xmlCalls...)
	}

	// Also try JSON-style tool calls (for models that output JSON).
	// Only add JSON calls that are NOT duplicates of already-parsed XML calls.
	// The JSON scanner can pick up JSON embedded inside XML args attributes
	// (e.g. the {"cmd":"read",...} inside args='{"cmd":"read",...}'), which
	// would cause the same tool call to appear twice in the batch.
	jsonCalls := parseJSONToolCalls(scannable)
	for _, jc := range jsonCalls {
		if !isDuplicateToolCall(calls, jc) {
			calls = append(calls, jc)
		}
	}

	// Recovery pass: no call found outside a fence — re-scan executable
	// fences (```xml / ```json / bare ```), where some models wrap their
	// actual call. Documentation fences (```bash, ```text, …) never execute.
	if len(calls) == 0 {
		calls = append(calls, parseFencedToolCalls(fences)...)
	}

	// Last-resort fallback: "[tool: @name {args}]" shorthand. Some models
	// (observed with Claude Opus 4.8 on Bedrock) emit this instead of the
	// canonical tag. Gated on zero calls so prose that merely mentions the
	// bracket syntax alongside a real <tool_call> never duplicates a batch.
	if len(calls) == 0 {
		calls = append(calls, parseBracketToolCalls(scannable)...)
	}

	// Drop documentation examples that leaked through: a call whose args are
	// a bare placeholder ("...", "{...}") is usage-syntax being described,
	// never an executable request. Applied to every stage so an unfenced
	// prose echo of a usage line is rejected the same way a fenced one is.
	calls = rejectUsageExamples(calls)

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
// It recognizes the canonical <tool_call ...> tag and the shorter <tool ...>
// alias in a single left-to-right pass, so mixed batches keep document order.
func parseXMLToolCalls(text string) ([]ToolCall, error) {
	var calls []ToolCall
	searchFrom := 0

	for searchFrom < len(text) {
		// Find the start of a <tool tag (case-insensitive). "<tool" is a
		// prefix of "<tool_call", so one scan finds both spellings; the tag
		// name is disambiguated right after.
		idx := indexCaseInsensitive(text[searchFrom:], "<tool")
		if idx < 0 {
			break
		}
		tagStart := searchFrom + idx

		tagName := "tool"
		const canonical = "<tool_call"
		if tagStart+len(canonical) <= len(text) &&
			strings.EqualFold(text[tagStart:tagStart+len(canonical)], canonical) {
			tagName = "tool_call"
		}

		// Ensure it's followed by whitespace or '>' (not part of another tag
		// like <tool_caller> or <toolbox>)
		afterTag := tagStart + 1 + len(tagName)
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
			// Self-closing: <tool_call ... /> or <tool ... />
			rawEnd = tagEnd + 2 // skip "/>"
		} else {
			// Opening tag: <tool_call ... > or <tool ... >
			rawEnd = tagEnd + 1 // skip ">"

			// Look for the optional matching closing tag. The exact-match
			// search means "</tool>" never swallows a "</tool_call>".
			closing := "</" + tagName + ">"
			closeIdx := indexCaseInsensitive(text[rawEnd:], closing)
			if closeIdx >= 0 {
				rawEnd = rawEnd + closeIdx + len(closing)
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

// parseBracketToolCalls handles the "[tool: @name {args}]" shorthand:
//
//	[tool: @websearch {"query":"golang"}]
//	[tool_call: @coder {'cmd':'read','args':{...}}]   (single quotes → recovery)
//	[tool: websearch {"query":"x"}]                    (missing @ → prepended)
//	[tool: @coder read --file main.go]                 (flat args string)
//	[tool: @tools]                                     (no args)
//
// The "tool:"/"tool_call:" prefix inside the bracket is the intent signal —
// a bare "[something]" in prose never matches. JSON args are extracted with
// balanced-brace scanning, so a missing closing bracket (truncated response)
// still recovers the call.
func parseBracketToolCalls(text string) []ToolCall {
	var calls []ToolCall
	searchFrom := 0

	for searchFrom < len(text) {
		idx := indexCaseInsensitive(text[searchFrom:], "[tool")
		if idx < 0 {
			break
		}
		start := searchFrom + idx

		name, pos, ok := parseBracketHeader(text, start+len("[tool"))
		if !ok {
			searchFrom = start + 1
			continue
		}

		args, end := parseBracketArgs(text, pos)

		// Optional trailing whitespace + closing ']' — tolerated if absent.
		for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
			end++
		}
		if end < len(text) && text[end] == ']' {
			end++
		}

		calls = append(calls, ToolCall{
			Name: name,
			Args: args,
			Raw:  text[start:end],
		})
		searchFrom = end
	}

	return calls
}

// parseBracketHeader consumes the optional "_call" spelling, the mandatory
// ":" separator and the tool name, starting right after "[tool". Returns the
// @-normalized name and the position after it. ok=false means this bracket
// is prose (no colon, empty name), not a call.
func parseBracketHeader(text string, pos int) (string, int, bool) {
	const callSuffix = "_call"
	if pos+len(callSuffix) <= len(text) && strings.EqualFold(text[pos:pos+len(callSuffix)], callSuffix) {
		pos += len(callSuffix)
	}

	// Require ":" (with optional surrounding spaces) — this is what
	// separates the shorthand from prose like "[tool docs]".
	pos = skipSpacesTabs(text, pos)
	if pos >= len(text) || text[pos] != ':' {
		return "", pos, false
	}
	pos = skipSpacesTabs(text, pos+1)

	// Tool name: optional '@' + attribute-name chars.
	nameStart := pos
	if pos < len(text) && text[pos] == '@' {
		pos++
	}
	for pos < len(text) && isAttrNameChar(text[pos]) {
		pos++
	}
	name := text[nameStart:pos]
	if strings.TrimPrefix(name, "@") == "" {
		return "", pos, false
	}
	if !strings.HasPrefix(name, "@") {
		name = "@" + name
	}
	for pos < len(text) && (text[pos] == ' ' || text[pos] == '\t' || text[pos] == '\n' || text[pos] == '\r') {
		pos++
	}
	return name, pos, true
}

// parseBracketArgs extracts the args portion of a bracket call starting at
// pos: a balanced JSON object, or a flat string up to the closing ']' (the
// downstream lenient arg parser understands "exec ls -t file" shapes).
// Returns the args and the position where they end.
func parseBracketArgs(text string, pos int) (string, int) {
	if pos < len(text) && text[pos] == '{' {
		if obj := extractJSONObject(text, pos); obj != "" {
			return obj, pos + len(obj)
		}
		// Unbalanced braces — fall through to flat-args handling.
	}
	if pos < len(text) && text[pos] == ']' {
		return "", pos
	}
	if closeIdx := strings.IndexByte(text[pos:], ']'); closeIdx >= 0 {
		return strings.TrimSpace(text[pos : pos+closeIdx]), pos + closeIdx
	}
	return strings.TrimSpace(text[pos:]), len(text)
}

func skipSpacesTabs(text string, pos int) int {
	for pos < len(text) && (text[pos] == ' ' || text[pos] == '\t') {
		pos++
	}
	return pos
}

// fencedBlock is one markdown code fence collected by maskQuotedSegments:
// its info string (the word after the opening backticks) and raw content.
type fencedBlock struct {
	info    string
	content string
}

// maskQuotedSegments walks text line by line collecting fenced code blocks
// and producing a "scannable" copy where fenced lines and inline code spans
// are blanked out (byte-for-byte, so no offset shifts). The primary parsers
// run on the scannable copy — syntax the model merely QUOTES (a usage example
// in a fence, a tag in `inline code`) is invisible to them — while the
// collected fences feed the executable-fence recovery pass.
func maskQuotedSegments(text string) ([]fencedBlock, string) {
	var blocks []fencedBlock
	var masked strings.Builder
	masked.Grow(len(text))

	inFence := false
	var fenceMarker byte
	var fenceLen int
	var info string
	var content strings.Builder

	rest := text
	for len(rest) > 0 {
		line := rest
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			line = rest[:nl+1]
			rest = rest[nl+1:]
		} else {
			rest = ""
		}
		trimmed := strings.TrimLeft(line, " \t")

		if inFence {
			if fenceCloses(trimmed, fenceMarker, fenceLen) {
				inFence = false
				blocks = append(blocks, fencedBlock{info: info, content: content.String()})
			} else {
				content.WriteString(line)
			}
			masked.WriteString(blankPreservingNewlines(line))
			continue
		}

		if marker, mlen, rem, ok := fenceOpens(trimmed); ok {
			inFence = true
			fenceMarker, fenceLen = marker, mlen
			info = strings.TrimSpace(rem)
			content.Reset()
			masked.WriteString(blankPreservingNewlines(line))
			continue
		}

		masked.WriteString(maskInlineCodeSpans(line))
	}
	if inFence {
		// Unterminated fence (truncated response): still counts as fenced.
		blocks = append(blocks, fencedBlock{info: info, content: content.String()})
	}
	return blocks, masked.String()
}

// fenceOpens reports whether a (left-trimmed) line opens a code fence: a run
// of three or more '`' or '~'. Returns the marker, run length and the info
// string remainder.
func fenceOpens(trimmed string) (byte, int, string, bool) {
	if len(trimmed) < 3 {
		return 0, 0, "", false
	}
	marker := trimmed[0]
	if marker != '`' && marker != '~' {
		return 0, 0, "", false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == marker {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	return marker, n, strings.TrimRight(trimmed[n:], "\r\n"), true
}

// fenceCloses reports whether a (left-trimmed) line closes the open fence:
// a run of at least fenceLen of the same marker with nothing else after it.
func fenceCloses(trimmed string, marker byte, fenceLen int) bool {
	n := 0
	for n < len(trimmed) && trimmed[n] == marker {
		n++
	}
	if n < fenceLen {
		return false
	}
	return strings.TrimSpace(trimmed[n:]) == ""
}

// blankPreservingNewlines replaces every byte of s with a space except CR/LF,
// keeping the masked copy byte-aligned with the original.
func blankPreservingNewlines(s string) string {
	b := []byte(s)
	for i, ch := range b {
		if ch != '\n' && ch != '\r' {
			b[i] = ' '
		}
	}
	return string(b)
}

// maskInlineCodeSpans blanks `inline code` spans within a single line. A span
// is a backtick run and the next run of the same length; unmatched backticks
// are left untouched.
func maskInlineCodeSpans(line string) string {
	if !strings.Contains(line, "`") {
		return line
	}
	b := []byte(line)
	i := 0
	for i < len(b) {
		if b[i] != '`' {
			i++
			continue
		}
		runLen := 0
		for i+runLen < len(b) && b[i+runLen] == '`' {
			runLen++
		}
		delim := strings.Repeat("`", runLen)
		closeIdx := indexDelimiterRun(string(b[i+runLen:]), delim)
		if closeIdx < 0 {
			i += runLen
			continue
		}
		for j := i; j < i+runLen+closeIdx+runLen; j++ {
			if b[j] != '\n' && b[j] != '\r' {
				b[j] = ' '
			}
		}
		i += runLen + closeIdx + runLen
	}
	return string(b)
}

// indexDelimiterRun finds delim in s where the match is not part of a longer
// backtick run (CommonMark: a 1-backtick span cannot close on a 2-backtick
// run). Returns the index or -1.
func indexDelimiterRun(s, delim string) int {
	from := 0
	for {
		idx := strings.Index(s[from:], delim)
		if idx < 0 {
			return -1
		}
		pos := from + idx
		end := pos + len(delim)
		if end < len(s) && s[end] == '`' {
			// Longer run — skip past it entirely.
			for end < len(s) && s[end] == '`' {
				end++
			}
			from = end
			continue
		}
		return pos
	}
}

// executableFenceInfo reports whether a fence's info string marks content the
// recovery pass may treat as a real call. Only the shapes models actually use
// to WRAP a call qualify; anything else (```bash, ```text, ```markdown, a
// natural-language label…) is documentation and never executes.
func executableFenceInfo(info string) bool {
	switch strings.ToLower(strings.TrimSpace(info)) {
	case "", "xml", "json", "tool", "tool_call", "toolcall":
		return true
	}
	return false
}

// parseFencedToolCalls extracts tool calls from collected fenced blocks. Runs
// only when nothing was found outside a fence (see ParseToolCalls), and only
// over executable fences.
func parseFencedToolCalls(fences []fencedBlock) []ToolCall {
	var calls []ToolCall
	for _, f := range fences {
		if !executableFenceInfo(f.info) {
			continue
		}
		xmlCalls, _ := parseXMLToolCalls(f.content)
		calls = append(calls, xmlCalls...)
		// Same dedup rule as the top-level flow: the JSON scanner re-finds
		// the object embedded in an XML args attribute.
		for _, jc := range parseJSONToolCalls(f.content) {
			if !isDuplicateToolCall(calls, jc) {
				calls = append(calls, jc)
			}
		}
	}
	return calls
}

// usageExamplePlaceholderRe matches an args value that is entirely an
// ellipsis placeholder, e.g. "query": "..." — the shape documentation and
// @tools describe output use for "fill this in".
var usageExamplePlaceholderRe = regexp.MustCompile(`:\s*['"](?:\.\.\.|…)['"]`)

// isUsageExampleArgs reports whether args reads as documentation placeholder
// syntax rather than an executable request.
func isUsageExampleArgs(args string) bool {
	t := strings.TrimSpace(args)
	switch t {
	case "...", "…", "{...}", "{…}":
		return true
	}
	if strings.Contains(t, "<...>") {
		return true
	}
	return usageExamplePlaceholderRe.MatchString(t)
}

// rejectUsageExamples filters out calls whose args are documentation
// placeholders. A described call ("use it like <tool_call name=… args='…'/>")
// must never reach the execution gate.
func rejectUsageExamples(calls []ToolCall) []ToolCall {
	out := calls[:0]
	for _, c := range calls {
		if isUsageExampleArgs(c.Args) {
			continue
		}
		out = append(out, c)
	}
	return out
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
