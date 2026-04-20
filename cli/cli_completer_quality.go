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

// getPlanSuggestions returns a usage-hint suggestion for /plan.
//
// /plan accepts either:
//
//	/plan                  arm plan-first flag; consumed by next /agent or /coder
//	/plan <free task>      arm + enter agent mode with that task inline
//
// Since there are no enumerable subcommands, the completer shows a single
// hint so the user understands the command form.
func (cli *ChatCLI) getPlanSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/plan", Description: i18n.T("complete.root.plan")},
		}
	}

	// After space: show usage hint (non-selectable placeholder; the user
	// types free-form task).
	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		return []prompt.Suggest{
			{Text: "<task>", Description: i18n.T("complete.plan.hint")},
		}
	}

	return []prompt.Suggest{}
}

// ─── /reflect ──────────────────────────────────────────────────────────────

// getReflectSuggestions returns a usage-hint suggestion for /reflect.
//
// /reflect accepts:
//
//	/reflect                  shows inline usage help
//	/reflect <free lesson>    persists the lesson directly to memory.Fact
//
// Same design as /plan: no enumerable subs, just a hint placeholder.
func (cli *ChatCLI) getReflectSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)

	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		return []prompt.Suggest{
			{Text: "/reflect", Description: i18n.T("complete.root.reflect")},
		}
	}

	if len(args) == 1 || (len(args) == 2 && !strings.HasSuffix(line, " ")) {
		return []prompt.Suggest{
			{Text: "<lesson>", Description: i18n.T("complete.reflect.hint")},
		}
	}

	return []prompt.Suggest{}
}
