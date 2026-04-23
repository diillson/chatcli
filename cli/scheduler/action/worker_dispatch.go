/*
 * WorkerDispatch — executor that runs a single worker (one of the
 * chatcli agent/workers agents: Planner, Tester, Shell, …) without
 * invoking the full ReAct loop. Useful for "send me a diagnostic
 * report" style scheduled tasks.
 *
 * Payload:
 *   agent_type  string — required
 *   task        string — required
 */
package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// WorkerDispatch implements scheduler.ActionExecutor.
type WorkerDispatch struct{}

// NewWorkerDispatch builds the executor.
func NewWorkerDispatch() *WorkerDispatch { return &WorkerDispatch{} }

// Type returns the ActionType literal.
func (WorkerDispatch) Type() scheduler.ActionType { return scheduler.ActionWorkerDispatch }

// ValidateSpec enforces required fields.
func (WorkerDispatch) ValidateSpec(payload map[string]any) error {
	if strings.TrimSpace(asString(payload, "agent_type")) == "" {
		return fmt.Errorf("worker_dispatch: payload.agent_type is required")
	}
	if strings.TrimSpace(asString(payload, "task")) == "" {
		return fmt.Errorf("worker_dispatch: payload.task is required")
	}
	return nil
}

// Execute delegates to the bridge.
func (WorkerDispatch) Execute(ctx context.Context, action scheduler.Action, env *scheduler.ExecEnv) scheduler.ActionResult {
	agentType := asString(action.Payload, "agent_type")
	task := asString(action.Payload, "task")
	if env == nil || env.Bridge == nil {
		return scheduler.ActionResult{Err: fmt.Errorf("worker_dispatch: no bridge wired")}
	}
	out, err := env.Bridge.DispatchWorker(ctx, agentType, task)
	return scheduler.ActionResult{
		Output: truncate(out, 1<<15),
		Err:    err,
	}
}
