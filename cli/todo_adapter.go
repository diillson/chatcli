/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"sync"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/plugins"
)

// liveTodoAdapter implements plugins.TodoAdapter by routing into the
// live agent.TaskTracker that the current AgentMode owns. The adapter
// holds the tracker via a function getter so the plugin sees the most
// recent instance even after the agent is recreated between turns.
//
// The plugins package cannot import cli/agent.TaskTracker directly
// without a tighter coupling than the project convention permits;
// this file is the only place where the two worlds meet.
type liveTodoAdapter struct {
	mu         sync.Mutex
	getTracker func() *agent.TaskTracker
}

// newLiveTodoAdapter builds an adapter bound to a getter. The getter
// returns nil when no agent is currently running; the adapter surfaces
// that as a clear error rather than panicking.
func newLiveTodoAdapter(getter func() *agent.TaskTracker) *liveTodoAdapter {
	return &liveTodoAdapter{getTracker: getter}
}

// Write replaces the entire plan and returns the post-write progress
// summary. Implements plugins.TodoAdapter.
func (a *liveTodoAdapter) Write(items []plugins.TodoItem) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tracker := a.tracker()
	if tracker == nil {
		return "", fmt.Errorf("@todo write: no active agent task tracker")
	}

	specs := make([]agent.TaskSpec, 0, len(items))
	for _, it := range items {
		specs = append(specs, agent.TaskSpec{
			Description: it.Description,
			Status:      agent.TaskStatus(it.Status),
		})
	}
	tracker.SetTasks(specs)
	return tracker.FormatProgress(), nil
}

// List returns the current plan's formatted progress. Returns an empty
// summary (not an error) when no plan exists yet — that's a normal
// state at the start of an agent run.
func (a *liveTodoAdapter) List() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tracker := a.tracker()
	if tracker == nil {
		return "", fmt.Errorf("@todo list: no active agent task tracker")
	}
	progress := tracker.FormatProgress()
	if progress == "" {
		return "(no plan)\n", nil
	}
	return progress, nil
}

// Mark updates one task by ID. Returns the post-mark progress so the
// LLM sees the resulting state.
func (a *liveTodoAdapter) Mark(id int, status string, errorMsg string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	tracker := a.tracker()
	if tracker == nil {
		return "", fmt.Errorf("@todo mark: no active agent task tracker")
	}

	ts := agent.TaskStatus(status)
	switch ts {
	case agent.TaskPending, agent.TaskInProgress, agent.TaskCompleted, agent.TaskFailed:
		// ok
	default:
		return "", fmt.Errorf("@todo mark: invalid status %q (expected pending|in_progress|completed|failed)", status)
	}

	if !tracker.MarkByID(id, ts, errorMsg) {
		return "", fmt.Errorf("@todo mark: no task with id=%d in the current plan", id)
	}
	return tracker.FormatProgress(), nil
}

// tracker is the locked-context getter — must hold a.mu.
func (a *liveTodoAdapter) tracker() *agent.TaskTracker {
	if a.getTracker == nil {
		return nil
	}
	return a.getTracker()
}
