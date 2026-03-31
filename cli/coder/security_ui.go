package coder

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/mattn/go-runewidth"
)

type SecurityDecision int

const (
	DecisionRunOnce SecurityDecision = iota
	DecisionAllowAlways
	DecisionDenyOnce
	DecisionDenyForever
	DecisionCancelled // user pressed Ctrl+C; action can be retried later
)

// SecurityContext provides optional metadata for richer security prompts.
// When provided, the prompt shows which agent is requesting the action and why.
type SecurityContext struct {
	AgentName string // e.g., "shell", "coder", "tester"
	TaskDesc  string // natural language task description
}

// sttyPath is resolved once at init to avoid PATH manipulation attacks.
var sttyPath = func() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if p, err := exec.LookPath("stty"); err == nil {
		return p
	}
	return ""
}()

// PromptSecurityCheck prompts the user for a security decision (no agent context).
func PromptSecurityCheck(ctx context.Context, toolName, args string, inputCh <-chan string) SecurityDecision {
	return PromptSecurityCheckWithContext(ctx, toolName, args, nil, inputCh)
}

// PromptSecurityCheckWithContext prompts the user with full context about what
// is being attempted, which agent is requesting it, and the parsed command details.
// When inputCh is provided, input is read from the channel instead of spawning a
// goroutine with bufio.Scanner on stdin. This avoids orphaned goroutines that steal
// stdin from go-prompt after agent mode exits (e.g., on Ctrl+C).
func PromptSecurityCheckWithContext(ctx context.Context, toolName, args string, secCtx *SecurityContext, inputCh <-chan string) SecurityDecision {
	if runtime.GOOS != "windows" && sttyPath != "" {
		_ = exec.Command(sttyPath, "sane").Run()
	}

	purple := "\u001b[35m"
	cyan := "\u001b[36m"
	yellow := "\u001b[33m"
	green := "\u001b[32m"
	red := "\u001b[31m"
	gray := "\u001b[90m"
	bold := "\u001b[1m"
	white := "\u001b[37m"
	reset := "\u001b[0m"

	fmt.Println()
	boxWidth := 58 // inner width between ║ borders
	headerText := "🔒 " + i18n.T("coder.security.header")
	// Calculate visible width using go-runewidth (handles emojis, CJK, etc.)
	visLen := runewidth.StringWidth(headerText)
	padTotal := boxWidth - visLen
	if padTotal < 0 {
		padTotal = 0
	}
	padLeft := padTotal / 2
	padRight := padTotal - padLeft
	paddedHeader := strings.Repeat(" ", padLeft) + headerText + strings.Repeat(" ", padRight)

	fmt.Println(purple + bold + "╔" + strings.Repeat("═", boxWidth) + "╗" + reset)
	fmt.Println(purple + bold + "║" + paddedHeader + "║" + reset)
	fmt.Println(purple + bold + "╚" + strings.Repeat("═", boxWidth) + "╝" + reset)

	// --- Agent context (parallel mode) ---
	if secCtx != nil && secCtx.AgentName != "" {
		fmt.Printf(" %s🤖 Agent:%s  %s%s%s\n", gray, reset, cyan+bold, secCtx.AgentName, reset)
		if secCtx.TaskDesc != "" {
			taskDisplay := secCtx.TaskDesc
			if len(taskDisplay) > 120 {
				taskDisplay = taskDisplay[:120] + "..."
			}
			fmt.Printf(" %s📋 %s:%s %s%s%s\n", gray, i18n.T("coder.security.task"), reset, white, taskDisplay, reset)
		}
		fmt.Println(gray + " " + strings.Repeat("─", 58) + reset)
	}

	// --- Parse and display the action in human-readable form ---
	sub, _ := NormalizeCoderArgs(args)
	actionLabel, details := formatActionDetails(sub, toolName, args)

	fmt.Printf(" %s⚡ %s:%s   %s%s%s\n", gray, i18n.T("coder.security.action"), reset, yellow+bold, actionLabel, reset)
	for _, d := range details {
		fmt.Printf("           %s%s%s\n", cyan, d, reset)
	}

	// --- Policy rule info ---
	pattern := GetSuggestedPattern(toolName, args)
	isExecCmd := pattern == ""

	if isExecCmd {
		fmt.Printf(" %s📜 %s:%s  %s%s%s\n", gray, i18n.T("coder.security.rule"), reset, gray, i18n.T("coder.security.exec_requires_approval"), reset)
	} else {
		fmt.Printf(" %s📜 %s:%s  %s%s%s\n", gray, i18n.T("coder.security.rule"), reset, gray, i18n.T("coder.security.no_rule_for", pattern), reset)
	}

	fmt.Println(gray + " " + strings.Repeat("─", 58) + reset)

	// --- Choices ---
	fmt.Println(bold + " " + i18n.T("coder.security.choose") + ":" + reset)
	fmt.Printf("   [%s] %s\n", green+"y"+reset, i18n.T("coder.security.yes_once"))
	if !isExecCmd {
		fmt.Printf("   [%s] %s\n", green+"a"+reset, i18n.T("coder.security.allow_always", pattern))
	}
	fmt.Printf("   [%s] %s\n", red+"n"+reset, i18n.T("coder.security.no_skip"))
	if !isExecCmd {
		fmt.Printf("   [%s] %s\n", red+"d"+reset, i18n.T("coder.security.deny_always", pattern))
	}

	fmt.Print("\n" + purple + " > " + reset)

	// Read user input either from the centralized stdin channel (if provided)
	// or via a fallback goroutine. Using inputCh avoids orphaned goroutines
	// that steal stdin after Ctrl+C cancels the context.
	var input string
	if inputCh != nil {
		select {
		case <-ctx.Done():
			fmt.Println("\n" + red + " [" + i18n.T("coder.security.cancelled") + "]" + reset)
			return DecisionCancelled
		case line := <-inputCh:
			input = strings.TrimSpace(strings.ToLower(line))
		}
	} else {
		resultChan := make(chan string, 1)
		go func() {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				resultChan <- strings.TrimSpace(strings.ToLower(scanner.Text()))
			} else {
				resultChan <- ""
			}
		}()
		select {
		case <-ctx.Done():
			_ = os.Stdin.SetReadDeadline(time.Now())
			fmt.Println("\n" + red + " [" + i18n.T("coder.security.cancelled") + "]" + reset)
			return DecisionCancelled
		case input = <-resultChan:
		}
	}

	if isExecCmd {
		switch input {
		case "", "y", "yes", "s", "sim":
			return DecisionRunOnce
		case "n", "no", "nao", "não":
			return DecisionDenyOnce
		default:
			// Unknown input (garbled terminal, paste artifacts, etc.)
			// Deny once to avoid unintended execution.
			fmt.Println(yellow + " [" + i18n.T("coder.security.invalid_input") + "]" + reset)
			return DecisionDenyOnce
		}
	}
	switch input {
	case "", "y", "yes", "s", "sim":
		return DecisionRunOnce
	case "a", "always":
		return DecisionAllowAlways
	case "n", "no", "nao", "não":
		return DecisionDenyOnce
	case "d", "deny":
		return DecisionDenyForever
	default:
		// Unknown input (garbled terminal, paste artifacts, etc.)
		// Deny once to avoid unintended execution.
		fmt.Println(yellow + " [" + i18n.T("coder.security.invalid_input") + "]" + reset)
		return DecisionDenyOnce
	}
}

// formatActionDetails parses the raw tool args and returns a human-readable
// action label and detail lines for the security prompt.
// toolName is the plugin name (e.g. "@coder", "@webfetch", "@websearch", "mcp_tool").
func formatActionDetails(subcmd, toolName, rawArgs string) (label string, details []string) {
	// Try parsing JSON args (flat or nested @coder format)
	var parsed struct {
		Cmd  string          `json:"cmd"`
		Args json.RawMessage `json:"args"`
	}
	var argsMap map[string]interface{}

	if err := json.Unmarshal([]byte(rawArgs), &parsed); err == nil && parsed.Cmd != "" {
		subcmd = parsed.Cmd
		_ = json.Unmarshal(parsed.Args, &argsMap)
	} else {
		// Flat JSON args (non-@coder plugins): {"url":"...", "query":"..."}
		_ = json.Unmarshal([]byte(rawArgs), &argsMap)
	}

	// --- Non-@coder plugins: display by toolName ---
	toolLower := strings.ToLower(strings.TrimSpace(toolName))

	switch toolLower {
	case "@webfetch":
		label = "Web Fetch"
		if url := extractField(argsMap, rawArgs, "url"); url != "" {
			details = append(details, "URL: "+url)
		}
		if raw := extractField(argsMap, "", "raw"); raw == "true" {
			details = append(details, "mode: raw HTML")
		}
		return

	case "@websearch":
		label = "Web Search"
		if q := extractField(argsMap, rawArgs, "query", "q", "term"); q != "" {
			details = append(details, "query: "+q)
		}
		return
	}

	// MCP tools
	if strings.HasPrefix(toolLower, "mcp_") {
		mcpName := strings.TrimPrefix(toolLower, "mcp_")
		label = "MCP: " + mcpName
		display := rawArgs
		if len(display) > 150 {
			display = display[:150] + "..."
		}
		if display != "" {
			details = append(details, display)
		}
		return
	}

	// --- @coder subcommands ---
	switch subcmd {
	case "exec":
		label = i18n.T("coder.security.action.exec")
		cmd := extractField(argsMap, rawArgs, "cmd", "command")
		if cmd != "" {
			details = append(details, "$ "+cmd)
		}
		if dir := extractField(argsMap, "", "cwd", "dir", "workdir"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	case "test":
		label = i18n.T("coder.security.action.test")
		cmd := extractField(argsMap, rawArgs, "cmd", "command")
		if cmd != "" {
			details = append(details, "$ "+cmd)
		}
		if dir := extractField(argsMap, "", "dir", "cwd", "workdir"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	case "write":
		label = i18n.T("coder.security.action.write")
		file := extractField(argsMap, rawArgs, "file", "path", "filepath")
		if file != "" {
			details = append(details, i18n.T("coder.security.detail.file")+": "+file)
		}

	case "patch":
		label = i18n.T("coder.security.action.patch")
		file := extractField(argsMap, rawArgs, "file", "path", "filepath")
		if file != "" {
			details = append(details, i18n.T("coder.security.detail.file")+": "+file)
		}

	case "read":
		label = i18n.T("coder.security.action.read")
		file := extractField(argsMap, rawArgs, "file", "path", "filepath")
		if file != "" {
			details = append(details, i18n.T("coder.security.detail.file")+": "+file)
		}

	case "search":
		label = i18n.T("coder.security.action.search")
		term := extractField(argsMap, rawArgs, "term", "pattern", "query")
		if term != "" {
			details = append(details, i18n.T("coder.security.detail.term")+": "+term)
		}
		if dir := extractField(argsMap, "", "dir"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	case "tree":
		label = i18n.T("coder.security.action.tree")
		if dir := extractField(argsMap, rawArgs, "dir", "path"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	default:
		// For unknown tools, use the tool name as label
		if toolName != "" && toolLower != "@coder" {
			label = toolName
		} else if subcmd != "" {
			label = subcmd
		} else {
			label = i18n.T("coder.security.action.unknown")
		}
		display := rawArgs
		if len(display) > 150 {
			display = display[:150] + "..."
		}
		details = append(details, display)
	}

	if len(details) == 0 {
		// Fallback: show truncated raw args
		display := rawArgs
		if len(display) > 150 {
			display = display[:150] + "..."
		}
		details = append(details, display)
	}

	return label, details
}

// extractField tries to get a string value from the parsed args map using
// multiple possible key names. Falls back to parsing from raw CLI-style args.
func extractField(argsMap map[string]interface{}, rawArgs string, keys ...string) string {
	// Try from parsed JSON map first
	if argsMap != nil {
		for _, k := range keys {
			if v, ok := argsMap[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}

	// Try CLI-style: --key value (join all remaining tokens after the flag)
	if rawArgs != "" {
		fields := strings.Fields(rawArgs)
		for _, k := range keys {
			flag := "--" + k
			for i, f := range fields {
				if f == flag && i+1 < len(fields) {
					val := strings.Join(fields[i+1:], " ")
					// Strip surrounding quotes (shell artifacts from CLI-style args)
					if len(val) >= 2 {
						if (val[0] == '\'' && val[len(val)-1] == '\'') || (val[0] == '"' && val[len(val)-1] == '"') {
							val = val[1 : len(val)-1]
						}
					}
					return val
				}
			}
		}
	}

	return ""
}
