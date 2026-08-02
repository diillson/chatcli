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

// fakeMailAdapter records the last call per method.
type fakeMailAdapter struct {
	mu        sync.Mutex
	lastCall  string
	lastArgs  []string
	lastLimit int
}

func (f *fakeMailAdapter) Send(to, cardID, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCall = "send"
	f.lastArgs = []string{to, cardID, text}
	return "ok:send", nil
}

func (f *fakeMailAdapter) Inbox() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCall = "inbox"
	return "ok:inbox", nil
}

func (f *fakeMailAdapter) History(n int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCall = "history"
	f.lastLimit = n
	return "ok:history", nil
}

func withFakeMailAdapter(t *testing.T) *fakeMailAdapter {
	t.Helper()
	f := &fakeMailAdapter{}
	SetMailAdapter(f)
	t.Cleanup(func() { SetMailAdapter(nil) })
	return f
}

func TestBuiltinMail_NoAdapterReturnsError(t *testing.T) {
	SetMailAdapter(nil)
	t.Cleanup(func() { SetMailAdapter(nil) })
	p := NewBuiltinMailPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"inbox"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no adapter wired")
}

func TestBuiltinMail_SendEnvelopeAndArgv(t *testing.T) {
	f := withFakeMailAdapter(t)
	p := NewBuiltinMailPlugin()

	out, err := p.Execute(context.Background(),
		[]string{`{"cmd":"send","args":{"to":"coder","text":"fix the tests","card_id":"card-2"}}`})
	require.NoError(t, err)
	assert.Equal(t, "ok:send", out)
	assert.Equal(t, []string{"coder", "card-2", "fix the tests"}, f.lastArgs)

	// argv form: "send coder fix the login flow".
	_, err = p.Execute(context.Background(), []string{"send", "coder", "fix", "the", "login", "flow"})
	require.NoError(t, err)
	assert.Equal(t, []string{"coder", "", "fix the login flow"}, f.lastArgs)
}

func TestBuiltinMail_SendValidation(t *testing.T) {
	f := withFakeMailAdapter(t)
	p := NewBuiltinMailPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"send","args":{"to":"coder"}}`})
	require.Error(t, err)
	assert.Empty(t, f.lastCall, "adapter must not be called on validation failure")
}

func TestBuiltinMail_InboxDefaultAndHistory(t *testing.T) {
	f := withFakeMailAdapter(t)
	p := NewBuiltinMailPlugin()

	_, err := p.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "inbox", f.lastCall)

	_, err = p.Execute(context.Background(), []string{`{"cmd":"history","args":{"limit":5}}`})
	require.NoError(t, err)
	assert.Equal(t, "history", f.lastCall)
	assert.Equal(t, 5, f.lastLimit)

	// Default limit applies when omitted.
	_, err = p.Execute(context.Background(), []string{`{"cmd":"history"}`})
	require.NoError(t, err)
	assert.Equal(t, 20, f.lastLimit)
}

func TestBuiltinMail_UnknownSubcommand(t *testing.T) {
	withFakeMailAdapter(t)
	p := NewBuiltinMailPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"broadcast"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subcommand")
}

func TestBuiltinMail_Capabilities(t *testing.T) {
	p := NewBuiltinMailPlugin()
	assert.True(t, p.IsReadOnly([]string{`{"cmd":"history"}`}))
	assert.False(t, p.IsReadOnly([]string{`{"cmd":"send","args":{"to":"coder","text":"x"}}`}))
	assert.False(t, p.IsReadOnly(nil)) // inbox drains → mutating
	assert.True(t, p.IsConcurrencySafe(nil))
	assert.NotEmpty(t, p.DescribeCall([]string{`{"cmd":"send","args":{"to":"coder","text":"x"}}`}))
	assert.NotEmpty(t, p.Schema())
	assert.NotEmpty(t, p.JSONSchema())
}
