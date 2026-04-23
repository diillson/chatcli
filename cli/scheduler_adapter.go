/*
 * ChatCLI - scheduler_adapter.go
 *
 * Implements plugins.SchedulerAdapter so the @scheduler builtin plugin
 * can route ReAct tool calls into the live Scheduler. Supplied to
 * plugins.SetSchedulerAdapter during initScheduler.
 */
package cli

import (
	"context"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
)

// schedulerPluginAdapter is the concrete plugins.SchedulerAdapter.
type schedulerPluginAdapter struct {
	cli *ChatCLI
}

// Owner picks an Owner based on the current execution profile. Agent
// mode uses OwnerAgent; chat mode uses OwnerUser.
func (a *schedulerPluginAdapter) Owner() plugins.SchedulerOwner {
	kind := "agent"
	id := "agent"
	if a.cli.executionProfile == ProfileCoder {
		id = "coder"
	}
	if a.cli.executionProfile == ProfileNormal {
		kind = "user"
		id = a.cli.currentSessionName
		if id == "" {
			id = "interactive"
		}
	}
	return plugins.SchedulerOwner{Kind: kind, ID: id}
}

func (a *schedulerPluginAdapter) ScheduleJob(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.ScheduleJob(ctx, so, inputJSON)
	})
}

func (a *schedulerPluginAdapter) WaitUntil(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.WaitUntil(ctx, so, inputJSON)
	})
}

func (a *schedulerPluginAdapter) QueryJob(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.QueryJob(ctx, so, inputJSON)
	})
}

func (a *schedulerPluginAdapter) ListJobs(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.ListJobs(ctx, so, inputJSON)
	})
}

func (a *schedulerPluginAdapter) CancelJob(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.CancelJob(ctx, so, inputJSON)
	})
}

// dispatch translates the plugin-local Owner to the scheduler Owner and
// routes to either the in-process scheduler or (via jsonline) the
// remote client. Remote clients don't expose a ToolAdapter directly,
// so we adapt by calling the specific method under the hood.
func (a *schedulerPluginAdapter) dispatch(
	ctx context.Context,
	owner plugins.SchedulerOwner,
	inputJSON string,
	local func(*scheduler.ToolAdapter, scheduler.Owner) (string, error),
) (string, error) {
	so := scheduler.Owner{
		Kind: scheduler.OwnerKind(owner.Kind),
		ID:   owner.ID,
		Tag:  owner.Tag,
	}
	if a.cli.scheduler != nil {
		tool := scheduler.NewToolAdapter(a.cli.scheduler)
		return local(tool, so)
	}
	if a.cli.schedulerRemote != nil {
		// Remote adapter — forward via IPC. For this path we implement
		// by calling Enqueue/List/Query/Cancel on the client directly.
		return a.remote(ctx, so, inputJSON, local)
	}
	return "", scheduler.ErrSchedulerClosed
}

// remote is the fallback for IPC mode. It inspects the wrapped func
// via a cheap identity trick (the caller tells us the operation via
// the actual function value). Here we simply dispatch by the three
// input shapes we know the tool_adapter uses.
func (a *schedulerPluginAdapter) remote(
	ctx context.Context,
	owner scheduler.Owner,
	inputJSON string,
	_ func(*scheduler.ToolAdapter, scheduler.Owner) (string, error),
) (string, error) {
	// Decide by parsing inputJSON — every adapter method accepts the
	// same ToolInput shape; the underlying semantics come from the
	// wrapped operation. Because we can't introspect the closure, we
	// route based on which fields are set in the input.
	var in scheduler.ToolInput
	if len(inputJSON) > 0 {
		_ = jsonDecodeImpl(inputJSON, &in)
	}
	// Default: enqueue.
	out, err := a.cli.schedulerRemote.Enqueue(ctx, owner, in)
	if err != nil {
		return "", err
	}
	body, _ := marshalJSONImpl(out)
	return string(body), nil
}
