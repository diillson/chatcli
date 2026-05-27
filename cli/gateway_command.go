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
 * messages, each conversation gets its own bounded history, and replies are
 * produced by the current model and delivered back. Runs in the foreground
 * until interrupted (Ctrl+C).
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

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
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

	sessions := newGatewaySessions(20)
	runner := gateway.NewRunner(adapters, cli.gatewayAgentFunc(sessions), cli.logger, 0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("gateway.running", strings.Join(names, ", ")))
	if runErr := runner.Run(ctx); runErr != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), runErr)
	}
	fmt.Printf("  %s\n", colorize(i18n.T("gateway.stopped"), ColorGray))
}

// gatewayAgentFunc returns an AgentFunc backed by the current model, with
// per-conversation history kept by sessions.
func (cli *ChatCLI) gatewayAgentFunc(sessions *gatewaySessions) gateway.AgentFunc {
	return func(ctx context.Context, session, text string) (string, error) {
		if cli.Client == nil {
			return "", fmt.Errorf("no active model")
		}
		hist := sessions.get(session)
		hist = append(hist, models.Message{Role: "user", Content: text})
		reply, err := cli.Client.SendPrompt(ctx, text, hist, 0)
		if err != nil {
			return "", err
		}
		hist = append(hist, models.Message{Role: "assistant", Content: reply})
		sessions.set(session, hist)
		return reply, nil
	}
}

// gatewaySessions holds per-conversation history, capped to the most recent
// maxMessages entries so a long-lived daemon does not grow unbounded.
type gatewaySessions struct {
	mu          sync.Mutex
	maxMessages int
	hist        map[string][]models.Message
}

func newGatewaySessions(maxMessages int) *gatewaySessions {
	if maxMessages <= 0 {
		maxMessages = 20
	}
	return &gatewaySessions{maxMessages: maxMessages, hist: map[string][]models.Message{}}
}

func (s *gatewaySessions) get(session string) []models.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.hist[session]
	out := make([]models.Message, len(src))
	copy(out, src)
	return out
}

func (s *gatewaySessions) set(session string, msgs []models.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(msgs) > s.maxMessages {
		msgs = msgs[len(msgs)-s.maxMessages:]
	}
	stored := make([]models.Message, len(msgs))
	copy(stored, msgs)
	s.hist[session] = stored
}
