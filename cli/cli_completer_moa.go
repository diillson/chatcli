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

// getMoASuggestions completes /moa: the --layers flag, then its value.
func (cli *ChatCLI) getMoASuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()
	typing := !strings.HasSuffix(line, " ")

	if len(args) == 1 && typing {
		return []prompt.Suggest{{Text: "/moa", Description: i18n.T("complete.root.moa")}}
	}
	if len(args) == 1 || (len(args) == 2 && typing) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "--layers", Description: i18n.T("complete.moa.layers")},
		}, word, true)
	}
	if args[1] == "--layers" && (len(args) == 2 || (len(args) == 3 && typing)) {
		return prompt.FilterHasPrefix([]prompt.Suggest{
			{Text: "2", Description: i18n.T("complete.moa.layers_2")},
			{Text: "3", Description: i18n.T("complete.moa.layers_3")},
			{Text: "1", Description: i18n.T("complete.moa.layers_1")},
		}, word, true)
	}
	return []prompt.Suggest{}
}
