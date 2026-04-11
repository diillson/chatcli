package agent

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// NormalizeToolArgs attempts multiple recovery strategies to parse malformed JSON
// from LLM tool call arguments. This handles common issues like:
//   - Single quotes instead of double quotes
//   - Unquoted keys: {cmd: "read", file: "main.go"}
//   - Plain string values that should be wrapped: "main.go" → {"file":"main.go"}
//   - Completely unstructured text: "read --file main.go"
//
// Returns the normalized JSON string and true if recovery succeeded.
func NormalizeToolArgs(toolName, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", true
	}

	// Attempt 1: standard JSON parse
	if isValidJSON(raw) {
		return raw, true
	}

	// Attempt 2: fix single quotes → double quotes
	if fixed := fixSingleQuotedJSON(raw); fixed != "" && isValidJSON(fixed) {
		return fixed, true
	}

	// Attempt 3: fix unquoted keys {cmd: "read"} → {"cmd": "read"}
	if fixed := fixUnquotedKeys(raw); fixed != "" && isValidJSON(fixed) {
		return fixed, true
	}

	// Attempt 4: combined — fix quotes then keys
	if fixed := fixSingleQuotedJSON(raw); fixed != "" {
		if fixed2 := fixUnquotedKeys(fixed); fixed2 != "" && isValidJSON(fixed2) {
			return fixed2, true
		}
	}

	// Attempt 5: fix trailing commas
	if fixed := fixTrailingCommas(raw); fixed != "" && isValidJSON(fixed) {
		return fixed, true
	}

	// Attempt 6: plain string wrapping based on tool name
	if wrapped := WrapPlainStringForTool(toolName, raw); wrapped != "" {
		return wrapped, true
	}

	// Attempt 7: if it looks like a structured object literal, try aggressive fix
	if isLikelyStructuredObjectLiteral(raw) {
		if fixed := aggressiveJSONFix(raw); fixed != "" && isValidJSON(fixed) {
			return fixed, true
		}
	}

	return raw, false
}

// isValidJSON checks if a string is valid JSON.
func isValidJSON(s string) bool {
	var v interface{}
	return json.Unmarshal([]byte(s), &v) == nil
}

// fixSingleQuotedJSON replaces single-quoted JSON strings with double-quoted ones.
// Handles nested quotes by escaping them properly.
// Example: {'cmd':'read','file':'main.go'} → {"cmd":"read","file":"main.go"}
func fixSingleQuotedJSON(input string) string {
	if !strings.Contains(input, "'") {
		return ""
	}

	// Only attempt if it looks like a JSON structure
	trimmed := strings.TrimSpace(input)
	if len(trimmed) < 2 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return ""
	}

	var result strings.Builder
	result.Grow(len(input))

	inSingle := false
	inDouble := false
	i := 0
	n := len(input)

	for i < n {
		ch := input[i]

		if inDouble {
			if ch == '\\' && i+1 < n {
				result.WriteByte(ch)
				result.WriteByte(input[i+1])
				i += 2
				continue
			}
			if ch == '"' {
				inDouble = false
			}
			result.WriteByte(ch)
			i++
			continue
		}

		if inSingle {
			if ch == '\\' && i+1 < n {
				next := input[i+1]
				if next == '\'' {
					// \' inside single-quoted → just write the quote
					result.WriteByte('\'')
					i += 2
					continue
				}
				result.WriteByte(ch)
				result.WriteByte(next)
				i += 2
				continue
			}
			if ch == '\'' {
				// Closing single quote → convert to double quote
				inSingle = false
				result.WriteByte('"')
				i++
				continue
			}
			// Escape any double quotes inside single-quoted string
			if ch == '"' {
				result.WriteString(`\"`)
				i++
				continue
			}
			result.WriteByte(ch)
			i++
			continue
		}

		// Not inside any string
		if ch == '\'' {
			inSingle = true
			result.WriteByte('"')
			i++
			continue
		}
		if ch == '"' {
			inDouble = true
			result.WriteByte(ch)
			i++
			continue
		}

		result.WriteByte(ch)
		i++
	}

	return result.String()
}

// unquotedKeyRe matches unquoted keys like: {cmd: or , cmd:
var unquotedKeyRe = regexp.MustCompile(`([{,]\s*)([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)

// fixUnquotedKeys adds double quotes to unquoted JSON keys.
// Example: {cmd: "read", file: "main.go"} → {"cmd": "read", "file": "main.go"}
func fixUnquotedKeys(input string) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return ""
	}

	result := unquotedKeyRe.ReplaceAllString(input, `${1}"${2}":`)
	if result == input {
		return ""
	}
	return result
}

// fixTrailingCommas removes trailing commas before } or ] in JSON.
// Example: {"cmd":"read","file":"main.go",} → {"cmd":"read","file":"main.go"}
func fixTrailingCommas(input string) string {
	re := regexp.MustCompile(`,\s*([}\]])`)
	result := re.ReplaceAllString(input, `$1`)
	if result == input {
		return ""
	}
	return result
}

// IsLikelyStructuredObjectLiteral detects if a string looks like a JavaScript-style
// object literal that should be a JSON object, e.g.: {cmd: read, file: main.go}
func isLikelyStructuredObjectLiteral(s string) bool {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 3 {
		return false
	}
	if trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}

	inner := trimmed[1 : len(trimmed)-1]
	// Must have at least one colon (key-value separator)
	if !strings.Contains(inner, ":") {
		return false
	}

	// Check if it has identifiers before colons (key: val pattern)
	parts := strings.Split(inner, ",")
	kvCount := 0
	for _, part := range parts {
		part = strings.TrimSpace(part)
		colonIdx := strings.Index(part, ":")
		if colonIdx > 0 {
			key := strings.TrimSpace(part[:colonIdx])
			// Strip any quotes
			key = strings.Trim(key, `"'`)
			if isIdentifier(key) {
				kvCount++
			}
		}
	}

	return kvCount > 0
}

// isIdentifier checks if a string is a valid identifier (alphanumeric + underscore).
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && unicode.IsDigit(r) {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// aggressiveJSONFix tries to fix badly malformed JSON by parsing key:value pairs
// manually and reconstructing valid JSON.
// Handles: {cmd: read, file: main.go} → {"cmd":"read","file":"main.go"}
func aggressiveJSONFix(input string) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) < 3 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return ""
	}

	inner := trimmed[1 : len(trimmed)-1]
	parts := splitRespectingBraces(inner)

	result := make(map[string]interface{})
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		colonIdx := findFirstColonOutsideQuotes(part)
		if colonIdx < 0 {
			continue
		}

		key := strings.TrimSpace(part[:colonIdx])
		val := strings.TrimSpace(part[colonIdx+1:])

		// Clean key
		key = strings.Trim(key, `"'`)

		// Parse value
		result[key] = parseLooseValue(val)
	}

	if len(result) == 0 {
		return ""
	}

	b, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(b)
}

// splitRespectingBraces splits on commas but respects nested braces and quotes.
func splitRespectingBraces(s string) []string {
	var parts []string
	var current strings.Builder
	depth := 0
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if inDouble {
			if ch == '\\' && i+1 < len(s) {
				current.WriteByte(ch)
				current.WriteByte(s[i+1])
				i++
				continue
			}
			if ch == '"' {
				inDouble = false
			}
			current.WriteByte(ch)
			continue
		}
		if inSingle {
			if ch == '\\' && i+1 < len(s) {
				current.WriteByte(ch)
				current.WriteByte(s[i+1])
				i++
				continue
			}
			if ch == '\'' {
				inSingle = false
			}
			current.WriteByte(ch)
			continue
		}

		switch ch {
		case '"':
			inDouble = true
			current.WriteByte(ch)
		case '\'':
			inSingle = true
			current.WriteByte(ch)
		case '{', '[':
			depth++
			current.WriteByte(ch)
		case '}', ']':
			depth--
			current.WriteByte(ch)
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		default:
			current.WriteByte(ch)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// findFirstColonOutsideQuotes finds the index of the first : outside quotes.
func findFirstColonOutsideQuotes(s string) int {
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inDouble {
			if ch == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if ch == '\\' && i+1 < len(s) {
				i++
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
		case ':':
			return i
		}
	}
	return -1
}

// parseLooseValue parses a value that might be unquoted, quoted, boolean, number, or nested.
func parseLooseValue(val string) interface{} {
	val = strings.TrimSpace(val)
	if val == "" {
		return ""
	}

	// Try standard JSON parse first
	var jsonVal interface{}
	if err := json.Unmarshal([]byte(val), &jsonVal); err == nil {
		return jsonVal
	}

	// Single-quoted string
	if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
		return val[1 : len(val)-1]
	}

	// Boolean
	lower := strings.ToLower(val)
	if lower == "true" {
		return true
	}
	if lower == "false" {
		return false
	}
	if lower == "null" || lower == "none" {
		return nil
	}

	// Unquoted string
	return val
}

// defaultFieldForTool maps tool names/subcommands to their primary input field.
// This enables recovery when a model sends just a value instead of a JSON object.
var defaultFieldForTool = map[string]string{
	// Native tool function names
	"read_file":      "file",
	"write_file":     "file",
	"patch_file":     "file",
	"list_directory": "dir",
	"search_files":   "term",
	"run_command":    "cmd",
	"git_status":     "dir",
	"git_diff":       "dir",
	"git_log":        "dir",
	"git_changed":    "dir",
	"git_branch":     "dir",
	"run_tests":      "dir",
	"rollback_file":  "file",
	"clean_backups":  "dir",

	// Engine subcommand names (used in XML mode)
	"read":       "file",
	"write":      "file",
	"patch":      "file",
	"tree":       "dir",
	"search":     "term",
	"exec":       "cmd",
	"git-status": "dir",
	"git-diff":   "dir",
	"git-log":    "dir",
	"git-changed":"dir",
	"git-branch": "dir",
	"test":       "dir",
	"rollback":   "file",
	"clean":      "dir",

	// Generic aliases
	"@coder":  "cmd",
	"coder":   "cmd",
	"bash":    "command",
	"shell":   "command",
	"Bash":    "command",
	"Read":    "file_path",
	"Write":   "file_path",
	"Glob":    "pattern",
	"Grep":    "pattern",
	"Edit":    "file_path",
}

// WrapPlainStringForTool wraps a plain string value into the appropriate JSON
// structure for the given tool. This handles cases where the LLM returns just
// the value instead of a proper JSON object.
//
// This function is conservative — it only wraps values that look like a single
// argument (e.g., a file path or search term). It does NOT wrap CLI-style args
// like "read --file main.go" because those are handled by the CLI parser.
//
// Examples:
//
//	WrapPlainStringForTool("read_file", "main.go") → `{"file":"main.go"}`
//	WrapPlainStringForTool("run_command", "ls -la") → `{"cmd":"ls -la"}`
//	WrapPlainStringForTool("search_files", "TODO") → `{"term":"TODO"}`
func WrapPlainStringForTool(toolName, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	// Don't wrap if it already looks like JSON
	if len(value) > 0 && (value[0] == '{' || value[0] == '[') {
		return ""
	}

	// Don't wrap if it looks like CLI-style args (subcommand + flags).
	// This avoids breaking "read --file main.go" → {"file":"read --file main.go"}.
	if looksLikeCLIArgs(value) {
		return ""
	}

	field, ok := defaultFieldForTool[toolName]
	if !ok {
		// Try lowercase version
		field, ok = defaultFieldForTool[strings.ToLower(toolName)]
	}
	if !ok {
		return ""
	}

	wrapped := map[string]string{field: value}
	b, err := json.Marshal(wrapped)
	if err != nil {
		return ""
	}
	return string(b)
}

// looksLikeCLIArgs returns true if the string appears to be CLI-style arguments,
// e.g., "read --file main.go" or "exec --cmd ls" or "read main.go".
// These should be parsed by the CLI arg parser, not wrapped into JSON.
func looksLikeCLIArgs(s string) bool {
	parts := strings.Fields(s)
	if len(parts) < 2 {
		return false
	}
	// If any part starts with "--" or "-" (flag), it's CLI-style
	for _, p := range parts[1:] {
		if strings.HasPrefix(p, "--") || (strings.HasPrefix(p, "-") && len(p) == 2) {
			return true
		}
	}
	// If the first word is a known subcommand, it's CLI-style even without flags
	// e.g., "read main.go", "exec ls -la", "tree /src"
	knownSubcmds := map[string]bool{
		"read": true, "write": true, "patch": true, "tree": true,
		"search": true, "exec": true, "test": true, "rollback": true, "clean": true,
		"git-status": true, "git-diff": true, "git-log": true,
		"git-changed": true, "git-branch": true,
	}
	if knownSubcmds[strings.ToLower(parts[0])] {
		return true
	}
	return false
}
