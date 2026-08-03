/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBoardAdapter records the last call per method.
type fakeBoardAdapter struct {
	mu       sync.Mutex
	lastCall string
	lastArgs []string
}

func (f *fakeBoardAdapter) record(call string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCall = call
	f.lastArgs = args
	return "ok:" + call, nil
}

func (f *fakeBoardAdapter) Create(title, description, assignee, column string) (string, error) {
	return f.record("create", title, description, assignee, column)
}
func (f *fakeBoardAdapter) List(column string) (string, error) { return f.record("list", column) }
func (f *fakeBoardAdapter) Show(id string) (string, error)     { return f.record("show", id) }
func (f *fakeBoardAdapter) Move(id, to, by string) (string, error) {
	return f.record("move", id, to, by)
}
func (f *fakeBoardAdapter) Assign(id, assignee string) (string, error) {
	return f.record("assign", id, assignee)
}
func (f *fakeBoardAdapter) Note(id, author, text string) (string, error) {
	return f.record("note", id, author, text)
}
func (f *fakeBoardAdapter) Link(id, runID, jobID string) (string, error) {
	return f.record("link", id, runID, jobID)
}
func (f *fakeBoardAdapter) Archive(olderThan string) (string, error) {
	return f.record("archive", olderThan)
}

func withFakeBoardAdapter(t *testing.T) *fakeBoardAdapter {
	t.Helper()
	f := &fakeBoardAdapter{}
	SetBoardAdapter(f)
	t.Cleanup(func() { SetBoardAdapter(nil) })
	return f
}

func TestBuiltinBoard_NoAdapterReturnsError(t *testing.T) {
	SetBoardAdapter(nil)
	t.Cleanup(func() { SetBoardAdapter(nil) })
	p := NewBuiltinBoardPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no adapter wired")
}

func TestBuiltinBoard_CreateEnvelope(t *testing.T) {
	f := withFakeBoardAdapter(t)
	p := NewBuiltinBoardPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"create","args":{"title":"Fix bug","description":"details","assignee":"coder","column":"doing"}}`})
	require.NoError(t, err)
	assert.Equal(t, "ok:create", out)
	assert.Equal(t, []string{"Fix bug", "details", "coder", "doing"}, f.lastArgs)
}

func TestBuiltinBoard_CreateMissingTitle(t *testing.T) {
	f := withFakeBoardAdapter(t)
	p := NewBuiltinBoardPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"create","args":{"description":"x"}}`})
	require.Error(t, err)
	assert.Empty(t, f.lastCall, "adapter must not be called on validation failure")
}

func TestBuiltinBoard_ListDefaultAndMoveArgv(t *testing.T) {
	f := withFakeBoardAdapter(t)
	p := NewBuiltinBoardPlugin()

	_, err := p.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "list", f.lastCall)

	// argv form: "move card-3 review".
	_, err = p.Execute(context.Background(), []string{"move", "card-3", "review"})
	require.NoError(t, err)
	assert.Equal(t, "move", f.lastCall)
	assert.Equal(t, "card-3", f.lastArgs[0])
	assert.Equal(t, "review", f.lastArgs[1])
}

func TestBuiltinBoard_NoteRequiresIDAndText(t *testing.T) {
	f := withFakeBoardAdapter(t)
	p := NewBuiltinBoardPlugin()

	_, err := p.Execute(context.Background(), []string{`{"cmd":"note","args":{"id":"card-1"}}`})
	require.Error(t, err)
	assert.Empty(t, f.lastCall)

	_, err = p.Execute(context.Background(),
		[]string{`{"cmd":"note","args":{"id":"card-1","text":"review passed","author":"reviewer"}}`})
	require.NoError(t, err)
	assert.Equal(t, []string{"card-1", "reviewer", "review passed"}, f.lastArgs)
}

func TestBuiltinBoard_LinkRequiresTarget(t *testing.T) {
	f := withFakeBoardAdapter(t)
	p := NewBuiltinBoardPlugin()

	_, err := p.Execute(context.Background(), []string{`{"cmd":"link","args":{"id":"card-1"}}`})
	require.Error(t, err)
	assert.Empty(t, f.lastCall)

	_, err = p.Execute(context.Background(), []string{`{"cmd":"link","args":{"id":"card-1","run_id":"run-9"}}`})
	require.NoError(t, err)
	assert.Equal(t, []string{"card-1", "run-9", ""}, f.lastArgs)
}

func TestBuiltinBoard_UnknownSubcommand(t *testing.T) {
	withFakeBoardAdapter(t)
	p := NewBuiltinBoardPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"destroy"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subcommand")
}

func TestBuiltinBoard_Capabilities(t *testing.T) {
	p := NewBuiltinBoardPlugin()

	assert.True(t, p.IsReadOnly([]string{`{"cmd":"list"}`}))
	assert.True(t, p.IsReadOnly([]string{"show", "card-1"}))
	assert.False(t, p.IsReadOnly([]string{`{"cmd":"move","args":{"id":"card-1","to":"done"}}`}))
	assert.False(t, p.IsReadOnly([]string{"create", "New card"}))

	assert.True(t, p.IsConcurrencySafe(nil))
	assert.False(t, p.IsConcurrencySafe([]string{"note", "card-1", "x"}))

	assert.NotEmpty(t, p.DescribeCall([]string{`{"cmd":"move","args":{"id":"card-1","to":"done"}}`}))
	assert.NotEmpty(t, p.Schema())
	assert.NotEmpty(t, p.JSONSchema())
}
