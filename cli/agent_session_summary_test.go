/*
 * ChatCLI - coverage for the agent/coder session telemetry summary.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestEmitSessionSummary(t *testing.T) {
	cli := &ChatCLI{costTracker: NewCostTracker()}
	cli.costTracker.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500, IsReal: true,
	})
	a := &AgentMode{cli: cli}
	a.emitSessionSummary() // main path: tokens + cost

	cli.unattended = true
	a.emitSessionSummary() // unattended → no-op

	// Nothing tracked → early return.
	(&AgentMode{cli: &ChatCLI{costTracker: NewCostTracker()}}).emitSessionSummary()
}
