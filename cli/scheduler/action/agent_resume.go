/*
 * AgentResume — fires when a parked agent's wait condition is satisfied
 * and the interactive ReAct loop should re-enter.
 *
 * Payload:
 *
 *   resume_token  string — required, identifies the on-disk snapshot
 *   outcome       string — one of "elapsed" | "matched" | "timeout" |
 *                          "cancelled" — describes why the park ended
 *   detail        string — optional probe output (HTTP body, stdout,
 *                          shell error message) injected as context
 *
 * The actual rehydration is delegated to CLIBridge.NotifyParkComplete:
 * the bridge owns the live agent state and the cli.bus that signals the
 * interactive loop to take over again. The action stays thin so it can
 * be retried safely (the bridge is idempotent on token).
 */
package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// AgentResume implements scheduler.ActionExecutor.
type AgentResume struct{}

// NewAgentResume builds the executor.
func NewAgentResume() *AgentResume { return &AgentResume{} }

// Type returns the canonical ActionType literal.
func (AgentResume) Type() scheduler.ActionType { return scheduler.ActionAgentResume }

// ValidateSpec enforces required fields at admission time.
func (AgentResume) ValidateSpec(payload map[string]any) error {
	tok, _ := payload["resume_token"].(string)
	if strings.TrimSpace(tok) == "" {
		return fmt.Errorf("agent_resume: payload.resume_token is required")
	}
	return nil
}

// Execute calls the bridge to load the snapshot and re-enter the loop.
func (AgentResume) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("agent_resume: no bridge wired")}
	}
	tok := action.PayloadString("resume_token", "")
	outcome := action.PayloadString("outcome", "elapsed")
	detail := action.PayloadString("detail", "")
	if err := env.Bridge.NotifyParkComplete(ctx, tok, outcome, detail); err != nil {
		// Cancellation is structural, not a transient failure.
		transient := ctx.Err() != nil
		return scheduler.ActionResult{
			Err:       err,
			Transient: transient,
		}
	}
	return scheduler.ActionResult{
		Output: fmt.Sprintf("agent_resume: notified token=%s outcome=%s", tok, outcome),
	}
}
