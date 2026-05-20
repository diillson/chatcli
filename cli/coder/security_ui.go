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

	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/i18n"
	"golang.org/x/term"
)

type SecurityDecision int

const (
	DecisionRunOnce SecurityDecision = iota
	DecisionAllowAlways
	DecisionDenyOnce
	DecisionDenyForever
	DecisionCanceled // user pressed Ctrl+C; action can be retried later
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

// detectTerminalWidth returns the live terminal column count by
// probing /dev/tty (the controlling terminal) when stdin/stdout are
// piped or otherwise non-tty. Falls back to 80 cols. We need this
// because the security prompt runs at a point where stdout may have
// been wrapped by another layer (animation, captured-pipe debug)
// and term.GetSize on the wrong fd returns 0.
func detectTerminalWidth() int {
	if runtime.GOOS == "windows" {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			return w
		}
		return 80
	}
	for _, fd := range []uintptr{os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd()} {
		if w, _, err := term.GetSize(int(fd)); err == nil && w > 0 {
			return w
		}
	}
	// Last resort: open /dev/tty directly.
	if tty, err := os.Open("/dev/tty"); err == nil {
		defer func() { _ = tty.Close() }()
		if w, _, err := term.GetSize(int(tty.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// resetTTYToSane restores the controlling terminal to canonical mode with
// echo on. We invoke `stty sane` against /dev/tty (NOT the parent process's
// stdin, which exec.Command silently rewires to /dev/null when cmd.Stdin is
// nil — making stty a no-op against the wrong fd). This matters after the
// park auto-resume path, where go-prompt's Setup/TearDown cycle around the
// TIOCSTI-injected line can leave the terminal in raw or echo-off state,
// causing the next security prompt to silently swallow the user's keys.
//
// Returns true when the reset was applied. Errors are intentionally
// swallowed: the prompt is best-effort UX and any failure here just
// degrades to the previous (broken-on-resume) behavior.
func resetTTYToSane() bool {
	if runtime.GOOS == "windows" || sttyPath == "" {
		return false
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = tty.Close() }()

	cmd := exec.Command(sttyPath, "sane")
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	return cmd.Run() == nil
}

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
	resetTTYToSane()

	purple := "\u001b[35m"
	cyan := "\u001b[36m"
	yellow := "\u001b[33m"
	green := "\u001b[32m"
	red := "\u001b[31m"
	gray := "\u001b[90m"
	bold := "\u001b[1m"
	white := "\u001b[37m"
	reset := "\u001b[0m"

	// --- Parse the action up front so the box knows what to display ---
	sub, _ := NormalizeCoderArgs(args)
	actionLabel, details := formatActionDetails(sub, toolName, args)
	pattern := GetSuggestedPattern(toolName, args)
	isExecCmd := pattern == ""

	// Build the entire prompt body INSIDE a single bordered card so
	// title, agent context, action, rule, and choices all read as
	// one cohesive panel. The previous design drew a separate ╔══╗
	// banner just for the title and then left every detail line as
	// loose text below the box, which the user (rightly) called
	// "disorganized". One card, one visual statement.
	var b strings.Builder
	b.WriteString(bold + purple + "🔒 " + i18n.T("coder.security.header") + reset + "\n")
	b.WriteString("\n")

	// --- Agent context (parallel mode) ---
	if secCtx != nil && secCtx.AgentName != "" {
		b.WriteString(fmt.Sprintf("%s🤖 Agent%s  %s%s%s%s\n",
			gray, reset, gray+"·  "+reset, cyan+bold, secCtx.AgentName, reset))
		if secCtx.TaskDesc != "" {
			taskDisplay := secCtx.TaskDesc
			if len(taskDisplay) > 120 {
				taskDisplay = taskDisplay[:120] + "..."
			}
			b.WriteString(fmt.Sprintf("%s📋 %s%s  %s%s%s%s\n",
				gray, i18n.T("coder.security.task"), reset, gray+"·  "+reset, white, taskDisplay, reset))
		}
		b.WriteString("\n")
	}

	// --- Action + details ---
	b.WriteString(fmt.Sprintf("%s⚡ %s%s  %s%s%s%s\n",
		gray, i18n.T("coder.security.action"), reset, gray+"·  "+reset, yellow+bold, actionLabel, reset))
	for _, d := range details {
		b.WriteString(fmt.Sprintf("              %s%s%s\n", cyan, d, reset))
	}

	// --- Policy rule info ---
	var ruleVal string
	if isExecCmd {
		ruleVal = i18n.T("coder.security.exec_requires_approval")
	} else {
		ruleVal = i18n.T("coder.security.no_rule_for", pattern)
	}
	b.WriteString(fmt.Sprintf("%s📜 %s%s  %s%s%s%s\n",
		gray, i18n.T("coder.security.rule"), reset, gray+"·  "+reset, gray, ruleVal, reset))

	b.WriteString("\n")

	// --- Choices ---
	b.WriteString(bold + i18n.T("coder.security.choose") + ":" + reset + "\n")
	b.WriteString(fmt.Sprintf("  [%s] %s\n", green+"y"+reset, i18n.T("coder.security.yes_once")))
	if !isExecCmd {
		b.WriteString(fmt.Sprintf("  [%s] %s\n", green+"a"+reset, i18n.T("coder.security.allow_always", pattern)))
	}
	b.WriteString(fmt.Sprintf("  [%s] %s\n", red+"n"+reset, i18n.T("coder.security.no_skip")))
	if !isExecCmd {
		b.WriteString(fmt.Sprintf("  [%s] %s", red+"d"+reset, i18n.T("coder.security.deny_always", pattern)))
	}
	// No final "\n" intentionally: trailing newline would force lipgloss
	// to render an empty padding row before the bottom border.

	// Render the body in a single rounded box. Purple = the security
	// channel color across the renderer; matches the prompt arrow on
	// the next line. We cap width to the live terminal so a long
	// command like `cp /Users/.../foo.go /Users/.../bar.go` doesn't
	// blow the box past the right edge — lipgloss wraps inside the
	// box at the configured width.
	termWidth := detectTerminalWidth()
	maxBox := termWidth - 2
	if maxBox < 40 {
		maxBox = 40
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("5")).
		Padding(0, 2).
		Width(maxBox - 2). // -2 for the border chars (lipgloss includes them in Width)
		Render(strings.TrimRight(b.String(), "\n"))

	fmt.Println()
	fmt.Println(box)
	fmt.Print(purple + " > " + reset)

	// Read user input either from the centralized stdin channel (if provided)
	// or via a fallback goroutine. Using inputCh avoids orphaned goroutines
	// that steal stdin after Ctrl+C cancels the context.
	var input string
	if inputCh != nil {
		select {
		case <-ctx.Done():
			fmt.Println("\n" + red + " [" + i18n.T("coder.security.canceled") + "]" + reset)
			return DecisionCanceled
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
			fmt.Println("\n" + red + " [" + i18n.T("coder.security.canceled") + "]" + reset)
			return DecisionCanceled
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
