/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - gateway_command.go
 *
 * /gateway [start|stop|status] runs ChatCLI as a messaging daemon. To keep the
 * interactive REPL free, `start` re-execs the binary as a detached child
 * (`chatcli gateway`) — its own process, its own stdout — and tracks it via a
 * pidfile + log under ~/.chatcli/. The child runs RunGatewayForeground.
 *
 * In the daemon, each inbound message runs through the real agent loop fully
 * unattended (no stdin confirmations; full autonomy — the operator opted in).
 * Progress streams back as a short, filtered action feed and the run closes
 * with the model's clean prose answer. Access control is at the edge: Telegram
 * allow-list, Slack signing secret, webhook secret, plus the agent security
 * mode (CHATCLI_AGENT_SECURITY_MODE).
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/i18n"
)

// SetUnattended toggles fully non-interactive agent execution (used by the
// gateway daemon). When set, the agent never prompts for confirmation.
func (cli *ChatCLI) SetUnattended(v bool) { cli.unattended = v }

func (cli *ChatCLI) handleGatewayCommand(input string) {
	sub := strings.TrimSpace(strings.TrimPrefix(input, "/gateway"))
	switch sub {
	case "", "start":
		cli.gatewayStartDetached()
	case "stop":
		cli.gatewayStop()
	case "status", "platforms":
		cli.gatewayStatus()
	default:
		fmt.Println(colorize("  "+i18n.T("gateway.usage"), ColorGray))
	}
}

// gatewayStatus reports the running daemon (if any) and which platforms are
// registered/configured.
func (cli *ChatCLI) gatewayStatus() {
	names := gateway.RegisteredNames()
	adapters, _ := gateway.BuildConfigured()
	if pid, ok := gatewayRunningPID(); ok {
		fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("gateway.status_running", pid))
	} else {
		fmt.Printf("  %s %s\n", colorize("--", ColorGray), i18n.T("gateway.status_stopped"))
	}
	fmt.Printf("  %s %s\n", colorize(i18n.T("gateway.registered"), ColorYellow), strings.Join(names, ", "))
	fmt.Printf("  %s %d\n", colorize(i18n.T("gateway.configured"), ColorYellow), len(adapters))
}

// gatewayStartDetached re-execs `chatcli gateway` as a detached background
// process so the interactive REPL stays free, tracking it via a pidfile.
func (cli *ChatCLI) gatewayStartDetached() {
	if pid, ok := gatewayRunningPID(); ok {
		fmt.Printf("  %s %s\n", colorize("--", ColorYellow), i18n.T("gateway.already_running", pid))
		return
	}

	adapters, err := gateway.BuildConfigured()
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	if len(adapters) == 0 {
		fmt.Println(colorize("  "+i18n.T("gateway.no_platforms"), ColorYellow))
		return
	}
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	logPath := gatewayStatePath("gateway.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o750)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- daemon-scoped
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "gateway") // #nosec G204 -- exe is self, no user args
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = gatewayDetachAttr()
	if err := cmd.Start(); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	if err := os.WriteFile(gatewayStatePath("gateway.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		cli.logger.Warn("gateway: could not write pidfile")
	}

	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen),
		i18n.T("gateway.started_detached", cmd.Process.Pid, strings.Join(names, ", "), logPath))
}

// gatewayStop signals the detached daemon to terminate and clears the pidfile.
func (cli *ChatCLI) gatewayStop() {
	pid, ok := gatewayRunningPID()
	if !ok {
		fmt.Printf("  %s %s\n", colorize("--", ColorGray), i18n.T("gateway.status_stopped"))
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	if err := gatewayTerminate(proc); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	_ = os.Remove(gatewayStatePath("gateway.pid"))
	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("gateway.stopped_pid", pid))
}

// RunGatewayForeground builds the configured adapters and runs the messaging
// runner in the foreground until ctx is cancelled. It is the body of the
// detached `chatcli gateway` subcommand; the agent runs fully unattended.
func (cli *ChatCLI) RunGatewayForeground(ctx context.Context) error {
	cli.SetUnattended(true)

	adapters, err := gateway.BuildConfigured()
	if err != nil {
		return err
	}
	if len(adapters) == 0 {
		return fmt.Errorf("%s", i18n.T("gateway.no_platforms"))
	}
	sessions := newGatewaySessions(5)
	runner := gateway.NewRunner(adapters, cli.gatewayAgentFunc(sessions), cli.logger, 0)
	return runner.Run(ctx)
}

// gatewayAgentFunc returns an AgentFunc that runs each inbound message through
// the real (unattended) agent loop, streaming a short filtered action feed via
// the ctx emitter and returning the model's clean prose answer. Runs serialize
// because the agent mutates shared ChatCLI state (history, lastAgentReply) and
// redirects os.Stdout for capture.
func (cli *ChatCLI) gatewayAgentFunc(sessions *gatewaySessions) gateway.AgentFunc {
	var mu sync.Mutex
	return func(ctx context.Context, session, text string) (string, error) {
		if cli.Client == nil {
			return "", fmt.Errorf("no active model")
		}

		mu.Lock()
		defer mu.Unlock()

		task := text
		if pre := sessions.preamble(session); pre != "" {
			task = pre + "\n\nCurrent request: " + text
		}

		emit := gateway.Progress(ctx)
		var lastSent string
		stream := func(line string) {
			s := gatewayCleanLine(line)
			if s == "" || s == lastSent { // drop noise and consecutive duplicates
				return
			}
			lastSent = s
			emit(s)
		}
		if _, err := cli.RunAgentStreaming(ctx, task, stream); err != nil {
			return "", err
		}

		sessions.remember(session, text)

		// The clean prose answer was captured (and not printed) during the run.
		if reply := strings.TrimSpace(cli.lastAgentReply); reply != "" {
			return reply, nil
		}
		return "✅ " + i18n.T("gateway.task_done"), nil
	}
}

// gatewayCleanLine trims a streamed line, strips box-drawing/decorative runes,
// and drops anything left without letters or digits, so the chat sees concise
// action lines instead of UI chrome.
func gatewayCleanLine(line string) string {
	// Strip box-drawing, block and arrow decoration that surrounds agent UI.
	cleaned := strings.Map(func(r rune) rune {
		if r == '\r' {
			return -1
		}
		if r >= 0x2500 && r <= 0x259F { // box drawing + block elements
			return -1
		}
		return r
	}, line)

	s := strings.TrimSpace(cleaned)
	if s == "" {
		return ""
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return s
		}
	}
	return ""
}

// ── daemon state helpers ───────────────────────────────────────

// gatewayStatePath returns ~/.chatcli/<name>, falling back to the temp dir.
func gatewayStatePath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), name)
	}
	return filepath.Join(home, ".chatcli", name)
}

// gatewayRunningPID returns the daemon PID if the pidfile points at a live
// process, clearing a stale pidfile otherwise.
func gatewayRunningPID() (int, bool) {
	data, err := os.ReadFile(gatewayStatePath("gateway.pid")) // #nosec G304 -- daemon-scoped
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !gatewayProcessAlive(pid) {
		_ = os.Remove(gatewayStatePath("gateway.pid"))
		return 0, false
	}
	return pid, true
}

// gatewaySessions keeps a small rolling list of recent user requests per
// conversation, so a long-lived daemon does not grow unbounded while still
// giving the agent continuity across turns.
type gatewaySessions struct {
	mu       sync.Mutex
	maxItems int
	recent   map[string][]string
}

func newGatewaySessions(maxItems int) *gatewaySessions {
	if maxItems <= 0 {
		maxItems = 5
	}
	return &gatewaySessions{maxItems: maxItems, recent: map[string][]string{}}
}

// preamble renders the recent user requests as context for the next run, or ""
// when the conversation is new.
func (s *gatewaySessions) preamble(session string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.recent[session]
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Earlier in this conversation the user asked:")
	for _, it := range items {
		b.WriteString("\n- ")
		b.WriteString(it)
	}
	return b.String()
}

// remember records a user request, keeping only the most recent maxItems.
func (s *gatewaySessions) remember(session, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := append(s.recent[session], text)
	if len(items) > s.maxItems {
		items = items[len(items)-s.maxItems:]
	}
	s.recent[session] = items
}
