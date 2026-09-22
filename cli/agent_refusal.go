/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A turn the provider's safety classifier stopped (Anthropic stop_reason
 * "refusal", OpenAI "content_filter") comes back with no text. It used to
 * surface as "no response received" and end the whole coder session, tool
 * results and plan included.
 *
 * Resending the same conversation to the same model mostly gets the same
 * answer: the classifier judges the whole context, and in the field it
 * refused half of a run's turns. So the resend goes to a sibling model of
 * the same provider (Fable → Opus, Opus → Sonnet, or whatever
 * CHATCLI_AGENT_REFUSAL_FALLBACK names) for that one turn, with a note
 * telling the model what happened; the next turn is back on the model the
 * user chose. A run refused more than a few times keeps the fallback for
 * the rest of the run instead of paying for a refused request every turn.
 * With the fallback off the resend is a plain retry, bounded per run.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// refusalFallbackEnv names the model a refused turn is resent on: "auto"
// (default) picks a sibling of the model that refused, "off" resends on the
// same model, "PROVIDER:model" is used as given.
const refusalFallbackEnv = "CHATCLI_AGENT_REFUSAL_FALLBACK"

// agentRefusalMaxRetries bounds the plain resends in one run, and is the
// number of refusals after which the fallback becomes sticky for the run.
const agentRefusalMaxRetries = 2

// refusalRouteVia is the source label the dashboard and the log show for
// a fallback route.
const refusalRouteVia = "refusal fallback"

// refusalNudge is the user-role message the model reads after a refusal.
// English on purpose: models follow it more reliably. It tells the model
// that nothing ran, so it does not assume the step happened.
const refusalNudge = "[system] Your previous reply was stopped by the provider's safety classifier (stop_reason: refusal) and arrived empty; nothing was executed. " +
	"Continue the task from the current step. State the next action plainly, avoid echoing file contents or secrets verbatim, " +
	"and if a step cannot be done, say so in one line and move to the next one."

// refusalSiblings is the "auto" fallback by model family, most capable
// sibling first. Looked up on the provider that refused, through the
// catalog, so a provider that lacks the sibling gets none.
var refusalSiblings = []struct {
	family   string
	siblings []string
}{
	{"fable", []string{"claude-opus-5", "claude-sonnet-5"}},
	{"mythos", []string{"claude-opus-5", "claude-sonnet-5"}},
	{"opus", []string{"claude-sonnet-5"}},
	{"sonnet", []string{"claude-haiku-4-5-20251001"}},
}

// resendTurnAfter reports whether the turn that failed with err should be
// sent again: after an expired credential was refreshed, or after a
// refusal, with a nudge appended to both the outgoing turn and the run's
// history so the resend and every later turn carry it.
func (a *AgentMode) resendTurnAfter(err error, turnHistory *[]models.Message) bool {
	if a.cli.refreshClientOnAuthError(err) {
		return true
	}
	return a.nudgeAfterRefusal(err, turnHistory)
}

// nudgeAfterRefusal appends the nudge, routes the resend to the fallback
// model when there is one, and asks for the resend. Without a fallback the
// resend is a plain retry while the run has retries left.
func (a *AgentMode) nudgeAfterRefusal(err error, turnHistory *[]models.Message) bool {
	if !llmclient.IsRefusal(err) {
		return false
	}
	a.refusalRetries++
	fallback := a.refusalFallbackHandle()
	if fallback == "" && a.refusalRetries > agentRefusalMaxRetries {
		if a.logger != nil {
			a.logger.Warn("agent turn: refused again and no fallback model, giving up", zap.Int("retries", agentRefusalMaxRetries), zap.Error(err))
		}
		return false
	}
	nudge := models.Message{Role: "user", Content: refusalNudge}
	if turnHistory != nil {
		*turnHistory = append(*turnHistory, nudge)
	}
	a.cli.history = append(a.cli.history, nudge)

	notice := i18n.T("agent.refusal.retrying", a.refusalRetries, agentRefusalMaxRetries)
	if fallback != "" {
		notice = a.armRefusalFallback(fallback)
	}
	if a.logger != nil {
		a.logger.Warn("agent turn: provider refused the reply, resending",
			zap.Int("attempt", a.refusalRetries), zap.String("fallback", fallback), zap.Bool("sticky", a.refusalSticky), zap.Error(err))
	}
	if !a.cli.unattended {
		fmt.Println(colorize("  "+notice, ColorYellow))
	}
	return true
}

// armRefusalFallback routes the coming resend to the fallback model. Past
// the retry budget the route stays for the rest of the run. It returns the
// notice for the terminal.
func (a *AgentMode) armRefusalFallback(fallback string) string {
	if a.refusalSticky {
		return i18n.T("agent.refusal.fallback_sticky", fallback)
	}
	if a.refusalFallback == "" {
		a.refusalPrevOverride = a.cli.agentRouteOverrideHandle()
	}
	a.refusalFallback, a.refusalArmed = fallback, true
	a.cli.setAgentRouteOverride(fallback, refusalRouteVia)
	if a.refusalRetries > agentRefusalMaxRetries {
		a.refusalSticky = true
		return i18n.T("agent.refusal.fallback_sticky", fallback)
	}
	return i18n.T("agent.refusal.fallback", fallback, a.refusalRetries, agentRefusalMaxRetries)
}

// settleRefusalFallback runs when a turn resolves its client. The resolve
// right after a refusal serves the fallback turn; the one after that hands
// the route back to what the user had, unless the fallback became sticky.
func (a *AgentMode) settleRefusalFallback() {
	if a.refusalFallback == "" || a.refusalSticky {
		return
	}
	if a.refusalArmed {
		a.refusalArmed = false
		return
	}
	if a.cli.agentRouteOverrideHandle() == a.refusalFallback {
		a.cli.setAgentRouteOverride(a.refusalPrevOverride, "refusal fallback done")
	}
	a.refusalFallback, a.refusalPrevOverride = "", ""
}

// resetRefusalState clears the per-run refusal budget and any fallback
// route still armed, so the next run starts on the model the user chose.
func (a *AgentMode) resetRefusalState() {
	a.refusalRetries = 0
	a.refusalFallback, a.refusalPrevOverride = "", ""
	a.refusalArmed, a.refusalSticky = false, false
}

// refusalFallbackHandle is the "PROVIDER:model" a refused turn is resent
// on, or "" when there is none: the setting is off, or "auto" finds no
// sibling of the refusing model in the provider's catalog.
func (a *AgentMode) refusalFallbackHandle() string {
	setting := strings.TrimSpace(utils.GetEnvOrDefault(refusalFallbackEnv, "auto"))
	switch strings.ToLower(setting) {
	case "", "off", "false", "0", "none":
		return ""
	case "auto":
		if a.refusalSticky && a.refusalFallback != "" {
			return a.refusalFallback
		}
		provider, model := a.effectiveRoute()
		return refusalSiblingFor(provider, model)
	}
	return setting
}

// refusalSiblingFor picks the sibling model of the same provider for the
// family of model, the first one the catalog knows.
func refusalSiblingFor(provider, model string) string {
	lower := strings.ToLower(model)
	for _, f := range refusalSiblings {
		if !strings.Contains(lower, f.family) {
			continue
		}
		for _, sibling := range f.siblings {
			if strings.Contains(lower, sibling) {
				continue // already on it
			}
			if _, known := catalog.Resolve(provider, sibling); known {
				return strings.ToUpper(provider) + ":" + sibling
			}
		}
		return ""
	}
	return ""
}
