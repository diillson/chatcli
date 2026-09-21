/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

// dashSubcommandSuggestions is built per call so i18n resolves with the
// active locale.
func dashSubcommandSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "open", Description: i18n.T("complete.dash.open")},
		{Text: "url", Description: i18n.T("complete.dash.url")},
		{Text: "status", Description: i18n.T("complete.dash.status")},
		{Text: "off", Description: i18n.T("complete.dash.off")},
	}
}

// getDashSuggestions completes the /dash subcommand.
func (cli *ChatCLI) getDashSuggestions(d prompt.Document) []prompt.Suggest {
	args := strings.Fields(d.TextBeforeCursor())
	trailing := strings.HasSuffix(d.TextBeforeCursor(), " ")
	if len(args) == 1 && trailing {
		return dashSubcommandSuggestions()
	}
	if len(args) == 2 && !trailing {
		return prompt.FilterHasPrefix(dashSubcommandSuggestions(), d.GetWordBeforeCursor(), true)
	}
	return nil
}
