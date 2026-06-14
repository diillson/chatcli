/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package cmd — gateway.go
 *
 * Implements the `chatcli gateway` subcommand: the foreground body of the
 * messaging daemon. The interactive `/gateway start` re-execs this in a
 * detached child so the REPL stays free. Here we own our own process and
 * stdout, so capturing the agent's output never touches a live terminal.
 *
 * The typewriter animation is disabled (output is captured and streamed to the
 * chat) and the agent runs fully unattended via RunGatewayForeground.
 */
package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/llm/manager"
	"go.uber.org/zap"
)

// RunGateway runs the messaging gateway in the foreground (the detached child).
func RunGateway(args []string, mgr manager.LLMManager, logger *zap.Logger) error {
	// Animation is meaningless when piped to a log/capture; disable it so the
	// streamed feed stays clean.
	_ = os.Setenv("CHATCLI_NO_TYPEWRITER", "1")

	chatCLI, err := cli.NewChatCLI(context.Background(), mgr, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("gateway: serving")
	return chatCLI.RunGatewayForeground(ctx)
}
