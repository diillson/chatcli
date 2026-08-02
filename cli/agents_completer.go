/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/i18n"
)

// agentsSubcommandSuggestions returns /agents subcommands (built per call so
// i18n resolves with the active locale).
func agentsSubcommandSuggestions() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "list", Description: i18n.T("complete.agents.list")},
		{Text: "show", Description: i18n.T("complete.agents.show")},
		{Text: "cancel", Description: i18n.T("complete.agents.cancel")},
		{Text: "help", Description: i18n.T("complete.agents.help")},
	}
}

// getAgentsSuggestions completes /agents subcommands and live run IDs.
func (cli *ChatCLI) getAgentsSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/agents")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix(agentsSubcommandSuggestions(), current, true)
	}

	switch args[0] {
	case "show", "cancel":
		if len(args) == 1 || (len(args) == 2 && !trailingSpace) {
			var suggestions []prompt.Suggest
			for _, info := range runs.Default().Active() {
				suggestions = append(suggestions, prompt.Suggest{
					Text:        info.ID,
					Description: string(info.Kind) + "/" + info.Agent + " — " + truncateForUI(info.Task, 40),
				})
			}
			if args[0] == "show" {
				for _, info := range runs.Default().Recent(5) {
					suggestions = append(suggestions, prompt.Suggest{
						Text:        info.ID,
						Description: string(info.Status) + " — " + truncateForUI(info.Task, 40),
					})
				}
			}
			return prompt.FilterHasPrefix(suggestions, current, true)
		}
	}
	return nil
}
