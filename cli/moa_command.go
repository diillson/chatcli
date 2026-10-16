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
 *   CHATCLI_MOA_LAYERS=1..4           (cross-review rounds; /moa --layers N per run)
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
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
	layers, prompt, err := parseMoALayers(strings.TrimSpace(strings.TrimPrefix(input, "/moa")))
	if err != nil {
		printMoAUsage()
		return err
	}
	if prompt == "" {
		printMoAUsage()
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

	if layers > 1 {
		fmt.Printf("  %s\n", colorize(i18n.T("moa.layers.running", len(refs), layers), ColorCyan))
	} else {
		fmt.Printf("  %s\n", colorize(i18n.T("moa.running", len(refs)), ColorCyan))
	}
	if caps := moaToolsetLabel(toolset); caps != "" {
		fmt.Printf("  %s\n", colorize(i18n.T("moa.tools", caps), ColorGray))
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(layers)*moaLayerTimeout)
	defer cancel()
	// A /moa is a turn of this session like a chat message: the panel, its
	// members and their requests hang from it on the live dashboard.
	runCtx, turnSpan := pulse.BeginCtx(runCtx, pulse.KindTurn, pulseTurnMoA, "")
	turnSpan.With("members", strconv.Itoa(len(refs))).With("layers", strconv.Itoa(layers)).
		With("aggregator", strings.ToLower(aggregator.Provider)+":"+aggregator.Model)
	defer turnSpan.End(pulse.StatusCancelled)

	// Each member's position prints as soon as it lands, round by round;
	// the synthesis follows once the last round is done.
	start := time.Now()
	hooks := cli.moaPanelHooks(moaPanelMode(), start)
	hooks.OnSynthesis = func() {
		fmt.Printf("  %s\n", colorize(i18n.T("moa.panel.synthesizing", moa.Ref{Provider: strings.ToLower(aggregator.Provider), Model: aggregator.Model}.String()), ColorCyan))
	}

	out, err := moa.RunLayers(runCtx, prompt, tempHistory, refs, aggregator, turn, moa.Config{Layers: layers, Hooks: hooks})
	turnSpan.EndErr(err)
	if err != nil {
		return err
	}
	final := out.Final

	// The panel is briefed with the chat prompt, coder handoff included: a
	// proposal in the aggregated answer is stripped and offered exactly as
	// on a regular chat turn, never printed or stored as a raw tag.
	handoffTask, final := takeCoderHandoff(final)

	fmt.Println()
	cli.renderMoAEnvelope(final, aggregator, out, time.Since(start))

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

// moaLayerTimeout bounds one round of a /moa run.
const moaLayerTimeout = 5 * time.Minute

// errMoALayers reports a --layers value that is not a number.
var errMoALayers = errors.New("/moa --layers needs a number")

// moaLayersFromEnv reads CHATCLI_MOA_LAYERS (1 when unset or invalid).
func moaLayersFromEnv() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CHATCLI_MOA_LAYERS")))
	if err != nil {
		return 1
	}
	return moa.ClampLayers(n)
}

// parseMoALayers takes a leading "--layers N" (or "--layers=N") off a /moa
// prompt; without one the layer count comes from CHATCLI_MOA_LAYERS.
func parseMoALayers(args string) (int, string, error) {
	rest, ok := strings.CutPrefix(args, "--layers")
	if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '=') {
		return moaLayersFromEnv(), args, nil
	}
	rest = strings.TrimLeft(rest, " =")
	value, prompt, _ := strings.Cut(rest, " ")
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, "", errMoALayers
	}
	return moa.ClampLayers(n), strings.TrimSpace(prompt), nil
}

// printMoAUsage prints the /moa usage, layers included.
func printMoAUsage() {
	fmt.Println(colorize("  "+i18n.T("moa.usage"), ColorGray))
	fmt.Println(colorize("  "+i18n.T("moa.layers.usage"), ColorGray))
}
