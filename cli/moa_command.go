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
 * Per run: --models a,b  --aggregator <provider[:model]>  --positions
 * lead|full|off  --layers N; "/moa list" shows who can take part.
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
	opts, err := parseMoAOptions(strings.TrimSpace(strings.TrimPrefix(input, "/moa")))
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		printMoAUsage()
		return err
	}
	layers, prompt := opts.layers, opts.prompt
	if prompt == "" {
		printMoAUsage()
		return errMoAUsage
	}
	if prompt == "list" {
		out, err := (&moaPluginAdapter{cli: cli}).List(ctx)
		if err == nil {
			fmt.Println(out)
		}
		return err
	}
	if opts.layersAsked != 0 {
		fmt.Println(colorize("  "+i18n.T("moa.cmd.layers_clamped", opts.layersAsked, layers, moa.MaxLayers), ColorYellow))
	}

	refs := opts.models
	if len(refs) == 0 {
		refs = moa.ParseRefs(os.Getenv("CHATCLI_MOA_MODELS"))
	}
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
	if opts.aggregator != nil {
		aggregator = *opts.aggregator
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
	hooks := cli.moaPanelHooks(opts.positions, start)
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

// moaRunOptions is one /moa invocation: the per-run flags, then the prompt.
// Anything a flag does not set keeps its env/session default.
type moaRunOptions struct {
	layers      int
	layersAsked int // the --layers value when it had to be clamped, else 0
	models      []moa.Ref
	aggregator  *moa.Ref
	positions   string
	prompt      string
}

// parseMoAOptions takes the leading "--flag value" (or "--flag=value") pairs
// off a /moa line; the rest is the prompt. An unknown flag is an error, not
// prompt text: "--model x question" would go to the default panel verbatim
// and bill for it. A lone "--" ends the flags for a dashed prompt.
func parseMoAOptions(args string) (moaRunOptions, error) {
	opts := moaRunOptions{layers: moaLayersFromEnv(), positions: moaPanelMode()}
	rest := strings.TrimSpace(args)
	for strings.HasPrefix(rest, "--") {
		head, tail, _ := strings.Cut(rest, " ")
		rest = strings.TrimSpace(tail)
		if head == "--" {
			break
		}
		name, value, inline := strings.Cut(head, "=")
		if !inline {
			value, tail, _ = strings.Cut(rest, " ")
			rest = strings.TrimSpace(tail)
		}
		if value == "" || strings.HasPrefix(value, "--") {
			return opts, errors.New(i18n.T("execparity.flag_needs_value", name))
		}
		if err := opts.set(name, value); err != nil {
			return opts, err
		}
	}
	opts.prompt = rest
	return opts, nil
}

// set applies one flag.
func (o *moaRunOptions) set(name, value string) error {
	switch name {
	case "--layers":
		n, err := strconv.Atoi(value)
		if err != nil {
			return errMoALayers
		}
		if o.layers = moa.ClampLayers(n); o.layers != n {
			o.layersAsked = n
		}
	case "--models":
		// "a, b q" splits at the space: panel "a", prompt "b q", silently.
		if strings.HasSuffix(value, ",") {
			return errors.New(i18n.T("moa.cmd.models_trailing_comma", value))
		}
		if o.models = moa.ParseRefs(value); len(o.models) == 0 {
			return errors.New(i18n.T("execparity.flag_needs_value", name))
		}
	case "--aggregator":
		refs := moa.ParseRefs(value)
		if len(refs) != 1 {
			return errors.New(i18n.T("moa.cmd.bad_aggregator", value))
		}
		o.aggregator = &refs[0]
	case "--positions":
		switch mode := strings.ToLower(value); mode {
		case moaPanelLead, moaPanelFull, moaPanelOff:
			o.positions = mode
		default:
			return errors.New(i18n.T("moa.cmd.bad_positions", value))
		}
	default:
		return errors.New(i18n.T("moa.cmd.unknown_flag", name))
	}
	return nil
}

// printMoAUsage prints the /moa usage, layers included.
func printMoAUsage() {
	fmt.Println(colorize("  "+i18n.T("moa.usage"), ColorGray))
	fmt.Println(colorize("  "+i18n.T("moa.layers.usage"), ColorGray))
	fmt.Println(colorize("  "+i18n.T("moa.cmd.flags_usage"), ColorGray))
}
