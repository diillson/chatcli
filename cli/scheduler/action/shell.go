/*
 * Shell — executor that runs a shell command under CoderMode safety.
 *
 * Payload:
 *   command    string — required
 *   env        map    — optional env overrides
 *   bypass_safety bool — opt-in. Requires the operator to have explicitly
 *                        whitelisted the calling job; the bridge rejects
 *                        otherwise.
 *   expect_exit int    — optional; when set, non-matching exit becomes
 *                        a failed outcome. Default 0.
 */
package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// Shell implements scheduler.ActionExecutor.
type Shell struct{}

// NewShell builds the executor.
func NewShell() *Shell { return &Shell{} }

// Type returns the ActionType literal.
func (Shell) Type() scheduler.ActionType { return scheduler.ActionShell }

// ValidateSpec enforces required fields.
func (Shell) ValidateSpec(payload map[string]any) error {
	if strings.TrimSpace(asString(payload, "command")) == "" {
		return fmt.Errorf("shell: payload.command is required")
	}
	return nil
}

// Execute runs the command.
func (Shell) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	cmd := asString(action.Payload, "command")
	overrides := asStringMap(action.Payload, "env")
	bypass := asBool(action.Payload, "bypass_safety")
	expectExit := 0
	if v, ok := action.Payload["expect_exit"]; ok {
		switch n := v.(type) {
		case int:
			expectExit = n
		case float64:
			expectExit = int(n)
		}
	}
	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("shell: no bridge wired")}
	}
	stdout, stderr, code, err := env.Bridge.RunShell(ctx, cmd, overrides, bypass)
	output := fmt.Sprintf("$ %s\nexit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s",
		cmd, code, stdout, stderr)
	if err != nil {
		transient := ctx.Err() != nil
		return scheduler.ActionResult{
			Output:    truncate(output, 1<<15),
			Err:       err,
			Transient: transient,
		}
	}
	if code != expectExit {
		return scheduler.ActionResult{
			Output: truncate(output, 1<<15),
			Err:    fmt.Errorf("exit %d != expected %d", code, expectExit),
		}
	}
	return scheduler.ActionResult{Output: truncate(output, 1<<15)}
}
