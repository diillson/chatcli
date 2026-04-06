/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const defaultMaxCommandOutput = 100 * 1024 // 100KB

// promptInjectionPatterns are strings commonly used in prompt injection attacks.
var promptInjectionPatterns = []string{
	"ignore previous instructions",
	"ignore all previous",
	"ignore the above",
	"disregard previous",
	"disregard all previous",
	"forget your instructions",
	"forget the above",
	"new instructions:",
	"system:",
	"<|im_start|>system",
	"<|im_end|>",
	"<|im_sep|>",
	"</s>",
	"<s>",
	"[INST]",
	"[/INST]",
	"<<SYS>>",
	"<</SYS>>",
	"Human:",
	"Assistant:",
	"### Instruction:",
	"### Response:",
	"BEGININSTRUCTION",
	"ENDINSTRUCTION",
	"you are now",
	"act as if",
	"pretend you are",
	"roleplay as",
	"you must obey",
	"override your",
}

// SanitizeCommandOutput wraps command output with delimiters, enforces size limits,
// and detects potential prompt injection attempts.
func SanitizeCommandOutput(cmd, output string) string {
	maxSize := getMaxCommandOutputSize()

	// Truncate if necessary
	truncated := false
	if len(output) > maxSize {
		output = output[:maxSize]
		truncated = true
	}

	// Check for prompt injection
	injectionDetected, patterns := DetectPromptInjection(output)

	var b strings.Builder
	if injectionDetected {
		b.WriteString(fmt.Sprintf("[WARNING: Output may contain prompt injection attempts. Detected patterns: %s]\n", strings.Join(patterns, ", ")))
	}

	// Wrap with explicit delimiters to separate data from instructions
	b.WriteString(fmt.Sprintf("<COMMAND_OUTPUT cmd=%q>\n", cmd))
	b.WriteString(output)
	if truncated {
		b.WriteString(fmt.Sprintf("\n[TRUNCATED: output exceeded %d bytes]", maxSize))
	}
	b.WriteString("\n</COMMAND_OUTPUT>")

	return b.String()
}

// DetectPromptInjection scans text for common prompt injection patterns.
// Returns (detected, list of matched patterns).
func DetectPromptInjection(text string) (bool, []string) {
	lower := strings.ToLower(text)
	var matched []string

	for _, pattern := range promptInjectionPatterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			matched = append(matched, pattern)
		}
	}

	return len(matched) > 0, matched
}

func getMaxCommandOutputSize() int {
	if v := os.Getenv("CHATCLI_MAX_COMMAND_OUTPUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxCommandOutput
}
