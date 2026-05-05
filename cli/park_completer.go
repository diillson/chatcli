/*
 * park_completer.go — go-prompt suggestions for /parked, /resume, and
 * /cancel-park. Mirrors the pattern in scheduler_completer.go: a flat
 * suggestion table for subcommands, plus dynamic token completion that
 * reads the on-disk snapshot directory.
 */
package cli

import (
	"strings"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/i18n"
)

// parkedSubcommandSuggestions lists the verbs /parked accepts after
// the leading slash command.
var parkedSubcommandSuggestions = []prompt.Suggest{
	{Text: "prune", Description: "remove snapshots whose scheduler job is in a terminal state"},
	{Text: "gc", Description: "remove snapshots older than <duration> (e.g. 24h, 7d)"},
	{Text: "help", Description: "show /parked usage"},
}

// getParkedSuggestions handles /parked, /parked <sub>, and /parked gc <duration>.
func (cli *ChatCLI) getParkedSuggestions(d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), "/parked")
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	// "/parked" alone — suggest subcommand verbs (or accept blank to
	// list all parked agents, which is also a valid invocation).
	if len(args) == 0 || (len(args) == 1 && !trailingSpace) {
		return prompt.FilterHasPrefix(parkedSubcommandSuggestions, current, true)
	}

	sub := args[0]
	switch sub {
	case "gc":
		// /parked gc <duration> — suggest common Go duration values.
		if len(args) == 1 || (len(args) == 2 && !trailingSpace) {
			return prompt.FilterHasPrefix([]prompt.Suggest{
				{Text: "1h", Description: "older than 1 hour"},
				{Text: "6h", Description: "older than 6 hours"},
				{Text: "24h", Description: "older than 1 day"},
				{Text: "168h", Description: "older than 1 week"},
			}, current, true)
		}
	case "prune", "help":
		// terminal subcommands — no further suggestions.
		return nil
	}
	return nil
}

// getParkTokenSuggestions completes /resume and /cancel-park with live
// snapshot tokens. Tokens shown are the first 8 characters (which is
// what /parked displays); the resolveParkToken helper accepts any
// unique prefix on dispatch.
func (cli *ChatCLI) getParkTokenSuggestions(prefix string, d prompt.Document) []prompt.Suggest {
	line := strings.TrimPrefix(d.TextBeforeCursor(), prefix)
	line = strings.TrimLeft(line, " ")
	args := strings.Fields(line)
	current := d.GetWordBeforeCursor()
	trailingSpace := strings.HasSuffix(d.TextBeforeCursor(), " ")

	// Only the first positional argument is a token; further args are
	// not accepted by either command.
	if len(args) > 1 || (len(args) == 1 && trailingSpace) {
		return nil
	}

	snaps, _ := park.List()
	if len(snaps) == 0 {
		return []prompt.Suggest{{
			Text:        "",
			Description: i18n.T("park.list.empty"),
		}}
	}
	out := make([]prompt.Suggest, 0, len(snaps))
	for _, s := range snaps {
		short := s.Token
		if len(short) > 12 {
			short = short[:12]
		}
		mode := string(s.Park.Mode)
		desc := mode + " — " + describeParkRequestShort(s.Park)
		out = append(out, prompt.Suggest{
			Text:        short,
			Description: desc,
		})
	}
	return prompt.FilterHasPrefix(out, current, true)
}

// describeParkRequestShort is a one-liner used inside completion
// descriptions. The longer form lives in agent_park.go.
func describeParkRequestShort(r park.Request) string {
	switch r.Mode {
	case park.ModeDelay:
		if r.Note != "" {
			return r.Note
		}
		return r.Delay.String()
	case park.ModeUntil:
		return r.Until.Format("15:04:05")
	case park.ModeForURL:
		if r.URL != "" {
			u := r.URL
			if len(u) > 40 {
				u = u[:40] + "…"
			}
			return u
		}
	case park.ModeForCmd:
		c := r.Command
		if len(c) > 40 {
			c = c[:40] + "…"
		}
		return c
	}
	return ""
}
