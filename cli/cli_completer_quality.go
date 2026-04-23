/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Autocomplete providers for the five seven-pattern quality slashes:
 *   /thinking — cross-provider reasoning override (#7)
 *   /refine   — Self-Refine session toggle (#5)
 *   /verify   — Chain-of-Verification session toggle (#6)
 *   /plan     — Plan-and-Solve / ReWOO trigger (#2)
 *   /reflect  — Reflexion manual lesson persistence (#3)
 *
 * All descriptions resolve via i18n so pt-BR and en speakers get the
 * explanation in their locale. Keys live under complete.{thinking,
 * refine,verify,plan,reflect}.* in i18n/locales/*.json.
 */
package cli

import (
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

// ─── /thinking ─────────────────────────────────────────────────────────────

// getThinkingSuggestions returns suggestions for /thinking.
//
// Accepted forms (see cli.handleThinkingCommand):
//
//	/thinking                          show current override state
//	/thinking auto                     clear override
//	/thinking off                      force no-thinking next turn
//	/thinking on|low|medium|high|max   set explicit tier
//	/thinking budget=<N>               nearest tier to N tokens
func (cli *ChatCLI) getThinkingSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	// Just "/thinking" with no trailing space → offer the root entry so
	// the user sees the command description without arguments yet.
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/thinking", Description: i18n.T("complete.root.thinking")},
		}
	}

	// Argument slot (len=1 with space, OR len=2 still typing).
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		tiers := []prompt.Suggest{
			{Text: "auto", Description: i18n.T("complete.thinking.auto")},
			{Text: "off", Description: i18n.T("complete.thinking.off")},
			{Text: "on", Description: i18n.T("complete.thinking.on")},
			{Text: "low", Description: i18n.T("complete.thinking.low")},
			{Text: "medium", Description: i18n.T("complete.thinking.medium")},
			{Text: "high", Description: i18n.T("complete.thinking.high")},
			{Text: "max", Description: i18n.T("complete.thinking.max")},
			{Text: "budget=4096", Description: i18n.T("complete.thinking.budget_medium")},
			{Text: "budget=8000", Description: i18n.T("complete.thinking.budget_high")},
			{Text: "budget=16384", Description: i18n.T("complete.thinking.budget_max")},
		}
		return prompt.FilterHasPrefix(tiers, word, true)
	}

	return []prompt.Suggest{}
}

// ─── /refine ───────────────────────────────────────────────────────────────

// getRefineSuggestions returns suggestions for /refine (Self-Refine #5).
//
// Subcommands (see cli.handleRefineCommand via qualityToggleSpec):
//
//	/refine                  show current session-override state
//	/refine on|off           force on/off for the session
//	/refine once|next        arm for next turn only
//	/refine auto|clear       clear override (defer to /config quality)
func (cli *ChatCLI) getRefineSuggestions(d prompt.Document) []prompt.Suggest {
	return qualityToggleCompleter(d, "/refine", "complete.root.refine", "complete.refine")
}

// ─── /verify ───────────────────────────────────────────────────────────────

// getVerifySuggestions returns suggestions for /verify (CoVe #6). Shape
// identical to /refine — both go through the same qualityToggleSpec path.
func (cli *ChatCLI) getVerifySuggestions(d prompt.Document) []prompt.Suggest {
	return qualityToggleCompleter(d, "/verify", "complete.root.verify", "complete.verify")
}

// qualityToggleCompleter is the shared completer for /refine and /verify
// since they accept the exact same argument set. Keeps the two functions
// above as thin wrappers so call sites stay self-documenting.
func qualityToggleCompleter(d prompt.Document, slash, rootKey, prefix string) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: slash, Description: i18n.T(rootKey)},
		}
	}

	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "on", Description: i18n.T(prefix + ".on")},
			{Text: "off", Description: i18n.T(prefix + ".off")},
			{Text: "once", Description: i18n.T(prefix + ".once")},
			{Text: "auto", Description: i18n.T(prefix + ".auto")},
			{Text: "clear", Description: i18n.T(prefix + ".clear")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	return []prompt.Suggest{}
}

// ─── /plan ─────────────────────────────────────────────────────────────────

// getPlanSuggestions returns suggestions for /plan (Plan-and-Solve #2).
//
// Accepted forms (see cli.handlePlanCommand):
//
//	/plan                        arm plan-first flag; consumed by next /agent or /coder
//	/plan <free task>            arm + enter agent mode with that task inline
//	/plan agent <task>           explicit agent mode (same as bare /plan <task>)
//	/plan coder <task>           coder mode with plan-first armed
//	/plan preview <task>         dry-run: generate plan and render it without executing
//	/plan dry <task>             alias of preview
func (cli *ChatCLI) getPlanSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/plan", Description: i18n.T("complete.root.plan")},
		}
	}

	// First argument slot: offer named subcommands alongside the
	// free-form <task> hint. The subcommands are filterable by prefix so
	// typing "c" narrows to /plan coder, "pr" to /plan preview, etc.
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "agent", Description: i18n.T("complete.plan.agent")},
			{Text: "coder", Description: i18n.T("complete.plan.coder")},
			{Text: "preview", Description: i18n.T("complete.plan.preview")},
			{Text: "dry", Description: i18n.T("complete.plan.dry")},
			{Text: "<task>", Description: i18n.T("complete.plan.hint")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	// After a known subcommand (e.g. "/plan coder "), show the task hint
	// so the user knows free-form text is expected next.
	if len(args) >= 2 {
		first := args[1]
		isSub := first == "agent" || first == "coder" || first == "preview" || first == "dry"
		if isSub && (len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " "))) {
			return []prompt.Suggest{
				{Text: "<task>", Description: i18n.T("complete.plan.hint")},
			}
		}
	}

	return []prompt.Suggest{}
}

// ─── /reflect ──────────────────────────────────────────────────────────────

// getReflectSuggestions returns suggestions for /reflect and its
// subcommands.
//
// Layout:
//
//	/reflect                        → root + subcommand menu
//	/reflect list|failed|drain      → terminal verbs, no further args
//	/reflect retry|purge <id>       → second arg autocompletes from DLQ
//	/reflect <free lesson>          → any other token is the lesson text
//
// The DLQ-ID autocomplete reaches into the live lessonq.Runner when
// one is wired; when the queue is disabled or the runner hasn't been
// built yet, it falls back to an ID placeholder.
func (cli *ChatCLI) getReflectSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	// Before any text: offer the root command.
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/reflect", Description: i18n.T("complete.root.reflect")},
		}
	}

	// After "/reflect ": show the subcommand menu + free-text fallback.
	if len(args) == 1 && strings.HasSuffix(line, " ") {
		return reflectSubcommandMenu()
	}

	// Partial subcommand token: prefix-filter the menu so typing "re"
	// narrows to "retry" without losing discoverability.
	if len(args) == 2 && !strings.HasSuffix(line, " ") {
		partial := strings.ToLower(args[1])
		filtered := filterSuggestions(reflectSubcommandMenu(), partial)
		if len(filtered) > 0 {
			return filtered
		}
		// No subcommand matched the prefix — treat it as free-text.
		return []prompt.Suggest{
			{Text: "<lesson>", Description: i18n.T("complete.reflect.hint")},
		}
	}

	// Second argument for retry / purge → autocomplete DLQ IDs.
	if len(args) >= 2 {
		sub := strings.ToLower(args[1])
		if sub == "retry" || sub == "purge" {
			// Accept both "/reflect retry " and "/reflect retry ab".
			if (len(args) == 2 && strings.HasSuffix(line, " ")) || len(args) == 3 {
				return cli.reflectDLQIDSuggestions()
			}
		}
	}

	return []prompt.Suggest{}
}

// reflectSubcommandMenu is the static menu of subcommand verbs plus
// the free-text hint. Keeping it static lets the completer stay
// snappy even when the queue is live.
func reflectSubcommandMenu() []prompt.Suggest {
	return []prompt.Suggest{
		{Text: "list", Description: i18n.T("complete.reflect.list")},
		{Text: "failed", Description: i18n.T("complete.reflect.failed")},
		{Text: "retry", Description: i18n.T("complete.reflect.retry")},
		{Text: "purge", Description: i18n.T("complete.reflect.purge")},
		{Text: "drain", Description: i18n.T("complete.reflect.drain")},
		{Text: "<lesson>", Description: i18n.T("complete.reflect.hint")},
	}
}

// reflectDLQIDSuggestions pulls live DLQ IDs from the runner when
// available. Each suggestion shows the ID plus the task preview so
// the user can pick the right one at a glance.
func (cli *ChatCLI) reflectDLQIDSuggestions() []prompt.Suggest {
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()
	if rnr == nil {
		return []prompt.Suggest{
			{Text: "<job-id>", Description: i18n.T("complete.reflect.id_placeholder")},
		}
	}
	dlq, err := rnr.DLQList()
	if err != nil || len(dlq) == 0 {
		return []prompt.Suggest{
			{Text: "<job-id>", Description: i18n.T("complete.reflect.id_placeholder")},
		}
	}
	out := make([]prompt.Suggest, 0, len(dlq))
	for _, job := range dlq {
		desc := truncate(job.Request.Task, 60)
		if job.LastError != "" {
			desc += " · " + truncate(job.LastError, 40)
		}
		out = append(out, prompt.Suggest{Text: string(job.ID), Description: desc})
	}
	return out
}

// filterSuggestions returns the subset of s whose Text starts with
// prefix (case-insensitive). Used to narrow the menu as the user types.
func filterSuggestions(s []prompt.Suggest, prefix string) []prompt.Suggest {
	if prefix == "" {
		return s
	}
	out := make([]prompt.Suggest, 0, len(s))
	for _, sug := range s {
		if strings.HasPrefix(strings.ToLower(sug.Text), prefix) {
			out = append(out, sug)
		}
	}
	return out
}
