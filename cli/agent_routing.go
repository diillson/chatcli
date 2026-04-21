/*
 * ChatCLI - Smart chat↔agent routing heuristics.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// TrivialQueryClassification is the output of IsTrivialAgentQuery. It tells
// the caller whether the query warrants the full ReAct loop (agent mode) or
// can be answered by a single chat-mode turn.
//
// The classifier is conservative: when in doubt it returns Trivial=false so
// the existing agent flow runs unchanged. That's the "do no harm" stance —
// we'd rather burn a few extra tokens on a borderline question than refuse
// to enter agent mode on a real task.
type TrivialQueryClassification struct {
	// Trivial reports whether the query looks like a conversational /
	// factual question that chat mode can answer in one shot.
	Trivial bool
	// Reason is a short tag (for logs / telemetry) describing why the
	// classifier reached its verdict. Useful when deciding whether to
	// downgrade behaviour is working as intended.
	Reason string
}

const (
	smartRoutingEnv        = "CHATCLI_AGENT_SMART_ROUTE"
	smartRoutingMaxLenChar = 160 // queries longer than this are treated as non-trivial by default
)

// SmartRoutingMode is the operational mode for the chat/agent router.
//
//	SmartRoutingOff   — fully disabled, behave exactly as pre-feature.
//	SmartRoutingHint  — detect trivial queries, print a short dev-visible
//	                    hint on stdout, but still run agent mode as asked.
//	                    Default: the user's intent is never second-guessed.
//	SmartRoutingAuto  — redirect trivial queries to chat mode automatically.
//	                    Use when you trust the classifier and want maximum
//	                    token savings (the crítico's original ask).
type SmartRoutingMode string

const (
	SmartRoutingOff  SmartRoutingMode = "off"
	SmartRoutingHint SmartRoutingMode = "hint"
	SmartRoutingAuto SmartRoutingMode = "auto"
)

// SmartRouting reads CHATCLI_AGENT_SMART_ROUTE and returns the active mode.
// Empty / unset defaults to SmartRoutingHint so users GET the money-saving
// advice without unexpected chat-mode redirects.
func SmartRouting() SmartRoutingMode {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(smartRoutingEnv)))
	switch v {
	case "0", "false", "off", "no":
		return SmartRoutingOff
	case "auto", "redirect", "2":
		return SmartRoutingAuto
	case "", "1", "hint", "on", "true", "yes":
		return SmartRoutingHint
	default:
		return SmartRoutingHint
	}
}

// smartRoutingEnabled reports whether the trivial-query detector should run.
// False only in SmartRoutingOff; all other modes keep the classifier active.
func smartRoutingEnabled() bool {
	return SmartRouting() != SmartRoutingOff
}

// taskSignalRe matches any token that strongly indicates the user wants the
// agent to TAKE ACTION (execute, edit, build, run, deploy, …). These kill
// the trivial classification regardless of length.
var taskSignalRe = regexp.MustCompile(`(?i)\b(create|crie|criar|build|compile|compilar|run|execut[eao]r?|deploy|install|instal[ae]r?|configure|configur[ae]r?|rename|renomear|refactor|refator[ae]r?|fix|corrig[ei]r?|debug|depur[ae]r?|test|test[ae]r?|write|escrev[ea]r?|read\s+file|open\s+file|edit|edit[ae]r?|patch|delete|delet[ae]r?|remove|remov[ea]r?|push|commit|git\s+|make\s+|go\s+build|go\s+test|npm\s+|yarn\s+|docker\s+|kubectl\s+)\b`)

// contextSignalRe matches tokens that indicate the query references the
// working directory / code (@file, @git, paths, function names, etc).
// These are excluded from trivial classification even if short.
var contextSignalRe = regexp.MustCompile(`(?:@file|@git|@command|@history|@env)\b|(?:\./|\.\./|~/)|\.(?:go|py|js|ts|tsx|jsx|rs|java|kt|swift|c|cpp|h|hpp|rb|php|md|json|yaml|yml|toml|sh|bash)\b`)

// IsTrivialAgentQuery classifies a user query as conversational/factual
// (chat-suited) or task-oriented (agent-suited). See the type doc for the
// "do no harm" policy — the classifier errs toward agent mode.
//
// Inputs:
//   - query: the raw user text (without the /agent or /run prefix).
//   - explicitAgentInvocation: true when the caller ran /run or /agent
//     explicitly on a non-/coder flow. When the user was just in their
//     normal shell and the router is deciding by itself, pass false and
//     the classifier will be slightly more permissive about suggesting
//     chat mode.
//
// The function is safe for empty input — returns Trivial=false with
// Reason="empty" so the caller's existing guard rails kick in.
func IsTrivialAgentQuery(query string, explicitAgentInvocation bool) TrivialQueryClassification {
	q := strings.TrimSpace(query)
	if q == "" {
		return TrivialQueryClassification{Trivial: false, Reason: "empty"}
	}
	if !smartRoutingEnabled() {
		return TrivialQueryClassification{Trivial: false, Reason: "disabled"}
	}

	// Long queries usually carry enough specifics to need the agent.
	if len(q) > smartRoutingMaxLenChar {
		return TrivialQueryClassification{Trivial: false, Reason: "long"}
	}

	// Any @file/@git/@command or path-looking token → agent territory.
	if contextSignalRe.MatchString(q) {
		return TrivialQueryClassification{Trivial: false, Reason: "has_context_signal"}
	}

	// Any obvious task verb → agent territory.
	if taskSignalRe.MatchString(q) {
		return TrivialQueryClassification{Trivial: false, Reason: "has_task_verb"}
	}

	// Mentions of tools or subcommands → agent territory.
	lower := strings.ToLower(q)
	if strings.Contains(lower, "<tool_call") ||
		strings.Contains(lower, "<agent_call") ||
		strings.Contains(lower, "```") {
		return TrivialQueryClassification{Trivial: false, Reason: "has_tool_markup"}
	}

	// Conversational question starters are a strong positive signal.
	// Localized to PT and EN to cover the two supported locales without
	// requiring i18n lookups here.
	questionLeads := []string{
		// English
		"what", "why", "how does", "how do", "when", "where", "who",
		"is there", "are there", "can you explain", "explain", "tell me about",
		"summarize", "summary of", "difference between",
		// Portuguese
		"o que", "por que", "porque", "pq", "como funciona", "como é",
		"quando", "onde", "quem", "existe", "existem", "pode me explicar",
		"explique", "fale sobre", "resuma", "resumo de", "diferença entre",
		"qual", "quais", "quanto", "quantos",
	}
	for _, lead := range questionLeads {
		if strings.HasPrefix(lower, lead+" ") || strings.HasPrefix(lower, lead+"?") {
			return TrivialQueryClassification{Trivial: true, Reason: "question_word"}
		}
	}

	// Very short (< ~8 words) + ends with a question mark → likely trivial.
	if strings.HasSuffix(q, "?") && len(strings.Fields(q)) <= 10 {
		return TrivialQueryClassification{Trivial: true, Reason: "short_question"}
	}

	// When the user explicitly typed /agent or /run, respect that intent
	// unless one of the strong positive signals above fired.
	if explicitAgentInvocation {
		return TrivialQueryClassification{Trivial: false, Reason: "explicit_invocation"}
	}

	return TrivialQueryClassification{Trivial: false, Reason: "default_agent"}
}

// MaybeReroute applies the SmartRouting policy to a `/agent <task>` or
// `/run <task>` invocation. It inspects `task` and:
//
//   - In SmartRoutingOff mode → no-op, returns false (caller proceeds
//     with the agent-mode panic as usual).
//   - In SmartRoutingHint (default) → when the task looks conversational,
//     prints a one-line tip to stdout. Always returns false so the
//     agent-mode path still runs. The user's explicit intent wins.
//   - In SmartRoutingAuto → when the task looks conversational AND the
//     ChatCLI has a configured client, hands the query off to
//     processLLMRequest (chat mode) and returns true. The caller MUST
//     NOT proceed with the agent-mode panic when true is returned.
//
// The function takes the full /run or /agent query string minus the
// command token. Pass `label` (e.g. "/agent" or "/run") for user-facing
// messaging only — it does not affect the classification.
func (cli *ChatCLI) MaybeReroute(label, task string) bool {
	task = strings.TrimSpace(task)
	if task == "" {
		return false
	}
	mode := SmartRouting()
	if mode == SmartRoutingOff {
		return false
	}
	classification := IsTrivialAgentQuery(task, true)
	if !classification.Trivial {
		return false
	}
	cli.logger.Debug("smart-route: trivial query detected",
		zap.String("mode", string(mode)),
		zap.String("label", label),
		zap.String("reason", classification.Reason))

	hint := i18n.T("agent.early_exit.chat_hint")
	fmt.Printf("  %s %s\n", colorize("ℹ", ColorGray), colorize(hint, ColorGray))

	if mode != SmartRoutingAuto {
		return false
	}
	// Auto: dispatch directly to chat mode. Mirrors the call graph used
	// by cli.executor so the queueing / animation / hook invariants
	// behave exactly as if the user typed the question without /agent.
	if cli.Client == nil {
		return false
	}
	cli.interactionState = StateProcessing
	// The /agent and /run panic paths run synchronously; to preserve the
	// same call shape on Windows (which runs chat synchronously too),
	// default to synchronous dispatch here as well.
	cli.processLLMRequest(task)
	return true
}
