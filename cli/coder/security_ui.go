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
// formatActionDetails dispatches an incoming tool call into a (label,
// details) pair suitable for the security prompt. The historical
// implementation was one ~120-line switch with cyclomatic complexity
// 34; the rewrite below routes through three small helpers
// (non-@coder, mcp_*, @coder subcmd) so the top-level function stays
// under 15 branches and each subcommand carries its own clearly
// scoped formatter. Adding a new @coder subcmd is now a one-line
// entry in coderSubcmdFormatters instead of another switch arm.
func formatActionDetails(subcmd, toolName, rawArgs string) (label string, details []string) {
	parsed, argsMap := parseToolCallArgs(rawArgs)
	if parsed != "" {
		subcmd = parsed
	}

	toolLower := strings.ToLower(strings.TrimSpace(toolName))

	// Non-@coder plugins: dispatch by tool name.
	if l, d, ok := formatNonCoderTool(toolLower, argsMap, rawArgs); ok {
		return l, d
	}

	// MCP tools have a common shape: label = "MCP: <name>", details = raw args.
	if strings.HasPrefix(toolLower, "mcp_") {
		return formatMCPTool(toolLower, rawArgs)
	}

	// @coder subcommands: look up the formatter and apply it. Unknown
	// subcommands fall through to formatFallback so the user still sees
	// something useful instead of a blank label.
	if spec, ok := coderSubcmdFormatters[subcmd]; ok {
		label = i18n.T(spec.labelKey)
		details = spec.format(argsMap, rawArgs)
	} else {
		label, details = formatFallback(subcmd, toolName, toolLower, rawArgs)
	}

	// Last-resort fallback: every prompt has at least one detail row.
	if len(details) == 0 {
		details = append(details, truncate150(rawArgs))
	}
	return label, details
}

// parseToolCallArgs splits the raw args JSON into (subcmd, argsMap),
// supporting both the nested @coder form `{"cmd":"...","args":{…}}`
// and the flat plugin form `{"url":"…","query":"…"}`. Returns "" for
// subcmd when the input didn't carry one.
func parseToolCallArgs(rawArgs string) (subcmd string, argsMap map[string]interface{}) {
	var nested struct {
		Cmd  string          `json:"cmd"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &nested); err == nil && nested.Cmd != "" {
		_ = json.Unmarshal(nested.Args, &argsMap)
		return nested.Cmd, argsMap
	}
	_ = json.Unmarshal([]byte(rawArgs), &argsMap)
	return "", argsMap
}

// formatNonCoderTool handles the curated set of built-in non-@coder
// plugins. The (_, _, false) return is the signal "I don't recognize
// this tool, let the caller try other dispatch paths".
func formatNonCoderTool(toolLower string, argsMap map[string]interface{}, rawArgs string) (string, []string, bool) {
	switch toolLower {
	case "@webfetch":
		details := []string{}
		if url := extractField(argsMap, rawArgs, "url"); url != "" {
			details = append(details, "URL: "+url)
		}
		if raw := extractField(argsMap, "", "raw"); raw == "true" {
			details = append(details, "mode: raw HTML")
		}
		return "Web Fetch", details, true
	case "@websearch":
		details := []string{}
		if q := extractField(argsMap, rawArgs, "query", "q", "term"); q != "" {
			details = append(details, "query: "+q)
		}
		return "Web Search", details, true
	}
	return "", nil, false
}

// formatMCPTool renders MCP plugin invocations. The raw args are
// truncated rather than parsed because MCP tool schemas are arbitrary
// — we'd need a per-tool formatter to do better.
func formatMCPTool(toolLower, rawArgs string) (string, []string) {
	mcpName := strings.TrimPrefix(toolLower, "mcp_")
	display := truncate150(rawArgs)
	var details []string
	if display != "" {
		details = []string{display}
	}
	return "MCP: " + mcpName, details
}

// formatFallback handles unknown @coder subcommands and bare tool
// invocations. Splits the label-decision out of formatActionDetails
// so the top function never has to think about "what's the right
// label" — only "do I have a formatter for this subcmd or not".
func formatFallback(subcmd, toolName, toolLower, rawArgs string) (string, []string) {
	switch {
	case toolName != "" && toolLower != "@coder":
		return toolName, []string{truncate150(rawArgs)}
	case subcmd != "":
		return subcmd, []string{truncate150(rawArgs)}
	default:
		return i18n.T("coder.security.action.unknown"), []string{truncate150(rawArgs)}
	}
}

// truncate150 caps a string at 150 chars + ellipsis. The 150 limit
// keeps the security prompt from spilling past the terminal width
// when a raw-args dump carries a long command line; 150 is the
// historical cap, so we preserve it byte-for-byte.
func truncate150(s string) string {
	if len(s) > 150 {
		return s[:150] + "..."
	}
	return s
}

// coderSubcmdFormatter is the table-driven spec for one @coder
// subcommand. labelKey is the i18n key that produces the human-
// readable verb; format builds the detail rows from the parsed args.
type coderSubcmdFormatter struct {
	labelKey string
	format   func(args map[string]interface{}, raw string) []string
}

// coderSubcmdFormatters owns the @coder-subcmd dispatch table. Adding
// a new subcommand is one entry here; deleting one is one removal.
// The previous design grew the parent switch by 8-12 lines per
// subcmd, which is what pushed it past the cyclo budget.
var coderSubcmdFormatters = map[string]coderSubcmdFormatter{
	"exec":   {labelKey: "coder.security.action.exec", format: detailsExec},
	"test":   {labelKey: "coder.security.action.test", format: detailsTest},
	"write":  {labelKey: "coder.security.action.write", format: detailsFile},
	"patch":  {labelKey: "coder.security.action.patch", format: detailsFile},
	"read":   {labelKey: "coder.security.action.read", format: detailsFile},
	"search": {labelKey: "coder.security.action.search", format: detailsSearch},
	"tree":   {labelKey: "coder.security.action.tree", format: detailsTree},
}

func detailsExec(args map[string]interface{}, raw string) []string {
	return cmdWithOptionalDir(args, raw, []string{"cwd", "dir", "workdir"})
}

func detailsTest(args map[string]interface{}, raw string) []string {
	return cmdWithOptionalDir(args, raw, []string{"dir", "cwd", "workdir"})
}

// cmdWithOptionalDir factors the exec/test detail layout: a `$ <cmd>`
// row plus an optional `dir: <dir>` row. The exec and test arms used
// to be near-duplicates in the parent switch; sharing this helper is
// what keeps the cyclo budget tight even as the @coder surface grows.
func cmdWithOptionalDir(args map[string]interface{}, raw string, dirKeys []string) []string {
	var out []string
	if cmd := extractField(args, raw, "cmd", "command"); cmd != "" {
		out = append(out, "$ "+cmd)
	}
	if dir := extractField(args, "", dirKeys...); dir != "" {
		out = append(out, "dir: "+dir)
	}
	return out
}

func detailsFile(args map[string]interface{}, raw string) []string {
	if file := extractField(args, raw, "file", "path", "filepath"); file != "" {
		return []string{i18n.T("coder.security.detail.file") + ": " + file}
	}
	return nil
}

func detailsSearch(args map[string]interface{}, raw string) []string {
	var out []string
	if term := extractField(args, raw, "term", "pattern", "query"); term != "" {
		out = append(out, i18n.T("coder.security.detail.term")+": "+term)
	}
	if dir := extractField(args, "", "dir"); dir != "" {
		out = append(out, "dir: "+dir)
	}
	return out
}

func detailsTree(args map[string]interface{}, raw string) []string {
	if dir := extractField(args, raw, "dir", "path"); dir != "" {
		return []string{"dir: " + dir}
	}
	return nil
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
