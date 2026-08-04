/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - moa_command.go
 *
 * /moa <prompt> runs Mixture-of-Agents: fan the prompt out to several
 * reference models in parallel and synthesize their answers with an
 * aggregator model. Provider-agnostic — any configured provider can be a
 * reference or the aggregator.
 *
 * Every participant is briefed like a regular chat turn (structured system
 * prompt: attached contexts, workspace memory, skills, retrieval) and holds
 * the chat-sanctioned read-only tool exceptions (knowledge retrieval, CCR
 * recall) — see moa_turn.go.
 *
 *   CHATCLI_MOA_MODELS="openai:gpt-5,claudeai:claude-opus-4-8,googleai:gemini-2.5-pro"
 *   CHATCLI_MOA_AGGREGATOR="claudeai:claude-opus-4-8"   (defaults to current model)
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

func (cli *ChatCLI) handleMoACommand(ctx context.Context, input string) {
	prompt := strings.TrimSpace(strings.TrimPrefix(input, "/moa"))
	if prompt == "" {
		fmt.Println(colorize("  "+i18n.T("moa.usage"), ColorGray))
		return
	}

	refs := moa.ParseRefs(os.Getenv("CHATCLI_MOA_MODELS"))
	if len(refs) == 0 {
		// No env configured → behave like the @moa tool: use the configured
		// providers (capped) so /moa works out of the box.
		refs = cli.defaultMoaRefs()
	}
	if len(refs) == 0 {
		fmt.Println(colorize("  "+i18n.T("moa.no_models"), ColorYellow))
		return
	}

	aggregator := moa.Ref{Provider: cli.Provider, Model: cli.Model}
	if agg := moa.ParseRefs(os.Getenv("CHATCLI_MOA_AGGREGATOR")); len(agg) > 0 {
		aggregator = agg[0]
	}

	// Every panel member gets the same briefing a regular chat turn gets: the
	// structured system prompt (attached contexts, workspace memory, skills,
	// retrieval) plus the prior conversation. Assembled ONCE per run — it
	// consumes per-turn staging state (e.g. a pending manual skill) — and
	// shared read-only by all participants.
	assembly := cli.assembleChatSystemPrompt(ctx, prompt, "")
	tempHistory := cli.buildChatTempHistory(assembly.parts, prompt, "", nil)

	// Panel members also get the chat-sanctioned read-only tool exceptions
	// (knowledge retrieval, CCR marker recall) when they apply to this session.
	toolset := cli.moaToolsetForRun(tempHistory)
	turn := cli.moaTurn(toolset)

	fmt.Printf("  %s\n", colorize(i18n.T("moa.running", len(refs)), ColorCyan))
	if caps := moaToolsetLabel(toolset); caps != "" {
		fmt.Printf("  %s\n", colorize(i18n.T("moa.tools", caps), ColorGray))
	}

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	final, results, err := moa.RunSession(runCtx, prompt, tempHistory, refs, aggregator, turn)
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}

	// Show which references contributed.
	for _, r := range results {
		status := colorize("✓", ColorGreen)
		if !r.OK() {
			status = colorize("✗", ColorRed)
		}
		fmt.Printf("    %s %s\n", status, colorize(r.Ref.String(), ColorGray))
	}
	fmt.Println()
	fmt.Println(cli.renderMarkdown(final))

	// Record the turn in history so a follow-up /moa or a normal message has the
	// context — same as a regular chat turn. Mirror onto the cross-channel hub.
	// Adopt other surfaces' writes BEFORE recording+persisting: /moa is a
	// turn like any other, and persisting without the refresh would clobber
	// a bound session's newer file with our stale local history.
	cli.refreshBoundSession()
	cli.history = append(cli.history, models.Message{Role: "user", Content: prompt})
	cli.history = append(cli.history, models.Message{Role: "assistant", Content: final})
	cli.mirrorHubTurn(ctx, prompt, final)
	cli.persistBoundSession()
}
