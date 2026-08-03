/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAgentsAdapter records calls and returns canned responses.
type fakeAgentsAdapter struct {
	mu        sync.Mutex
	listCalls int
	showID    string
	cancelID  string
	failWith  error
}

func (f *fakeAgentsAdapter) List() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.failWith != nil {
		return "", f.failWith
	}
	return "ACTIVE RUNS (1):\nrun-1 kind=worker agent=coder status=running", nil
}

func (f *fakeAgentsAdapter) Show(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.showID = id
	if f.failWith != nil {
		return "", f.failWith
	}
	return "id=" + id + "\nstatus=running", nil
}

func (f *fakeAgentsAdapter) Cancel(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelID = id
	if f.failWith != nil {
		return "", f.failWith
	}
	return "cancellation requested for " + id, nil
}

func withFakeAgentsAdapter(t *testing.T, f *fakeAgentsAdapter) {
	t.Helper()
	SetAgentsAdapter(f)
	t.Cleanup(func() { SetAgentsAdapter(nil) })
}

func TestBuiltinAgents_NoAdapterReturnsError(t *testing.T) {
	SetAgentsAdapter(nil)
	t.Cleanup(func() { SetAgentsAdapter(nil) })
	p := NewBuiltinAgentsPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no adapter wired")
}

func TestBuiltinAgents_ListDefaultAndAliases(t *testing.T) {
	f := &fakeAgentsAdapter{}
	withFakeAgentsAdapter(t, f)
	p := NewBuiltinAgentsPlugin()

	for _, args := range [][]string{
		nil,                  // no args → list
		{`{"cmd":"list"}`},   // canonical envelope
		{"ls"},               // argv alias
		{`{"cmd":"status"}`}, // alias in envelope
	} {
		out, err := p.Execute(context.Background(), args)
		require.NoError(t, err, "args=%v", args)
		assert.Contains(t, out, "run-1")
	}
	assert.Equal(t, 4, f.listCalls)
}

func TestBuiltinAgents_ShowEnvelopeAndArgv(t *testing.T) {
	f := &fakeAgentsAdapter{}
	withFakeAgentsAdapter(t, f)
	p := NewBuiltinAgentsPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"show","args":{"id":"run-3"}}`})
	require.NoError(t, err)
	assert.Contains(t, out, "id=run-3")
	assert.Equal(t, "run-3", f.showID)

	// Positional argv form: "show run-7".
	_, err = p.Execute(context.Background(), []string{"show", "run-7"})
	require.NoError(t, err)
	assert.Equal(t, "run-7", f.showID)

	// Flag argv form: "show --id run-9".
	_, err = p.Execute(context.Background(), []string{"show", "--id", "run-9"})
	require.NoError(t, err)
	assert.Equal(t, "run-9", f.showID)

	// Flat envelope (id at top level) must also work.
	_, err = p.Execute(context.Background(), []string{`{"cmd":"show","id":"run-11"}`})
	require.NoError(t, err)
	assert.Equal(t, "run-11", f.showID)
}

func TestBuiltinAgents_ShowMissingID(t *testing.T) {
	f := &fakeAgentsAdapter{}
	withFakeAgentsAdapter(t, f)
	p := NewBuiltinAgentsPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"show"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
	assert.Empty(t, f.showID, "adapter must not be called when validation fails")
}

func TestBuiltinAgents_CancelHappyPathAndError(t *testing.T) {
	f := &fakeAgentsAdapter{}
	withFakeAgentsAdapter(t, f)
	p := NewBuiltinAgentsPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"cancel","args":{"id":"run-2"}}`})
	require.NoError(t, err)
	assert.Contains(t, out, "run-2")
	assert.Equal(t, "run-2", f.cancelID)

	f.failWith = errors.New("already finished")
	_, err = p.Execute(context.Background(), []string{"cancel", "run-2"})
	require.Error(t, err)
}

func TestBuiltinAgents_UnknownSubcommand(t *testing.T) {
	withFakeAgentsAdapter(t, &fakeAgentsAdapter{})
	p := NewBuiltinAgentsPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"restart"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subcommand")
}

func TestBuiltinAgents_MalformedJSON(t *testing.T) {
	withFakeAgentsAdapter(t, &fakeAgentsAdapter{})
	p := NewBuiltinAgentsPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"list"`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed JSON")
}

func TestBuiltinAgents_Capabilities(t *testing.T) {
	p := NewBuiltinAgentsPlugin()

	assert.True(t, p.IsReadOnly([]string{`{"cmd":"list"}`}))
	assert.True(t, p.IsReadOnly([]string{"show", "run-1"}))
	assert.False(t, p.IsReadOnly([]string{`{"cmd":"cancel","args":{"id":"run-1"}}`}))
	assert.False(t, p.IsReadOnly([]string{"stop", "run-1"}))

	assert.True(t, p.IsConcurrencySafe(nil))
	assert.False(t, p.IsConcurrencySafe([]string{"cancel", "run-1"}))

	assert.NotEmpty(t, p.DescribeCall([]string{`{"cmd":"cancel","args":{"id":"run-1"}}`}))
	assert.NotEmpty(t, p.Schema())
	assert.NotEmpty(t, p.JSONSchema())
}
