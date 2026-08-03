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

// mailSubcommandSuggestions returns /mail subcommands (per call so i18n
// resolves with the active locale).
func mailSubcommandSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "list", Description: i18n.T("complete.mail.list")},
		{Text: "send", Description: i18n.T("complete.mail.send")},
		{Text: "pending", Description: i18n.T("complete.mail.pending")},
		{Text: "help", Description: i18n.T("complete.mail.help")},
	}
}

// mailRecipientSuggestions lists the addressable squad members.
func mailRecipientSuggestions() []prompt.Suggest {
	recipients := []string{
		"orchestrator", "coder", "reviewer", "tester", "planner", "refactor",
		"search", "file", "shell", "git", "diagnostics", "formatter", "deps",
	}
	out := make([]prompt.Suggest, 0, len(recipients))
	for _, r := range recipients {
		out = append(out, prompt.Suggest{Text: r})
	}
	return out
}

// getMailSuggestions completes /mail subcommands and recipients.
func (cli *ChatCLI) getMailSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/mail")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix(mailSubcommandSuggestions(), current, true)
	}
	if args[0] == "send" && (len(args) == 1 || (len(args) == 2 && !trailingSpace)) {
		return prompt.FilterHasPrefix(mailRecipientSuggestions(), current, true)
	}
	return nil
}
