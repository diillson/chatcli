/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// finalizeSpend persists the session's cost snapshot and daily spend and
// releases provider cache resources (Gemini explicit caches bill storage
// per hour). The REPL runs it from cleanup; one-shot, the gateway daemon
// and the MCP/ACP servers call it on their own exit so no surface leaves
// spend unsaved or a paid cache lingering.
// FinalizeSpend is finalizeSpend for the command layer (rpcserve exits).
func (cli *ChatCLI) FinalizeSpend(ctx context.Context) { cli.settleSpendOnExit(ctx) }

func (cli *ChatCLI) settleSpendOnExit(ctx context.Context) {
	if cli == nil {
		return
	}
	if cli.costTracker != nil {
		if err := cli.costTracker.SaveSession(); err != nil && cli.logger != nil {
			cli.logger.Debug("cost snapshot not saved on exit", zap.Error(err))
		}
		cli.costTracker.FlushDailySpend()
	}
	if n := client.ReleaseCacheResources(ctx); n > 0 && cli.logger != nil {
		cli.logger.Info("released provider cache resources on exit", zap.Int("count", n))
	}
}
