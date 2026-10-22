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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/scheduler"
	"github.com/diillson/chatcli/i18n"
)

// schedulerPluginAdapter is the concrete plugins.SchedulerAdapter.
type schedulerPluginAdapter struct {
	cli *ChatCLI
}

// activeSkillNames snapshots the skill names active in the current
// session: pinned skills plus everything the running agent loop has
// injected so far. Sorted and deduplicated for a stable job payload.
func (cli *ChatCLI) activeSkillNames() []string {
	seen := make(map[string]bool)
	if cli.skillHandler != nil {
		for _, name := range cli.skillHandler.PinnedNames() {
			seen[name] = true
		}
	}
	if cli.agentMode != nil {
		for _, name := range cli.agentMode.InjectedSkillNames() {
			seen[name] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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
	inputJSON = a.withDefaultSkills(inputJSON)
	return a.dispatch(ctx, owner, inputJSON, "schedule", func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.ScheduleJob(ctx, so, inputJSON)
	})
}

// withDefaultSkills injects the creating run's active skill names into a
// schedule_job input that did not specify its own, so a card the squad
// schedules fires with the same knowledge the scheduling run had. The
// caller's explicit skills list (even an empty one) always wins; inputs
// that fail to parse pass through untouched — buildJobFromInput will
// surface the real error.
func (a *schedulerPluginAdapter) withDefaultSkills(inputJSON string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &m); err != nil {
		return inputJSON
	}
	if _, has := m["skills"]; has {
		return inputJSON
	}
	names := a.cli.activeSkillNames()
	if len(names) == 0 {
		return inputJSON
	}
	m["skills"] = names
	out, err := json.Marshal(m)
	if err != nil {
		return inputJSON
	}
	return string(out)
}

func (a *schedulerPluginAdapter) WaitUntil(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, "wait", func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.WaitUntil(ctx, so, inputJSON)
	})
}

func (a *schedulerPluginAdapter) QueryJob(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, "query", func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.QueryJob(ctx, so, inputJSON)
	})
}

func (a *schedulerPluginAdapter) ListJobs(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, "list", func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.ListJobs(ctx, so, inputJSON)
	})
}

func (a *schedulerPluginAdapter) CancelJob(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, "cancel", func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.CancelJob(ctx, so, inputJSON)
	})
}

// PauseJob implements plugins.SchedulerPauser.
func (a *schedulerPluginAdapter) PauseJob(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, "pause", func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.PauseJob(ctx, so, inputJSON)
	})
}

// ResumeJob implements plugins.SchedulerPauser.
func (a *schedulerPluginAdapter) ResumeJob(ctx context.Context, owner plugins.SchedulerOwner, inputJSON string) (string, error) {
	return a.dispatch(ctx, owner, inputJSON, "resume", func(t *scheduler.ToolAdapter, so scheduler.Owner) (string, error) {
		return t.ResumeJob(ctx, so, inputJSON)
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
	op string,
	local func(*scheduler.ToolAdapter, scheduler.Owner) (string, error),
) (string, error) {
	so := scheduler.Owner{
		Kind: scheduler.OwnerKind(owner.Kind),
		ID:   owner.ID,
		Tag:  owner.Tag,
	}
	if a.cli.scheduler != nil {
		// The heartbeat prints through stdout: on the REPL it lands above the
		// spinner; on RPC surfaces captureStreaming forwards it live to the
		// client, so a long synchronous wait shows life signs instead of
		// minutes of "thinking" silence.
		tool := scheduler.NewToolAdapter(a.cli.scheduler).WithWaitProgress(
			func(id scheduler.JobID, status scheduler.JobStatus, elapsed, remaining time.Duration) {
				fmt.Println(colorize("  ⏳ "+i18n.T("scheduler.wait.progress", string(id), string(status), elapsed, remaining), ColorGray))
			})
		return local(tool, so)
	}
	if a.cli.schedulerRemote != nil {
		// Remote adapter — forward via IPC, routed by the operation.
		return a.remote(ctx, so, inputJSON, op)
	}
	return "", scheduler.ErrSchedulerClosed
}

// remote is the IPC path: each operation goes to its own daemon call.
// Before, every operation was forwarded as an enqueue, so a list, query,
// cancel or wait issued through the daemon created a job instead.
func (a *schedulerPluginAdapter) remote(ctx context.Context, owner scheduler.Owner, inputJSON string, op string) (string, error) {
	var in scheduler.ToolInput
	if len(strings.TrimSpace(inputJSON)) > 0 {
		if err := jsonDecodeImpl(inputJSON, &in); err != nil {
			return "", fmt.Errorf("scheduler %s: %w", op, err)
		}
	}
	c := a.cli.schedulerRemote
	needID := func() error {
		if strings.TrimSpace(in.ID) == "" {
			return fmt.Errorf("scheduler %s: id is required", op)
		}
		return nil
	}
	var out any
	switch op {
	case "query":
		if err := needID(); err != nil {
			return "", err
		}
		j, err := c.Query(ctx, scheduler.JobID(in.ID))
		if err != nil {
			return "", err
		}
		out = scheduler.ToolOutput{OK: true, Job: j, Status: j.Status}
	case "list":
		filter := in.Filter
		if filter.Owner == nil && owner.Kind == scheduler.OwnerAgent {
			o := owner
			filter.Owner = &o
		}
		jobs, err := c.List(ctx, filter)
		if err != nil {
			return "", err
		}
		out = scheduler.ToolOutput{OK: true, Jobs: jobs}
	case "cancel":
		if err := needID(); err != nil {
			return "", err
		}
		if err := c.Cancel(ctx, owner, scheduler.JobID(in.ID), in.Reason); err != nil {
			return "", err
		}
		out = scheduler.ToolOutput{OK: true, JobID: scheduler.JobID(in.ID), Status: scheduler.StatusCancelled}
	case "pause":
		if err := needID(); err != nil {
			return "", err
		}
		if err := c.Pause(ctx, owner, scheduler.JobID(in.ID), in.Reason); err != nil {
			return "", err
		}
		out = scheduler.ToolOutput{OK: true, JobID: scheduler.JobID(in.ID), Status: scheduler.StatusPaused}
	case "resume":
		if err := needID(); err != nil {
			return "", err
		}
		if err := c.Resume(ctx, owner, scheduler.JobID(in.ID)); err != nil {
			return "", err
		}
		out = scheduler.ToolOutput{OK: true, JobID: scheduler.JobID(in.ID), Status: scheduler.StatusPending}
	default: // schedule, wait: both enqueue a job (wait carries its condition)
		o, err := c.Enqueue(ctx, owner, in)
		if err != nil {
			return "", err
		}
		out = o
	}
	body, err := marshalJSONImpl(out)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
