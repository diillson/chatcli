/*
 * SlashCmd — executor that invokes a chatcli slash command as if the
 * user had typed it. Returns the captured output.
 *
 * Payload:
 *   command  string — required. The full line, including leading '/'.
 */
package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// SlashCmd implements scheduler.ActionExecutor.
type SlashCmd struct{}

// NewSlashCmd builds the executor.
func NewSlashCmd() *SlashCmd { return &SlashCmd{} }

// Type returns the ActionType literal.
func (SlashCmd) Type() scheduler.ActionType { return scheduler.ActionSlashCmd }

// ValidateSpec enforces required fields.
func (SlashCmd) ValidateSpec(payload map[string]any) error {
	cmd := asString(payload, "command")
	if strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("slash_cmd: payload.command is required")
	}
	if !strings.HasPrefix(cmd, "/") && !strings.HasPrefix(cmd, "@") {
		return fmt.Errorf("slash_cmd: command must start with '/' or '@'")
	}
	return nil
}

// Execute runs the slash command.
func (SlashCmd) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	cmd := asString(action.Payload, "command")
	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("slash_cmd: no bridge wired")}
	}
	// Forward Job.DangerousConfirmed so the scheduler's headless
	// policy checker can admit "Ask" classifications for jobs that
	// pre-authorized via --i-know / i_know:true. Without this, the
	// LLM-driven /run|/agent|/coder paths see ShellPolicyAsk → reject
	// even though the user explicitly opted in at enqueue.
	output, _, err := env.Bridge.ExecuteSlashCommand(ctx, cmd, env.Job.DangerousConfirmed)
	return scheduler.ActionResult{
		Output: output,
		Err:    err,
	}
}
