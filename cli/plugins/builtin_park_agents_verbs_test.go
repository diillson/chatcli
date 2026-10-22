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
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- @park cancel ----------------------------------------------------------

type fakeParkCanceller struct {
	mu     sync.Mutex
	token  string
	reason string
	err    error
}

func (f *fakeParkCanceller) CancelPark(token, reason string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token, f.reason = token, reason
	if f.err != nil {
		return "", f.err
	}
	return "runtime: resume queued", nil
}

func saveTestPark(t *testing.T, note string, createdAt time.Time) *park.Snapshot {
	t.Helper()
	snap := &park.Snapshot{
		Token:     park.NewToken(),
		CreatedAt: createdAt,
		Park:      park.Request{Mode: park.ModeDelay, Delay: time.Minute, Note: note},
	}
	require.NoError(t, snap.Save())
	return snap
}

func TestParkPlugin_Cancel(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	f := &fakeParkCanceller{}
	SetParkCanceller(f)
	t.Cleanup(func() { SetParkCanceller(nil) })
	p := NewBuiltinParkPlugin()

	older := saveTestPark(t, "old", time.Now().Add(-time.Hour))
	newest := saveTestPark(t, "fresh", time.Now())

	// Explicit token prefix + reason, envelope form.
	out, err := p.Execute(context.Background(), []string{`{"cmd":"cancel","args":{"token":"` + older.Token[:8] + `","reason":"CI is green"}}`})
	require.NoError(t, err)
	assert.Equal(t, older.Token, f.token)
	assert.Equal(t, "CI is green", f.reason)
	assert.Contains(t, out, older.Token[:8])
	assert.Contains(t, out, "outcome=cancelled")
	assert.Contains(t, out, "runtime: resume queued")
	assert.Contains(t, out, `note="old"`)

	// No token: newest park, default reason; argv alias.
	_, err = p.Execute(context.Background(), []string{"abort"})
	require.NoError(t, err)
	assert.Equal(t, newest.Token, f.token)
	assert.NotEmpty(t, f.reason)

	// argv flags.
	_, err = p.Execute(context.Background(), []string{"cancel", "--token", older.Token[:8], "--reason", "done"})
	require.NoError(t, err)
	assert.Equal(t, older.Token, f.token)
	assert.Equal(t, "done", f.reason)

	// Runtime failure surfaces.
	f.err = errors.New("already resuming")
	_, err = p.Execute(context.Background(), []string{`{"cmd":"cancel"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already resuming")
}

func TestParkPlugin_CancelErrors(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	SetParkCanceller(nil)
	p := NewBuiltinParkPlugin()

	_, err := p.Execute(context.Background(), []string{`{"cmd":"cancel"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no parked agents")

	snap := saveTestPark(t, "", time.Now())
	_, err = p.Execute(context.Background(), []string{`{"cmd":"cancel","args":{"token":"zzzz"}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no parked agent matches")

	// No runtime wired: refuse and point at /cancel-park — the snapshot
	// must survive (deleting it would orphan the scheduler job).
	_, err = p.Execute(context.Background(), []string{`{"cmd":"cancel"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/cancel-park "+snap.Token[:8])
	_, loadErr := park.Load(snap.Token)
	require.NoError(t, loadErr)
}

func TestParkPlugin_CancelCaps(t *testing.T) {
	checkCaps(t, "@park", NewBuiltinParkPlugin(), []capsCase{
		{[]string{`{"cmd":"cancel"}`}, false, false},
		{[]string{"cancel", "--token", "abc"}, false, false},
	})
	p := NewBuiltinParkPlugin()
	assert.Contains(t, p.DescribeCall([]string{`{"cmd":"cancel","args":{"token":"b733521e9"}}`}), "b733521e")
	assert.NotEmpty(t, p.DescribeCall([]string{"cancel"}))
	assert.Contains(t, p.Schema(), `"cancel"`)
	assert.Contains(t, p.Usage(), "cancel")
}

// --- @agents wait / transcript --------------------------------------------

type fakeAgentsWatcher struct {
	fakeAgentsAdapter
	waitID      string
	waitTimeout time.Duration
	waitBlocks  bool
	tailID      string
	tail        int
}

func (f *fakeAgentsWatcher) Wait(ctx context.Context, id string, timeout time.Duration) (string, error) {
	f.mu.Lock()
	f.waitID, f.waitTimeout = id, timeout
	blocks := f.waitBlocks
	f.mu.Unlock()
	if blocks {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "RUN " + id + " FINISHED status=completed", nil
}

func (f *fakeAgentsWatcher) Transcript(id string, tail int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tailID, f.tail = id, tail
	return "ACTIVITY LOG " + id, nil
}

func TestBuiltinAgents_Wait(t *testing.T) {
	f := &fakeAgentsWatcher{}
	SetAgentsAdapter(f)
	t.Cleanup(func() { SetAgentsAdapter(nil) })
	p := NewBuiltinAgentsPlugin()

	cases := []struct {
		args        []string
		wantTimeout time.Duration
	}{
		{[]string{`{"cmd":"wait","args":{"id":"run-3"}}`}, 2 * time.Minute},
		{[]string{`{"cmd":"wait","args":{"id":"run-3","timeout":"10m"}}`}, 10 * time.Minute},
		{[]string{`{"cmd":"wait","args":{"id":"run-3","timeout":"2h"}}`}, 30 * time.Minute}, // clamped
		{[]string{`{"cmd":"wait","args":{"id":"run-3","timeout":45}}`}, 45 * time.Second},   // bare seconds
		{[]string{"wait", "run-3", "--timeout", "5m"}, 5 * time.Minute},
		{[]string{"await", "run-3"}, 2 * time.Minute},
	}
	for _, c := range cases {
		out, err := p.Execute(context.Background(), c.args)
		require.NoError(t, err, "%v", c.args)
		assert.Contains(t, out, "run-3")
		assert.Equal(t, "run-3", f.waitID)
		assert.Equal(t, c.wantTimeout, f.waitTimeout, "%v", c.args)
	}

	_, err := p.Execute(context.Background(), []string{`{"cmd":"wait","args":{"id":"run-3","timeout":"soon"}}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timeout")

	_, err = p.Execute(context.Background(), []string{`{"cmd":"wait"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing "id"`)

	// The caller's context reaches the adapter so a cancelled turn ends the wait.
	f.waitBlocks = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p.Execute(ctx, []string{"wait", "run-3"}); done <- err }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not honor ctx cancellation")
	}
}

func TestBuiltinAgents_Transcript(t *testing.T) {
	f := &fakeAgentsWatcher{}
	SetAgentsAdapter(f)
	t.Cleanup(func() { SetAgentsAdapter(nil) })
	p := NewBuiltinAgentsPlugin()

	cases := []struct {
		args     []string
		wantTail int
	}{
		{[]string{`{"cmd":"transcript","args":{"id":"run-3"}}`}, 200},
		{[]string{`{"cmd":"transcript","args":{"id":"run-3","tail":50}}`}, 50},
		{[]string{`{"cmd":"transcript","args":{"id":"run-3","tail":"25"}}`}, 25},
		{[]string{`{"cmd":"log","args":{"id":"run-3","tail":99999}}`}, 2000}, // clamped
		{[]string{"transcript", "run-3", "--tail", "10"}, 10},
		{[]string{"activity", "run-3"}, 200},
	}
	for _, c := range cases {
		out, err := p.Execute(context.Background(), c.args)
		require.NoError(t, err, "%v", c.args)
		assert.Equal(t, "ACTIVITY LOG run-3", out)
		assert.Equal(t, c.wantTail, f.tail, "%v", c.args)
	}
	_, err := p.Execute(context.Background(), []string{`{"cmd":"transcript"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing "id"`)
}

func TestBuiltinAgents_WatcherUnsupportedAndCaps(t *testing.T) {
	withFakeAgentsAdapter(t, &fakeAgentsAdapter{})
	p := NewBuiltinAgentsPlugin()
	for _, args := range [][]string{{"wait", "run-1"}, {"transcript", "run-1"}} {
		_, err := p.Execute(context.Background(), args)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not supported")
	}
	checkCaps(t, "@agents", p, []capsCase{
		{[]string{`{"cmd":"wait","args":{"id":"run-1"}}`}, true, true},
		{[]string{"transcript", "run-1", "--tail", "5"}, true, true},
		{[]string{`{"cmd":"cancel","args":{"id":"run-1"}}`}, false, false},
	})
	assert.Contains(t, p.DescribeCall([]string{`{"cmd":"wait","args":{"id":"run-7"}}`}), "run-7")
	assert.Contains(t, p.DescribeCall([]string{"transcript", "run-8"}), "run-8")
	for _, verb := range []string{`"wait"`, `"transcript"`} {
		assert.Contains(t, p.Schema(), verb)
		assert.Contains(t, p.JSONSchema(), verb)
	}
	assert.True(t, strings.Contains(p.Usage(), "wait") && strings.Contains(p.Usage(), "transcript"))
}
