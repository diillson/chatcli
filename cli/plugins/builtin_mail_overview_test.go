/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMailOverviewer struct {
	fakeMailPeeker
	overviews int
}

func (f *fakeMailOverviewer) Overview() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overviews++
	return "ok:overview", nil
}

// The argv here is what the agent loop hands the plugin after flattening
// {"cmd":"overview"} and {"cmd":"overview","args":{...}}.
func TestBuiltinMail_OverviewFlattened(t *testing.T) {
	f := &fakeMailOverviewer{}
	SetMailAdapter(f)
	t.Cleanup(func() { SetMailAdapter(nil) })
	p := NewBuiltinMailPlugin()

	for _, args := range [][]string{{"overview"}, {"counts"}, {`{"cmd":"overview"}`}, {`{"cmd":"overview","args":{}}`}} {
		out, err := p.Execute(context.Background(), args)
		require.NoError(t, err, "%v", args)
		assert.Equal(t, "ok:overview", out)
	}
	assert.Equal(t, 4, f.overviews)

	// An argument means the caller wanted one inbox: refused, pointing at
	// peek, instead of answering with every queue.
	for _, args := range [][]string{{"overview", "--for", "coder"}, {"overview", "coder"}, {`{"cmd":"overview","args":{"limit":3}}`}} {
		_, err := p.Execute(context.Background(), args)
		require.Error(t, err, "%v", args)
		assert.Contains(t, err.Error(), "takes no arguments")
		assert.Contains(t, err.Error(), "peek")
	}
	assert.Equal(t, 4, f.overviews)

	// "pending" stays the alias of peek it has always been.
	out, err := p.Execute(context.Background(), []string{"pending", "--for", "coder"})
	require.NoError(t, err)
	assert.Equal(t, "ok:peek:coder", out)
	assert.Equal(t, 4, f.overviews)

	checkCaps(t, "@mail", p, []capsCase{
		{[]string{"overview"}, true, true},
		{[]string{`{"cmd":"overview"}`}, true, true},
	})
	assert.NotEqual(t, p.DescribeCall([]string{"overview"}), p.DescribeCall([]string{"inbox"}))
	assert.Contains(t, p.Usage(), "overview")
	assert.Contains(t, p.Schema(), `"overview"`)
	assert.Contains(t, p.JSONSchema(), `"overview"`)

	_, err = p.Execute(context.Background(), []string{"bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overview")

	// An adapter that predates the verb says so instead of failing oddly.
	SetMailAdapter(&fakeMailPeeker{})
	_, err = p.Execute(context.Background(), []string{"overview"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}
