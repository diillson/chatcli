/*
 * Noop — do-nothing executor. Useful for testing, for placeholder
 * pipeline nodes whose only purpose is to trigger downstream jobs
 * via the Triggers edge, and for "heartbeat" scheduled jobs.
 *
 * Payload:
 *   message  string — optional, echoed in Output.
 */
package action

import (
	"context"

	"github.com/diillson/chatcli/cli/scheduler"
)

// Noop implements scheduler.ActionExecutor.
type Noop struct{}

// NewNoop builds the executor.
func NewNoop() *Noop { return &Noop{} }

// Type returns the ActionType literal.
func (Noop) Type() scheduler.ActionType { return scheduler.ActionNoop }

// ValidateSpec is always happy.
func (Noop) ValidateSpec(_ map[string]any) error { return nil }

// Execute returns success with the optional message.
func (Noop) Execute(_ context.Context, action scheduler.Action, _ *scheduler.ExecEnv) scheduler.ActionResult {
	msg := asString(action.Payload, "message")
	if msg == "" {
		msg = "noop"
	}
	return scheduler.ActionResult{Output: msg}
}
