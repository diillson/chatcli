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
 *   CHATCLI_MOA_PANEL=lead|full|off   (each member's position, see moa_panel.go)
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

func (cli *ChatCLI) handleMoACommand(ctx context.Context, input string) {
	if err := cli.runMoACommand(ctx, input); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
	}
}

// errMoAUsage and errMoANoModels are reported by runMoACommand after the
// hint was printed, so a one-shot run exits non-zero.
var (
	errMoAUsage    = errors.New("/moa needs a prompt")
	errMoANoModels = errors.New("no MoA models configured")
)

// runMoACommand runs one /moa turn; shared by the REPL and one-shot (-p).
func (cli *ChatCLI) runMoACommand(ctx context.Context, input string) error {
	prompt := strings.TrimSpace(strings.TrimPrefix(input, "/moa"))
	if prompt == "" {
		fmt.Println(colorize("  "+i18n.T("moa.usage"), ColorGray))
		return errMoAUsage
	}

	refs := moa.ParseRefs(os.Getenv("CHATCLI_MOA_MODELS"))
	if len(refs) == 0 {
		// No env configured → behave like the @moa tool: use the configured
		// providers (capped) so /moa works out of the box.
		refs = cli.defaultMoaRefs()
	}
	if len(refs) == 0 {
		fmt.Println(colorize("  "+i18n.T("moa.no_models"), ColorYellow))
		return errMoANoModels
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

	// Each member's position prints as soon as it lands; the synthesis
	// follows once the slowest member is done.
	var landed atomic.Int32
	start := time.Now()
	printMember := cli.moaPanelPrinter(moaPanelMode(), start)
	observer := func(r moa.RefResult) {
		printMember(r)
		if int(landed.Add(1)) == len(refs) {
			fmt.Printf("  %s\n", colorize(i18n.T("moa.panel.synthesizing", moa.Ref{Provider: strings.ToLower(aggregator.Provider), Model: aggregator.Model}.String()), ColorCyan))
		}
	}

	final, results, err := moa.RunSessionObserved(runCtx, prompt, tempHistory, refs, aggregator, turn, observer)
	if err != nil {
		return err
	}

	// The panel is briefed with the chat prompt, coder handoff included: a
	// proposal in the aggregated answer is stripped and offered exactly as
	// on a regular chat turn, never printed or stored as a raw tag.
	handoffTask, final := takeCoderHandoff(final)

	fmt.Println()
	cli.renderMoAEnvelope(final, aggregator, results, time.Since(start))

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
	cli.offerCoderHandoff(handoffTask)
	return nil
}
