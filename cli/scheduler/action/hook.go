/*
 * HookAction — executor that fires a configured chatcli hook event.
 *
 * Payload:
 *   event       string — required. One of hooks.EventType values.
 *   tool        string — optional, for PreToolUse/PostToolUse filters.
 *   message     string — optional, goes into ToolOutput.
 */
package action

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/scheduler"
)

// HookAction implements scheduler.ActionExecutor.
type HookAction struct{}

// NewHookAction builds the executor.
func NewHookAction() *HookAction { return &HookAction{} }

// Type returns the ActionType literal.
func (HookAction) Type() scheduler.ActionType { return scheduler.ActionHook }

// ValidateSpec enforces required fields.
func (HookAction) ValidateSpec(payload map[string]any) error {
	evt := asString(payload, "event")
	if strings.TrimSpace(evt) == "" {
		return fmt.Errorf("hook: payload.event is required")
	}
	return nil
}

// Execute dispatches the hook event.
func (HookAction) Execute(_ context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	event := asString(action.Payload, "event")
	tool := asString(action.Payload, "tool")
	message := asString(action.Payload, "message")

	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("hook: no bridge wired")}
	}
	res := env.Bridge.FireHook(hooks.HookEvent{
		Type:       hooks.EventType(event),
		Timestamp:  time.Now(),
		ToolName:   tool,
		ToolOutput: message,
	})
	if res == nil {
		return scheduler.ActionResult{Output: "no matching hook"}
	}
	if res.Blocked {
		return scheduler.ActionResult{
			Output: fmt.Sprintf("hook blocked: %s", res.BlockReason),
			Err:    fmt.Errorf("hook %s blocked: %s", event, res.BlockReason),
		}
	}
	return scheduler.ActionResult{
		Output: fmt.Sprintf("exit=%d\n%s", res.ExitCode, truncate(res.Output, 4096)),
	}
}
