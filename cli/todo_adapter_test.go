/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestLiveTodoAdapter_WriteThenList exercises the full happy path
// against a real agent.TaskTracker. The tracker is the production
// type — no mocks here — so a regression in SetTasks / FormatProgress
// surfaces at this site instead of at runtime in a real /coder turn.
func TestLiveTodoAdapter_WriteThenList(t *testing.T) {
	tracker := agent.NewTaskTracker(zap.NewNop())
	adapter := newLiveTodoAdapter(func() *agent.TaskTracker { return tracker })

	out, err := adapter.Write([]plugins.TodoItem{
		{Description: "Investigate bug", Status: "completed"},
		{Description: "Apply fix", Status: "in_progress"},
		{Description: "Add regression test", Status: "pending"},
	})
	require.NoError(t, err)
	assert.Contains(t, out, "Investigate bug")
	assert.Contains(t, out, "[x]", "completed task must render with [x] icon")
	assert.Contains(t, out, "[>]", "in_progress task must render with [>] icon")
	assert.Contains(t, out, "[ ]", "pending task must render with [ ] icon")

	list, err := adapter.List()
	require.NoError(t, err)
	assert.Equal(t, out, list, "list after write returns the same snapshot")
}

// TestLiveTodoAdapter_MarkByID exercises the single-item update path
// through the real tracker. Mark must update the named task and leave
// the rest of the plan intact.
func TestLiveTodoAdapter_MarkByID(t *testing.T) {
	tracker := agent.NewTaskTracker(zap.NewNop())
	adapter := newLiveTodoAdapter(func() *agent.TaskTracker { return tracker })

	_, err := adapter.Write([]plugins.TodoItem{
		{Description: "First", Status: "pending"},
		{Description: "Second", Status: "pending"},
		{Description: "Third", Status: "pending"},
	})
	require.NoError(t, err)

	out, err := adapter.Mark(2, "completed", "")
	require.NoError(t, err)
	// Find the line containing "Second" — it must show the completed marker.
	var secondLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Second") {
			secondLine = line
			break
		}
	}
	require.NotEmpty(t, secondLine)
	assert.Contains(t, secondLine, "[x]", "Mark(2,completed) must flip task #2 to [x]")
}

// TestLiveTodoAdapter_MarkRejectsInvalidStatus pins the validation
// boundary at the cli layer: even before reaching the tracker, an
// unknown status is rejected with a clear message.
func TestLiveTodoAdapter_MarkRejectsInvalidStatus(t *testing.T) {
	tracker := agent.NewTaskTracker(zap.NewNop())
	adapter := newLiveTodoAdapter(func() *agent.TaskTracker { return tracker })

	_, err := adapter.Write([]plugins.TodoItem{{Description: "X", Status: "pending"}})
	require.NoError(t, err)

	_, err = adapter.Mark(1, "bogus", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid status")
}

// TestLiveTodoAdapter_MarkRejectsUnknownID guards the out-of-range case.
func TestLiveTodoAdapter_MarkRejectsUnknownID(t *testing.T) {
	tracker := agent.NewTaskTracker(zap.NewNop())
	adapter := newLiveTodoAdapter(func() *agent.TaskTracker { return tracker })

	_, err := adapter.Write([]plugins.TodoItem{{Description: "X", Status: "pending"}})
	require.NoError(t, err)

	_, err = adapter.Mark(99, "completed", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no task with id=99")
}

// TestLiveTodoAdapter_ListWhenNoPlan returns the "(no plan)" sentinel
// rather than an empty string so the LLM sees a clear "nothing here"
// message.
func TestLiveTodoAdapter_ListWhenNoPlan(t *testing.T) {
	tracker := agent.NewTaskTracker(zap.NewNop())
	adapter := newLiveTodoAdapter(func() *agent.TaskTracker { return tracker })

	out, err := adapter.List()
	require.NoError(t, err)
	assert.Contains(t, out, "no plan")
}

// TestLiveTodoAdapter_NilTrackerSurfacesError pins the safe-failure
// path: when the getter returns nil (agent not started), Write/List/
// Mark fail with a clear message rather than panicking.
func TestLiveTodoAdapter_NilTrackerSurfacesError(t *testing.T) {
	adapter := newLiveTodoAdapter(func() *agent.TaskTracker { return nil })

	_, err := adapter.Write([]plugins.TodoItem{{Description: "X"}})
	require.Error(t, err)
	_, err = adapter.List()
	require.Error(t, err)
	_, err = adapter.Mark(1, "completed", "")
	require.Error(t, err)
}

// TestLiveTodoAdapter_StatusDefaultsToPending exercises the empty-
// status promotion: an item with no status falls through to Pending
// at the tracker layer (SetTasks).
func TestLiveTodoAdapter_StatusDefaultsToPending(t *testing.T) {
	tracker := agent.NewTaskTracker(zap.NewNop())
	adapter := newLiveTodoAdapter(func() *agent.TaskTracker { return tracker })

	out, err := adapter.Write([]plugins.TodoItem{{Description: "X"}})
	require.NoError(t, err)
	assert.Contains(t, out, "[ ]", "empty status defaults to pending [ ]")
}
