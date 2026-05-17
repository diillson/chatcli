/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTodoAdapter is a deterministic TodoAdapter for unit tests. We
// record each call so we can assert exact arguments without depending
// on the live agent.TaskTracker (which lives in a different package
// and would create test cross-coupling).
type fakeTodoAdapter struct {
	mu sync.Mutex

	writeItems []TodoItem
	writeErr   error

	listOut string
	listErr error

	markID     int
	markStatus string
	markError  string
	markOut    string
	markErr    error
}

func (f *fakeTodoAdapter) Write(items []TodoItem) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeItems = items
	if f.writeErr != nil {
		return "", f.writeErr
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.Status + ":" + it.Description + "\n")
	}
	return b.String(), nil
}

func (f *fakeTodoAdapter) List() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listOut, f.listErr
}

func (f *fakeTodoAdapter) Mark(id int, status string, errMsg string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markID = id
	f.markStatus = status
	f.markError = errMsg
	if f.markErr != nil {
		return "", f.markErr
	}
	if f.markOut != "" {
		return f.markOut, nil
	}
	return "marked " + status, nil
}

// withFakeTodoAdapter installs the fake adapter for the test's
// lifetime and restores nil on cleanup.
func withFakeTodoAdapter(t *testing.T, f *fakeTodoAdapter) {
	t.Helper()
	SetTodoAdapter(f)
	t.Cleanup(func() { SetTodoAdapter(nil) })
}

// TestBuiltinTodo_NoAdapterReturnsError pins the safe-failure path:
// if no adapter is wired (agent loop not active), the plugin returns
// a clear error rather than panicking.
func TestBuiltinTodo_NoAdapterReturnsError(t *testing.T) {
	SetTodoAdapter(nil)
	t.Cleanup(func() { SetTodoAdapter(nil) })

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no adapter wired")
}

// TestBuiltinTodo_WriteHappyPath verifies the canonical TodoWrite
// shape: full list submitted, adapter receives typed items.
func TestBuiltinTodo_WriteHappyPath(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"write","args":{"todos":[
			{"description":"Investigate bug","status":"completed"},
			{"description":"Apply fix","status":"in_progress"},
			{"description":"Add tests","status":"pending"}
		]}}`})
	require.NoError(t, err)
	require.Len(t, f.writeItems, 3)
	assert.Equal(t, "Investigate bug", f.writeItems[0].Description)
	assert.Equal(t, "completed", f.writeItems[0].Status)
	assert.Equal(t, "in_progress", f.writeItems[1].Status)
	assert.Equal(t, "pending", f.writeItems[2].Status)
	assert.Contains(t, out, "Investigate bug")
}

// TestBuiltinTodo_WriteWithoutStatusDefaultsToEmpty pins the
// status-optional behavior: items without explicit status pass
// through with empty string so the adapter/tracker default applies.
func TestBuiltinTodo_WriteWithoutStatusDefaultsToEmpty(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"write","args":{"todos":[{"description":"First"}]}}`})
	require.NoError(t, err)
	require.Len(t, f.writeItems, 1)
	assert.Empty(t, f.writeItems[0].Status, "missing status passes through; adapter applies default")
}

// TestBuiltinTodo_WriteRejectsInvalidStatus pins the validation
// contract: any status other than the four canonical values is
// rejected before reaching the adapter.
func TestBuiltinTodo_WriteRejectsInvalidStatus(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"write","args":{"todos":[{"description":"x","status":"bogus"}]}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid status")
	assert.Nil(t, f.writeItems, "adapter must not be called when validation fails")
}

// TestBuiltinTodo_WriteRejectsEmptyTodos pins the empty-array case.
func TestBuiltinTodo_WriteRejectsEmptyTodos(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"write","args":{"todos":[]}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
}

// TestBuiltinTodo_WriteRejectsEmptyDescription guards against the LLM
// emitting a placeholder object — empty description is meaningless.
func TestBuiltinTodo_WriteRejectsEmptyDescription(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"write","args":{"todos":[{"description":"","status":"pending"}]}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty description")
}

// TestBuiltinTodo_WriteRequiresTodosKey pins the missing-key case.
func TestBuiltinTodo_WriteRequiresTodosKey(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"write","args":{}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "todos array is required")
}

// TestBuiltinTodo_List returns the adapter's current snapshot.
func TestBuiltinTodo_List(t *testing.T) {
	f := &fakeTodoAdapter{listOut: "1. [x] done\n2. [ ] pending\n"}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	require.NoError(t, err)
	assert.Equal(t, f.listOut, out)
}

// TestBuiltinTodo_NoArgsDefaultsToList lets the LLM ask "what's the
// plan?" with the minimal payload `[]` or even no args.
func TestBuiltinTodo_NoArgsDefaultsToList(t *testing.T) {
	f := &fakeTodoAdapter{listOut: "snapshot"}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	out, err := p.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "snapshot", out)
}

// TestBuiltinTodo_MarkHappyPath verifies single-item update.
func TestBuiltinTodo_MarkHappyPath(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"mark","args":{"id":2,"status":"completed"}}`})
	require.NoError(t, err)
	assert.Equal(t, 2, f.markID)
	assert.Equal(t, "completed", f.markStatus)
	assert.Empty(t, f.markError)
	assert.Contains(t, out, "marked completed")
}

// TestBuiltinTodo_MarkWithErrorMessage threads the optional error
// field through to the adapter for status=failed.
func TestBuiltinTodo_MarkWithErrorMessage(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"mark","args":{"id":1,"status":"failed","error":"build broke"}}`})
	require.NoError(t, err)
	assert.Equal(t, "failed", f.markStatus)
	assert.Equal(t, "build broke", f.markError)
}

// TestBuiltinTodo_MarkRejectsMissingID guards the contract.
func TestBuiltinTodo_MarkRejectsMissingID(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"mark","args":{"status":"completed"}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id is required")
}

// TestBuiltinTodo_MarkRejectsMissingStatus guards the other half.
func TestBuiltinTodo_MarkRejectsMissingStatus(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"mark","args":{"id":1}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status is required")
}

// TestBuiltinTodo_MarkPropagatesAdapterError ensures errors bubble up
// from the adapter (e.g. "no task with id=99 in plan").
func TestBuiltinTodo_MarkPropagatesAdapterError(t *testing.T) {
	f := &fakeTodoAdapter{markErr: errors.New("not found")}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(),
		[]string{`{"cmd":"mark","args":{"id":99,"status":"completed"}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestBuiltinTodo_UnknownSubcommand pins the safe-failure path for
// a typo or future subcommand the model invented.
func TestBuiltinTodo_UnknownSubcommand(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"frobnicate"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subcommand")
}

// TestBuiltinTodo_MalformedJSONReturnsError pins the JSON parse path.
func TestBuiltinTodo_MalformedJSONReturnsError(t *testing.T) {
	f := &fakeTodoAdapter{}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	_, err := p.Execute(context.Background(), []string{`{broken`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed JSON")
}

// TestBuiltinTodo_PositionalSubcommand lets the user type
// `/run @todo list` from the CLI without the JSON envelope.
func TestBuiltinTodo_PositionalSubcommand(t *testing.T) {
	f := &fakeTodoAdapter{listOut: "via positional"}
	withFakeTodoAdapter(t, f)

	p := NewBuiltinTodoPlugin()
	out, err := p.Execute(context.Background(), []string{"list"})
	require.NoError(t, err)
	assert.Equal(t, "via positional", out)
}

// TestBuiltinTodo_CapabilitiesReflectSubcommand pins the
// IsReadOnly / IsConcurrencySafe split: list is read-only, write/mark
// mutate the in-process tracker.
func TestBuiltinTodo_CapabilitiesReflectSubcommand(t *testing.T) {
	p := NewBuiltinTodoPlugin()
	assert.True(t, p.IsReadOnly([]string{`{"cmd":"list"}`}))
	assert.True(t, p.IsConcurrencySafe([]string{`{"cmd":"list"}`}))
	assert.True(t, p.IsReadOnly(nil)) // empty defaults to list
	assert.False(t, p.IsReadOnly([]string{`{"cmd":"write"}`}))
	assert.False(t, p.IsConcurrencySafe([]string{`{"cmd":"write"}`}))
	assert.False(t, p.IsReadOnly([]string{`{"cmd":"mark"}`}))
}

// TestBuiltinTodo_DescribeCallSurfacesSubcommand pins the spinner text.
func TestBuiltinTodo_DescribeCallSurfacesSubcommand(t *testing.T) {
	p := NewBuiltinTodoPlugin()
	assert.NotEmpty(t, p.DescribeCall([]string{`{"cmd":"write"}`}))
	assert.NotEmpty(t, p.DescribeCall([]string{`{"cmd":"list"}`}))
	assert.NotEmpty(t, p.DescribeCall([]string{`{"cmd":"mark","args":{"id":3}}`}))
	assert.NotEmpty(t, p.DescribeCall(nil))
}

// TestSetTodoAdapter_NilUnwires verifies the unwire path: passing nil
// stores a typed nil so currentTodoAdapter returns nil cleanly.
func TestSetTodoAdapter_NilUnwires(t *testing.T) {
	SetTodoAdapter(&fakeTodoAdapter{})
	SetTodoAdapter(nil)
	t.Cleanup(func() { SetTodoAdapter(nil) })
	assert.Nil(t, currentTodoAdapter())
}
