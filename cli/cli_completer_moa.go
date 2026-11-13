/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

// getMoASuggestions completes /moa: list or the per-run flags, a flag's
// value (providers for --models/--aggregator), then the next flag.
func (cli *ChatCLI) getMoASuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()
	typing := !strings.HasSuffix(line, " ")

	if len(args) == 1 && typing {
		return []prompt.Suggest{{Text: "/moa", Description: i18n.T("complete.root.moa")}}
	}
	// Flags come in "--flag value" pairs before the prompt: nothing is
	// offered past the first token that is not part of one.
	done := args[1:]
	if typing {
		done = done[:len(done)-1]
	}
	for i := 0; i < len(done); i += 2 {
		if !strings.HasPrefix(done[i], "--") {
			return []prompt.Suggest{}
		}
	}
	if len(done)%2 == 0 {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "--layers", Description: i18n.T("complete.moa.layers")},
			{Text: "--models", Description: i18n.T("complete.moa.models")},
			{Text: "--aggregator", Description: i18n.T("complete.moa.aggregator")},
			{Text: "--positions", Description: i18n.T("complete.moa.positions")},
			{Text: "list", Description: i18n.T("complete.moa.list")},
		}, word, true)
	}
	flag := done[len(done)-1]
	if flag == "--layers" {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "2", Description: i18n.T("complete.moa.layers_2")},
			{Text: "3", Description: i18n.T("complete.moa.layers_3")},
			{Text: "1", Description: i18n.T("complete.moa.layers_1")},
		}, word, true)
	}
	out := []prompt.Suggest{}
	if flag == "--positions" {
		out = []prompt.Suggest{{Text: moaPanelLead}, {Text: moaPanelFull}, {Text: moaPanelOff}}
	} else if cli.manager != nil && (flag == "--models" || flag == "--aggregator") {
		for _, p := range cli.manager.GetAvailableProviders() {
			out = append(out, prompt.Suggest{Text: strings.ToLower(p)})
		}
	}
	return prompt.FilterHasPrefix(out, word, true)
}
