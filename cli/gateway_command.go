/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - gateway_command.go
 *
 * /gateway [start|status] runs ChatCLI as a messaging daemon. Configured
 * platform adapters (e.g. Telegram via CHATCLI_TELEGRAM_BOT_TOKEN) receive
 * messages, each one is run through the real agent loop (tools, shell and file
 * edits — auto-executed), its progress is streamed back to the chat as it
 * works, and a completion notice closes the turn. Runs in the foreground until
 * interrupted (Ctrl+C).
 *
 * Because the agent auto-executes, gate who can reach the bot: Telegram
 * allow-list (CHATCLI_TELEGRAM_ALLOWED_USERS), Slack signing secret, webhook
 * secret, plus the agent security mode (CHATCLI_AGENT_SECURITY_MODE).
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"unicode"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/i18n"
)

func (cli *ChatCLI) handleGatewayCommand(input string) {
	sub := strings.TrimSpace(strings.TrimPrefix(input, "/gateway"))
	switch sub {
	case "", "start":
		cli.startGateway()
	case "status", "platforms":
		names := gateway.RegisteredNames()
		adapters, _ := gateway.BuildConfigured()
		fmt.Printf("  %s %s\n", colorize(i18n.T("gateway.registered"), ColorYellow), strings.Join(names, ", "))
		fmt.Printf("  %s %d\n", colorize(i18n.T("gateway.configured"), ColorYellow), len(adapters))
	default:
		fmt.Println(colorize("  "+i18n.T("gateway.usage"), ColorGray))
	}
}

func (cli *ChatCLI) startGateway() {
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

	sessions := newGatewaySessions(5)
	runner := gateway.NewRunner(adapters, cli.gatewayAgentFunc(sessions), cli.logger, 0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("gateway.running", strings.Join(names, ", ")))
	if runErr := runner.Run(ctx); runErr != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), runErr)
	}
	fmt.Printf("  %s\n", colorize(i18n.T("gateway.stopped"), ColorGray))
}

// gatewayAgentFunc returns an AgentFunc that runs each inbound message through
// the real agent loop, streaming the agent's rendered progress back to the
// chat (decorative-only lines filtered out) and returning a completion notice.
// A light per-conversation context (recent user requests) is prepended so the
// agent keeps continuity across turns; durable state otherwise lives in the
// workspace files the agent operates on.
func (cli *ChatCLI) gatewayAgentFunc(sessions *gatewaySessions) gateway.AgentFunc {
	return func(ctx context.Context, session, text string) (string, error) {
		if cli.Client == nil {
			return "", fmt.Errorf("no active model")
		}

		task := text
		if pre := sessions.preamble(session); pre != "" {
			task = pre + "\n\nCurrent request: " + text
		}

		emit := gateway.Progress(ctx)
		stream := func(line string) {
			if s := gatewayCleanLine(line); s != "" {
				emit(s)
			}
		}
		if _, err := cli.RunAgentStreaming(ctx, task, stream); err != nil {
			return "", err
		}

		sessions.remember(session, text)
		return "✅ " + i18n.T("gateway.task_done"), nil
	}
}

// gatewayCleanLine trims a streamed line and drops purely decorative output
// (box-drawing rules, spinners) that has no letters or digits, so the chat sees
// substantive progress instead of UI chrome.
func gatewayCleanLine(line string) string {
	s := strings.TrimSpace(line)
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
