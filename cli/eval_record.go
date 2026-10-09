/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/trajectory"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/evals"
	"go.uber.org/zap"
)

// One-shot modes as the eval record reports them.
const (
	evalModeChat  = "chat"
	evalModeCoder = "coder"
	evalModeAgent = "agent"
)

// writeEvalRecord reports this one-shot run to the eval harness when it
// asked for it (CHATCLI_EVAL_RECORD names the file). Unset — every run that
// is not driven by `chatcli eval` — it does nothing at all.
func (cli *ChatCLI) writeEvalRecord(mode string, runErr error) {
	path := strings.TrimSpace(os.Getenv(evals.RecordEnv))
	if path == "" {
		return
	}
	rec := cli.buildEvalRecord(mode, runErr)
	if err := evals.WriteRecord(path, rec); err != nil && cli.logger != nil {
		cli.logger.Warn("eval record not written", zap.String("path", path), zap.Error(err))
	}
}

// buildEvalRecord assembles the run's answer, tool trail and spend.
func (cli *ChatCLI) buildEvalRecord(mode string, runErr error) *evals.Record {
	// The model id, not the display name: reports compare runs by it.
	rec := &evals.Record{Mode: mode, Provider: cli.Provider, Model: cli.Model}
	if rec.Model == "" && cli.Client != nil {
		rec.Model = cli.Client.GetModelName()
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}

	var lastAssistant string
	for _, m := range cli.history {
		if m.Role != "assistant" {
			continue
		}
		rec.Turns++
		lastAssistant = m.Content
		rec.ToolCalls = append(rec.ToolCalls, evalToolCalls(m)...)
	}
	switch mode {
	case evalModeChat:
		rec.Final = strings.TrimSpace(lastAssistant)
	default:
		// The ReAct loop keeps the last prose answer, tool-call markup
		// stripped, in lastAgentReply; the raw history message would carry
		// the markup.
		rec.Final = strings.TrimSpace(cli.lastAgentReply)
		if rec.Final == "" {
			rec.Final = strings.TrimSpace(lastAssistant)
		}
	}

	if cli.costTracker != nil {
		snap := cli.costTracker.Snapshot()
		rec.CostUSD = snap.TotalCostUSD
		rec.Requests = snap.TotalRequests
		for _, u := range snap.ModelUsage {
			in := u.InputTokens
			if in == 0 {
				in = u.PromptTokens
			}
			rec.InputTokens += in
			rec.OutputTokens += u.CompletionTokens
		}
	}

	for _, t := range trajectory.ToTurns(cli.history) {
		rec.Transcript = append(rec.Transcript, evals.Transcript{From: t.From, Value: t.Value})
	}
	return rec
}

// evalToolCalls lists the tools an assistant message invoked, whether as
// native tool calls or as the XML tool-call markup the ReAct loop parses.
func evalToolCalls(m models.Message) []evals.ToolCall {
	var out []evals.ToolCall
	for _, tc := range m.ToolCalls {
		out = append(out, evals.ToolCall{Name: tc.Name, Args: tc.ArgumentsJSON()})
	}
	if len(m.ToolCalls) == 0 && m.Content != "" {
		if calls, err := agent.ParseToolCalls(m.Content); err == nil {
			for _, tc := range calls {
				out = append(out, evals.ToolCall{Name: tc.Name, Args: tc.Args})
			}
		}
	}
	return out
}

// evalModeFor names the one-shot route an input takes.
func evalModeFor(input string, coderRoute bool) string {
	switch {
	case coderRoute, strings.HasPrefix(input, "/coder "):
		return evalModeCoder
	case strings.HasPrefix(input, "/agent "), strings.HasPrefix(input, "/run "):
		return evalModeAgent
	}
	return evalModeChat
}
