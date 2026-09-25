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

func webSubcommandSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "open", Description: i18n.T("complete.web.open")},
		{Text: "url", Description: i18n.T("complete.web.url")},
		{Text: "status", Description: i18n.T("complete.web.status")},
		{Text: "off", Description: i18n.T("complete.web.off")},
	}
}

// getWebSuggestions completes the /web subcommand.
func (cli *ChatCLI) getWebSuggestions(d prompt.Document) []prompt.Suggest {
	args := strings.Fields(d.TextBeforeCursor())
	trailing := strings.HasSuffix(d.TextBeforeCursor(), " ")
	if len(args) == 1 && trailing {
		return webSubcommandSuggestions()
	}
	if len(args) == 2 && !trailing {
		return prompt.FilterHasPrefix(webSubcommandSuggestions(), d.GetWordBeforeCursor(), true)
	}
	return nil
}
