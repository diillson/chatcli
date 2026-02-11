package coder

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/diillson/chatcli/utils"
)

// payloadKeys are keys excluded from the normalized string because they contain
// large payloads (file content, encoded data) that are irrelevant for policy
// pattern matching and would prevent rules like "@coder write --file /etc"
// from matching correctly (since --content would appear before --file
// alphabetically).
var payloadKeys = map[string]bool{
	"content":  true,
	"contents": true,
	"data":     true,
	"encoding": true,
	"replace":  true,
}

// NormalizeCoderArgs parses raw tool call args (JSON or CLI format) and returns:
//   - subcommand: the extracted subcommand name (e.g., "read", "exec")
//   - normalized: the full normalized CLI-style string with sorted flags
//     (e.g., "read --file main.go") suitable for deterministic prefix matching.
//
// When the subcommand cannot be determined, both return values are empty.
// This is a safe default because Check() will fall through to ActionAsk.
func NormalizeCoderArgs(args string) (subcommand string, normalized string) {
	trimmed := strings.TrimSpace(html.UnescapeString(args))
	if trimmed == "" {
		return "", ""
	}

	if unescaped, ok := utils.MaybeUnescapeJSONishArgs(trimmed); ok {
		trimmed = unescaped
	}

	// Try JSON parsing
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return normalizeJSON(trimmed)
	}

	// CLI format: first token is subcommand, rest are args
	return normalizeCLI(trimmed)
}

func normalizeJSON(trimmed string) (string, string) {
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", ""
	}

	switch v := payload.(type) {
	case map[string]any:
		return normalizeJSONMap(v)
	case []any:
		return normalizeJSONArray(v)
	}

	return "", ""
}

func normalizeJSONMap(m map[string]any) (string, string) {
	// Extract cmd
	cmd := ""
	if c, ok := m["cmd"].(string); ok && c != "" {
		cmd = c
	} else if argv, ok := m["argv"].([]any); ok && len(argv) > 0 {
		if first, ok := argv[0].(string); ok {
			cmd = first
		}
	}

	if cmd == "" {
		return "", ""
	}

	// If argv is present, use it directly (already ordered)
	if argvRaw, ok := m["argv"].([]any); ok && len(argvRaw) > 0 {
		parts := make([]string, 0, len(argvRaw))
		for _, item := range argvRaw {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		// Ensure cmd is first
		if len(parts) > 0 && parts[0] != cmd {
			parts = append([]string{cmd}, parts...)
		}
		return cmd, strings.Join(parts, " ")
	}

	// Build from args/flags maps
	argsMap := make(map[string]any)

	if raw, ok := m["args"]; ok {
		if mm, ok := raw.(map[string]any); ok {
			for k, v := range mm {
				if k == "command" {
					argsMap["cmd"] = v
					continue
				}
				argsMap[k] = v
			}
		}
	}
	if raw, ok := m["flags"]; ok {
		if mm, ok := raw.(map[string]any); ok {
			for k, v := range mm {
				if k == "command" {
					argsMap["cmd"] = v
					continue
				}
				argsMap[k] = v
			}
		}
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(argsMap))
	for k := range argsMap {
		if payloadKeys[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := []string{cmd}
	for _, k := range keys {
		v := argsMap[k]
		switch val := v.(type) {
		case bool:
			if val {
				parts = append(parts, "--"+k)
			}
		case string:
			parts = append(parts, "--"+k, val)
		case float64:
			parts = append(parts, "--"+k, fmt.Sprintf("%g", val))
		default:
			parts = append(parts, "--"+k, fmt.Sprint(val))
		}
	}

	return cmd, strings.Join(parts, " ")
}

func normalizeJSONArray(arr []any) (string, string) {
	if len(arr) == 0 {
		return "", ""
	}

	parts := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			parts = append(parts, s)
		}
	}

	if len(parts) == 0 {
		return "", ""
	}

	return parts[0], strings.Join(parts, " ")
}

func normalizeCLI(trimmed string) (string, string) {
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", ""
	}
	return fields[0], trimmed
}
