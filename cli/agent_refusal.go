/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A turn the provider's safety classifier stopped (Anthropic stop_reason
 * "refusal", OpenAI "content_filter") comes back with no text. It used to
 * surface as "no response received" and end the whole coder session, tool
 * results and plan included, for something the model can recover from on
 * its own: the request that follows is a different one. The loop now tells
 * the model what happened and resends the turn, a bounded number of times
 * per run; only a repeated refusal ends the run, and then with the cause.
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/i18n"
	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// agentRefusalMaxRetries bounds the nudged resends in one run. A classifier
// that refuses the same conversation twice will refuse it a third time.
const agentRefusalMaxRetries = 2

// refusalNudge is the user-role message the model reads after a refusal.
// English on purpose: models follow it more reliably. It tells the model
// that nothing ran, so it does not assume the step happened.
const refusalNudge = "[system] Your previous reply was stopped by the provider's safety classifier (stop_reason: refusal) and arrived empty; nothing was executed. " +
	"Continue the task from the current step. State the next action plainly, avoid echoing file contents or secrets verbatim, " +
	"and if a step cannot be done, say so in one line and move to the next one."

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

// nudgeAfterRefusal appends the nudge and asks for a resend while the run
// has retries left. The count is per run (reset with the rest of the
// per-run state), so a run that keeps being refused ends with the cause.
func (a *AgentMode) nudgeAfterRefusal(err error, turnHistory *[]models.Message) bool {
	if !llmclient.IsRefusal(err) {
		return false
	}
	a.refusalRetries++
	if a.refusalRetries > agentRefusalMaxRetries {
		if a.logger != nil {
			a.logger.Warn("agent turn: refused again, giving up", zap.Int("retries", agentRefusalMaxRetries), zap.Error(err))
		}
		return false
	}
	if a.logger != nil {
		a.logger.Warn("agent turn: provider refused the reply, resending with a nudge",
			zap.Int("attempt", a.refusalRetries), zap.Int("max", agentRefusalMaxRetries), zap.Error(err))
	}
	if !a.cli.unattended {
		fmt.Println(colorize("  "+i18n.T("agent.refusal.retrying", a.refusalRetries, agentRefusalMaxRetries), ColorYellow))
	}
	nudge := models.Message{Role: "user", Content: refusalNudge}
	if turnHistory != nil {
		*turnHistory = append(*turnHistory, nudge)
	}
	a.cli.history = append(a.cli.history, nudge)
	return true
}
