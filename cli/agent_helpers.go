/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/config"
)

// allowedEditors is the set of known safe editors for the EDITOR env var.
var allowedEditors = map[string]bool{
	"vim": true, "vi": true, "nvim": true, "nano": true, "emacs": true,
	"code": true, "subl": true, "micro": true, "helix": true, "hx": true,
	"ed": true, "pico": true, "joe": true, "ne": true, "kate": true,
	"gedit": true, "kwrite": true, "notepad++": true, "atom": true,
}

// resolveEditor validates and resolves the EDITOR environment variable.
// It returns the resolved absolute path, or an error if the editor is
// unknown or cannot be found in PATH.
func resolveEditor() (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}

	base := filepath.Base(editor)
	if !allowedEditors[base] {
		return "", fmt.Errorf("editor %q is not in the allowed list; set EDITOR to one of: vim, nvim, nano, emacs, code, subl, micro, helix", base)
	}

	resolved, err := exec.LookPath(editor)
	if err != nil {
		return "", fmt.Errorf("editor %q not found in PATH: %w", editor, err)
	}
	return resolved, nil
}

func isCoderMinimalUI() bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_UI")))
	if val == "" || val == "full" || val == "false" || val == "0" {
		return false
	}
	if val == "minimal" || val == "min" || val == "true" || val == "1" || val == "compact" {
		return true
	}
	return false
}

// isCoderCompactUI returns true when the user wants the aru-style ultra-compact UI:
//
//	✓ Read(main.go) 1.2s
//	✓ Write(handler.go) 0.3s
//
// Set via CHATCLI_CODER_UI=compact
func isCoderCompactUI() bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_UI")))
	return val == "compact"
}

// extractSubcmdFromArgs extracts the coder subcommand from raw tool call args.
// Works with both JSON ({"cmd":"read",...}) and CLI-style ("read --file main.go") args.
func extractSubcmdFromArgs(argsStr string) string {
	// Try JSON format
	type jsonCmd struct {
		Cmd string `json:"cmd"`
	}
	var jc jsonCmd
	if err := json.Unmarshal([]byte(argsStr), &jc); err == nil && jc.Cmd != "" {
		return jc.Cmd
	}

	// CLI-style: first word
	parts := strings.Fields(argsStr)
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

func isCoderBannerEnabled() bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_BANNER")))
	if val == "" || val == "true" || val == "1" || val == "yes" {
		return true
	}
	if val == "false" || val == "0" || val == "no" {
		return false
	}
	return true
}

func compactText(input string, maxLines int, maxLen int) string {
	lines := strings.Split(input, "\n")
	out := make([]string, 0, maxLines)
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
		if len(out) >= maxLines {
			break
		}
	}
	joined := strings.Join(out, " · ")
	if maxLen > 0 && len(joined) > maxLen {
		joined = joined[:maxLen] + "..."
	}
	return joined
}

func findLastMeaningfulLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "```") {
			return line
		}
	}
	return ""
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// detectHeredocs verifica presença de heredocs
func detectHeredocs(script string) bool {
	heredocPattern := regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?`)
	return heredocPattern.MatchString(script)
}

// isShellScript determina se o conteúdo é um script shell
func isShellScript(content string) bool {
	return detectHeredocs(content) ||
		strings.Contains(content, "#!/bin/") ||
		regexp.MustCompile(`if\s+.*\s+then`).MatchString(content) ||
		regexp.MustCompile(`for\s+.*\s+in\s+.*\s+do`).MatchString(content) ||
		regexp.MustCompile(`while\s+.*\s+do`).MatchString(content) ||
		regexp.MustCompile(`case\s+.*\s+in`).MatchString(content) ||
		strings.Contains(content, "function ") ||
		strings.Count(content, "{") > 1 && strings.Count(content, "}") > 1
}

func AgentMaxTurns() int {
	value := os.Getenv(config.AgentPluginMaxTurnsEnv)
	if value == "" {
		return config.DefaultAgentMaxTurns
	}

	turns, err := strconv.Atoi(value)
	if err != nil {
		return config.DefaultAgentMaxTurns
	}

	if turns <= 0 {
		return config.DefaultAgentMaxTurns
	}

	if turns > config.MaxAgentMaxTurns {
		return config.MaxAgentMaxTurns
	}

	return turns
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func hasAnyNewline(s string) bool {
	return strings.Contains(s, "\n") || strings.Contains(s, "\r")
}

func truncateForUI(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
