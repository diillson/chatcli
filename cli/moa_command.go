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
	"github.com/diillson/chatcli/llm/client"
)

func (cli *ChatCLI) handleMoACommand(input string) {
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

	factory := func(provider, model string) (moa.Client, error) {
		// Shared resolver: case-insensitive provider match + reuse of the live
		// session client so OAuth / forwarded-token auth is honored (same as the
		// @moa tool). Plain GetClient here failed on lowercase env names like
		// "openai" because the registry keys are upper-case.
		c, err := cli.moaClientFor(provider, model)
		if err != nil {
			return nil, err
		}
		var _ client.LLMClient = c // documents that LLMClient satisfies moa.Client
		return c, nil
	}

	fmt.Printf("  %s\n", colorize(i18n.T("moa.running", len(refs)), ColorCyan))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	final, results, err := moa.Run(ctx, prompt, refs, factory, aggregator)
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
}
