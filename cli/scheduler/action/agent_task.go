/*
 * AgentTask — executor that boots a full ReAct agent loop.
 *
 * Payload:
 *   task          string — required
 *   system_hint   string — optional system prompt override
 */
package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// AgentTask implements scheduler.ActionExecutor.
type AgentTask struct{}

// NewAgentTask builds the executor.
func NewAgentTask() *AgentTask { return &AgentTask{} }

// Type returns the ActionType literal.
func (AgentTask) Type() scheduler.ActionType { return scheduler.ActionAgentTask }

// ValidateSpec enforces required fields.
func (AgentTask) ValidateSpec(payload map[string]any) error {
	if strings.TrimSpace(asString(payload, "task")) == "" {
		return fmt.Errorf("agent_task: payload.task is required")
	}
	return nil
}

// Execute delegates to the bridge.
func (AgentTask) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	task := asString(action.Payload, "task")
	system := asString(action.Payload, "system_hint")
	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("agent_task: no bridge wired")}
	}
	out, err := env.Bridge.RunAgentTask(ctx, task, system, env.Job.DangerousConfirmed)
	return scheduler.ActionResult{
		Output: truncate(out, 1<<15),
		Err:    err,
	}
}
